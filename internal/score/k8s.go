package score

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/score-spec/score-go/types"
	core "k8s.io/api/core/v1"
	k8s "k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/humanitec/cloudrun-container-runner/internal/inputs"
	"github.com/humanitec/cloudrun-container-runner/internal/utils"
)

type K8sScoreConverter struct {
	WorkloadResource
	EnvVarSecretResolver  EnvVarSecretResolver
	ContainerFileResolver ContainerFileResolver
	EnvVarOverride        func(containerName string) ([]core.EnvVar, error)
}

// EnvVarSecretResolver returns an core.EnvVarSource object.
// name is guaranteed to be a valid Secret key and be unique within the workload.
type EnvVarSecretResolver func(name string, secret *inputs.SecretInput) (core.EnvVarSource, error)

// ContainerFileResolver returns a volume that will be used as the container file
type ContainerFileResolver func(workloadRes WorkloadResource, volumeName, dir string, files map[string]*types.ContainerFile, containerName string) (core.Volume, error)

var k8sEvnVarExpansion = regexp.MustCompile(`\$+\([a-zA-Z_][a-zA-Z0-9_]*\)`)

func ptr[T any](t T) *T {
	return &t
}

func ptrStr(s string) *string {
	return &s
}

func strFirstN(s string, n int) string {
	l := len(s)
	if n > l {
		return s
	}
	return s[0 : n-1]
}
func strLastN(s string, n int) string {
	l := len(s)
	if n > l {
		return s
	}
	return s[l-n:]
}
func hashStr(s string) string {
	h := sha1.Sum([]byte(s))
	return hex.EncodeToString(h[:])
}

// escapeK8sEnvVar escapes $ such that the string can be passed through
// score placeholder resolution AND kubernetes EnvVar resolution.
// In both cases, $$ is substituted for $.
// However, the user is only aware of score substitution. This means that
// If a user intends the string $() it needs to be escaped to $$$$() so that
// Score escaping gives $$() which leaves K8s to do $()
func escapeK8sEnvVar(str string) string {
	// Account for the $ escaping that K8s does on top of Score
	str = strings.ReplaceAll(str, "$$", "$$$$")
	expansions := k8sEvnVarExpansion.FindAllStringIndex(str, -1)
	out := strings.Builder{}
	lastIndex := 0
	for _, expansion := range expansions {
		out.WriteString(str[lastIndex:expansion[0]])
		count := 0
		for i := expansion[0]; str[i] == '$'; i++ {
			count++
		}

		if count%2 == 1 {
			out.WriteString("$$$")
		}
		if count%4 == 2 {
			out.WriteString("$$")
		}
		out.WriteString(str[expansion[0]:expansion[1]])
		lastIndex = expansion[1]
	}
	out.WriteString(str[lastIndex:])
	return out.String()
}

// POSIX Env Vars must can be alphanumeric with "_". They cannot start with a number.
var illegalPosixEnvVarChars = regexp.MustCompile(`[^a-zA-Z0-9_]`)

func placeholderToVar(placeholder string) string {
	return "__SECRET__" + illegalPosixEnvVarChars.ReplaceAllString(strings.TrimPrefix(placeholder, "resources."), "_") + "__"
}

func NoSecretsResolver(secret *inputs.SecretInput) (core.EnvVar, error) {
	return core.EnvVar{}, fmt.Errorf("no secrets expected")
}

// K8sEnvFromScore generates a slice compatible with Kubernetes corev1.EnvVar
//
// Inputs:
//
// - workloadName is the raw name as specified in the deployment
//
// - workload is the Score Workload
//
// - containername is the container to generate the envVar slice for
//
// - params is the Params that the workload instance is provisioned with
//
// Notes:
//
// In order to support using placeholders to generate environment variables
// that may contain secrets - such as a connection string, all secrets are
// assigned to auto-generated environment variables which are then expanded
// using the "$(...)" expansion mechanism described in the EnvVar Value
// documentation.
func (c *K8sScoreConverter) EnvVar(containerName string) ([]core.EnvVar, error) {
	if c.EnvVarOverride != nil {
		return c.EnvVarOverride(containerName)
	}
	var env []core.EnvVar
	if container, exists := c.Workload.Containers[containerName]; exists {
		if len(container.Variables) > 0 {
			placeholders := GetAllPlaceholders(container.Variables)
			placeholderStrs := map[string]string{}
			secretVars := map[string]*inputs.SecretInput{}
			for _, placeholder := range placeholders {
				output, err := c.OutputForPlaceholder(placeholder, containerName)
				if err != nil {
					return nil, err
				}
				if output.Secret != nil {
					secretVars[placeholder] = output.Secret
					placeholderStrs[placeholder] = "$(" + placeholderToVar(placeholder) + ")"
				} else if output.Value != nil {
					if str, ok := output.Value.(string); ok {
						placeholderStrs[placeholder] = str
					} else {
						b, err := json.Marshal(output.Value)
						if err != nil {
							return nil, fmt.Errorf("resolving placeholder ${%s}: %w", placeholder, err)
						}
						placeholderStrs[placeholder] = string(b)
					}
				}
			}
			env = make([]core.EnvVar, 0, len(container.Variables)+len(secretVars))
			for placeholder, secretVar := range secretVars {
				valueFrom, err := c.EnvVarSecretResolver(placeholder, secretVar)
				if err != nil {
					return nil, fmt.Errorf("resolving secret for placeholder ${%s}: %w", placeholder, err)
				}
				env = append(env, core.EnvVar{
					Name:      placeholderToVar(placeholder),
					ValueFrom: &valueFrom,
				})
			}
			for name, value := range container.Variables {
				value = escapeK8sEnvVar(value)
				replacedVal, err := ReplaceAllPlaceholdersInString(value, placeholderStrs)
				if err != nil {
					return nil, fmt.Errorf("resolving variable %s in container %s: %w", name, containerName, err)
				}
				env = append(env, core.EnvVar{
					Name:  name,
					Value: replacedVal,
				})
			}
			slices.SortFunc(env, func(a, b core.EnvVar) int {
				return strings.Compare(a.Name, b.Name)
			})
		}
	}
	return env, nil
}

// ContainerFiles generates a slice compatible with Kubernetes corev1.Volumes
// and a map of VolumeMounts for each volume
//
// Inputs:
//
// - workloadName is the raw name as specified in the deployment
//
// - workload is the Score Workload
//
// - params is the Params that the workload instance is provisioned with
//
//   - fileResolver is a function that returns a K8s volume for the file. The
//     volume should have the name supplied in volumeName.
//
// NOTE:
//
// This initial implementation creates a colume per common directory containing
// files.  It does not attempt to minimise the number of volumes. Volumes will
// be repeated acorss volumes. E.g. the following:
//
//	containers:
//	  one:
//	    files:
//	     /a/b/alpha.txt:
//	       content: ${resources.alpha.password}
//	     /a/b/beta.txt:
//	       content: ${resources.beta.password}
//	     /a/c/gamma.txt:
//	       content: ${resources.gamma.password}
//	  two:
//	    files:
//	     /a/c/gamma.txt:
//	       content: ${resources.gamma.password}
//
// will result in 3 volumes and 3 volume mounts. It could be possible to reduce
// the volumes to two and have 3 volume mounts, but this will not be done.
//
// Raw value - can be written to a Confimap
// Raw secret - can be written to a K8s secret
// K8s secret + key - use projected
func (c *K8sScoreConverter) ContainerFiles() (map[string][]core.VolumeMount, []core.Volume, error) {
	volumes := make([]core.Volume, 0)
	// container -> Volume mounts
	volumeMounts := map[string][]core.VolumeMount{}

	for containerName, container := range c.Workload.Containers {
		dirs := map[string]map[string]*types.ContainerFile{}
		for filePath, fileObj := range container.Files {
			dir, name := path.Split(filePath)
			dir = path.Clean(dir)
			if _, exists := dirs[dir]; !exists {
				dirs[dir] = map[string]*types.ContainerFile{}
			}
			dirs[dir][name] = &fileObj
		}
		if len(dirs) == 0 {
			continue
		}

		volumeMounts[containerName] = []core.VolumeMount{}

		for dir, files := range dirs {
			if !path.IsAbs(dir) {
				return nil, nil, fmt.Errorf("processing containers.%s.files: \"%s\" is not an absolute path", containerName, dir)
			}
			// guranateed to be 63 characters or less to comply with DNS Label requirement
			volumeName := utils.ToSlug(fmt.Sprintf("%s-%s",
				strLastN(strings.ReplaceAll(dir, "/", "-")[1:], 32),
				strFirstN(hashStr(fmt.Sprintf("%s-%s-%s", c.Name, containerName, dir)), 30),
			))

			volume, err := c.ContainerFileResolver(c.WorkloadResource, volumeName, dir, files, containerName)
			if err != nil {
				return nil, nil, err
			}
			if volume.Name != volumeName {
				return nil, nil, fmt.Errorf("generated volume name is not supplied name, got %s want %s", volume.Name, volumeName)
			}
			volumeMounts[containerName] = append(volumeMounts[containerName], core.VolumeMount{
				Name:      volume.Name,
				ReadOnly:  true,
				MountPath: dir,
			})
			volumes = append(volumes, volume)
		}

		// Avoid arbitrary changes in order due to map -> slice conversion
		slices.SortFunc(volumeMounts[containerName], func(a, b core.VolumeMount) int {
			return strings.Compare(a.Name, b.Name)
		})
	}
	slices.SortFunc(volumes, func(a, b core.Volume) int {
		return strings.Compare(a.Name, b.Name)
	})
	return volumeMounts, volumes, nil
}

// Volumes generates a slice compatible with Kubernetes corev1.Volume
func (c *K8sScoreConverter) Volumes(platform string) (map[string][]core.VolumeMount, []core.Volume, error) {
	volumes := map[string]core.Volume{}
	volumeMounts := map[string][]core.VolumeMount{}

	for containerName, container := range c.Workload.Containers {
		if len(container.Volumes) > 0 {
			volumeMounts[containerName] = []core.VolumeMount{}
		}
		// TODO: this should be revisited after adding a volume placeholder substitution to the substitution map
		//for mountPath, volume := range container.Volumes {
		//	placeholders := GetAllPlaceholdersInString(volume.Source)
		//	if len(placeholders) != 1 || !strings.HasPrefix(volume.Source, "${") || !strings.HasSuffix(volume.Source, "}") {
		//		return nil, nil, fmt.Errorf("resolving volume containers.%s.volumes.%s: source must be a placeholder referencing a volume, got \"%s\"", containerName, mountPath, volume.Source)
		//	}
		//	parts := strings.Split(placeholders[0], ".")
		//	if len(parts) != 2 || parts[0] != "resources" {
		//		return nil, nil, fmt.Errorf("resolving volume containers.%s.volumes.%s: source must be a placeholder referencing a volume, i.e. of the form ${resources.NAME} got ${%s}", containerName, mountPath, placeholders[0])
		//	}
		//	if volumeRes, exists := c.Workload.Resources[parts[1]]; exists {
		//		if volumeRes.Type != "volume" {
		//			return nil, nil, fmt.Errorf("resolving volume containers.%s.volumes.%s: placeholder ${%s} in source is not of type volume, got %s", containerName, mountPath, placeholders[0], volumeRes.Type)
		//		}
		//	} else {
		//		return nil, nil, fmt.Errorf("resolving volume containers.%s.volumes.%s: placeholder ${%s} cannot be resolved: no resource with name %s", containerName, mountPath, placeholders[0], parts[1])
		//	}
		//
		//	volName := strings.ReplaceAll(placeholders[0], ".", "-")
		//
		//	if _, exists := volumes[volName]; !exists {
		//		output, err := c.WorkloadResource.OutputForPlaceholder(placeholders[0]+"."+platform, containerName)
		//		if err != nil {
		//			return nil, nil, fmt.Errorf("resolving volume resource %s: %w", placeholders[0], err)
		//		}
		//		if outputAsMap, ok := output.Value.(map[string]any); !ok {
		//			return nil, nil, fmt.Errorf("resolving volume resource %s: invalid output for platform %s, expected object, got %T", placeholders[0], platform, output)
		//		} else {
		//			var k8sVolume core.Volume
		//			if err := utils.DecodeViaJSON(outputAsMap, &k8sVolume); err != nil {
		//				return nil, nil, fmt.Errorf("resolving volume resource %s: unable to parse output for platform %s as k8s volume: %w", placeholders[0], platform, err)
		//			}
		//			k8sVolume.Name = volName
		//			volumes[volName] = k8sVolume
		//		}
		//	}
		//	subPath := ""
		//	if volume.Path != nil {
		//		subPath = *volume.Path
		//	}
		//
		//	readOnly := false
		//	var recursiveReadOnlyMode *core.RecursiveReadOnlyMode
		//	if volume.ReadOnly != nil {
		//		readOnly = *volume.ReadOnly
		//		r := core.RecursiveReadOnlyIfPossible
		//		recursiveReadOnlyMode = &r
		//	}
		//	volumeMounts[containerName] = append(volumeMounts[containerName], core.VolumeMount{
		//		Name:              volName,
		//		ReadOnly:          readOnly,
		//		RecursiveReadOnly: recursiveReadOnlyMode,
		//		MountPath:         mountPath,
		//		SubPath:           subPath,
		//		MountPropagation:  nil, // Default ti "None"
		//		SubPathExpr:       "",  // Mutually exclusive to SubPath
		//	})
		//}
	}
	volumesAsSlice := make([]core.Volume, 0, len(volumes))
	for _, volume := range volumes {
		volumesAsSlice = append(volumesAsSlice, volume)
	}
	return volumeMounts, volumesAsSlice, nil
}

func (c *K8sScoreConverter) PodSpec(platform string) (core.PodSpec, error) {
	volumeMounts, volumes, err := c.Volumes(platform)
	if err != nil {
		return core.PodSpec{}, err
	}
	fileVolumeMounts, fileVolumes, err := c.ContainerFiles()
	if err != nil {
		return core.PodSpec{}, err
	}
	volumes = append(volumes, fileVolumes...)
	if len(volumes) == 0 {
		// Stay as true as possible to the defaults, nil rather than empty slice
		volumes = nil
	} else {
		slices.SortFunc(volumes, func(a, b core.Volume) int {
			return strings.Compare(a.Name, b.Name)
		})
	}
	containers := make([]core.Container, 0)
	for containerName, scoreContainer := range c.Workload.Containers {
		envVars, err := c.EnvVar(containerName)
		if err != nil {
			return core.PodSpec{}, err
		}
		containerVolumeMounts := make([]core.VolumeMount, 0)
		if _, exists := volumeMounts[containerName]; exists {
			containerVolumeMounts = append(containerVolumeMounts, volumeMounts[containerName]...)
		}
		if _, exists := fileVolumeMounts[containerName]; exists {
			containerVolumeMounts = append(containerVolumeMounts, fileVolumeMounts[containerName]...)
		}
		if len(containerVolumeMounts) == 0 {
			// Stay as true as possible to the defaults, nil rather than empty slice
			containerVolumeMounts = nil
		} else {
			slices.SortFunc(containerVolumeMounts, func(a, b core.VolumeMount) int {
				return strings.Compare(a.Name, b.Name)
			})
		}
		resReq, err := K8sResourceRequirementsFromScore(scoreContainer.Resources)
		if err != nil {
			return core.PodSpec{}, fmt.Errorf("%s.containers.%s: %w", c.Name, containerName, err)
		}
		livenessProbe, err := K8sProbeFromScore(scoreContainer.LivenessProbe)
		if err != nil {
			return core.PodSpec{}, fmt.Errorf("%s.containers.%s.livenessProbe: %w", c.Name, containerName, err)
		}
		readinessProbe, err := K8sProbeFromScore(scoreContainer.ReadinessProbe)
		if err != nil {
			return core.PodSpec{}, fmt.Errorf("%s.containers.%s.readinessProbe: %w", c.Name, containerName, err)
		}

		containers = append(containers, core.Container{
			Name:           containerName,
			Image:          scoreContainer.Image,
			Command:        scoreContainer.Command,
			Args:           scoreContainer.Args,
			WorkingDir:     "",
			Env:            envVars,
			Resources:      resReq,
			VolumeMounts:   containerVolumeMounts,
			LivenessProbe:  livenessProbe,
			ReadinessProbe: readinessProbe,
		})
	}
	slices.SortFunc(containers, func(a, b core.Container) int {
		return strings.Compare(a.Name, b.Name)
	})
	return core.PodSpec{
		Volumes: volumes,
		//InitContainers:                ,
		Containers: containers,
		//EphemeralContainers:           []core.EphemeralContainer{},
		//RestartPolicy:                 "",
		//TerminationGracePeriodSeconds: new(int64),
		//ActiveDeadlineSeconds:         new(int64),
		//DNSPolicy:                     "",
		//NodeSelector:                  map[string]string{},
		//ServiceAccountName:            "",
		//DeprecatedServiceAccount:      "",
		//AutomountServiceAccountToken:  new(bool),
		//NodeName:                      "",
		//HostNetwork:                   false,
		//HostPID:                       false,
		//HostIPC:                       false,
		//ShareProcessNamespace:         new(bool),
		//SecurityContext:               &core.PodSecurityContext{},
		//ImagePullSecrets:              []core.LocalObjectReference{},
		//Hostname:                      "",
		//Subdomain:                     "",
		//Affinity:                      &core.Affinity{},
		//SchedulerName:                 "",
		//Tolerations:                   []core.Toleration{},
		//HostAliases:                   []core.HostAlias{},
		//PriorityClassName:             "",
		//Priority:                      new(int32),
		//DNSConfig:                     &core.PodDNSConfig{},
		//ReadinessGates:                []core.PodReadinessGate{},
		//RuntimeClassName:              new(string),
		//EnableServiceLinks:            new(bool),
		//PreemptionPolicy:              &"",
		//Overhead:                      core.ResourceList{},
		//TopologySpreadConstraints:     []core.TopologySpreadConstraint{},
		//SetHostnameAsFQDN:             new(bool),
		//OS:                            &core.PodOS{},
		//HostUsers:                     new(bool),
		//SchedulingGates:               []core.PodSchedulingGate{},
		//ResourceClaims:                []core.PodResourceClaim{},
		//Resources:                     &core.ResourceRequirements{},

	}, err
}

func K8sResourceRequirementsFromScore(resources *types.ContainerResources) (core.ResourceRequirements, error) {
	var resourceReqs core.ResourceRequirements
	if resources != nil {
		var err error
		if resources.Requests != nil {
			resourceReqs.Requests, err = resourceLimitsToResourceList(resources.Requests)
			if err != nil {
				return core.ResourceRequirements{}, fmt.Errorf("converting resources.requests%w", err)
			}
		}
		if resources.Limits != nil {
			resourceReqs.Limits, err = resourceLimitsToResourceList(resources.Limits)
			if err != nil {
				return core.ResourceRequirements{}, fmt.Errorf("converting resources.requests%w", err)
			}
		}
	}
	return resourceReqs, nil
}

func resourceLimitsToResourceList(input *types.ResourcesLimits) (core.ResourceList, error) {
	var err error
	output := core.ResourceList{}
	if input.Cpu != nil {
		output["cpu"], err = k8s.ParseQuantity(*input.Cpu)
		if err != nil {
			return nil, fmt.Errorf(".cpu: %s", err)
		}
	}
	if input.Memory != nil {
		output["memory"], err = k8s.ParseQuantity(*input.Memory)
		if err != nil {
			return nil, fmt.Errorf(".memory: %s", err)
		}
	}
	return output, nil
}

func K8sProbeFromScore(scoreProbe *types.ContainerProbe) (*core.Probe, error) {
	if scoreProbe == nil {
		return nil, nil
	}
	probe := core.Probe{}
	if httpGet := scoreProbe.HttpGet; httpGet != nil {
		probeHandler := core.ProbeHandler{
			HTTPGet: &core.HTTPGetAction{
				Path: httpGet.Path,
				Port: intstr.FromInt32(int32(httpGet.Port)),
			},
		}
		if httpGet.Host != nil {
			probeHandler.HTTPGet.Host = *httpGet.Host
		}
		if httpGet.Scheme != nil {
			probeHandler.HTTPGet.Scheme = core.URIScheme(*httpGet.Scheme)
		}

		if len(httpGet.HttpHeaders) > 0 {
			headers := make([]core.HTTPHeader, 0, len(httpGet.HttpHeaders))
			for _, header := range httpGet.HttpHeaders {
				headers = append(headers, core.HTTPHeader{Name: header.Name, Value: header.Value})
			}
			probeHandler.HTTPGet.HTTPHeaders = headers
		}
		probe.ProbeHandler = probeHandler

	} else if scoreProbe.Exec != nil {
		probe.ProbeHandler = core.ProbeHandler{
			Exec: &core.ExecAction{
				Command: scoreProbe.Exec.Command,
			},
		}
	} else {
		return &core.Probe{}, fmt.Errorf("only \"httpGet\" and \"exec\" probes supported")
	}
	return &probe, nil
}
