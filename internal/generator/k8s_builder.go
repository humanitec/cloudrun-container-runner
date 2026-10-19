package generator

import (
	"cmp"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/score-spec/score-go/types"
	corev1 "k8s.io/api/core/v1"
	k8s "k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/humanitec/cloudrun-container-runner/internal/google/secretmanager"
	"github.com/humanitec/cloudrun-container-runner/internal/score"
	"github.com/humanitec/cloudrun-container-runner/internal/utils"
)

const volumeResourceType = "volume"

// K8sBuilder builds Kubernetes objects from a Score workload.
type K8sBuilder struct {
	workload      types.Workload
	substitutions map[string]score.SubValue
	secrets       SecretSaver
	serviceName   string
}

// NewK8sBuilder returns a builder for the workload. serviceName names the secrets and file volumes.
func NewK8sBuilder(workload types.Workload, substitutions map[string]score.SubValue, secrets SecretSaver, serviceName string) *K8sBuilder {
	return &K8sBuilder{
		workload:      workload,
		substitutions: substitutions,
		secrets:       secrets,
		serviceName:   serviceName,
	}
}

func (b *K8sBuilder) EnvVar(ctx context.Context, containerName string) ([]corev1.EnvVar, error) {
	envVars := make([]corev1.EnvVar, 0)
	for name, value := range b.workload.Containers[containerName].Variables {
		placeholders := GetAllPlaceholdersInString(value)

		placeholderStrs := map[string]string{}
		isSecret := false
		for _, placeholder := range placeholders {
			output, err := score.ResolvePlaceholder(placeholder, containerName, b.workload, b.substitutions)
			if err != nil {
				return nil, err
			}
			if output.Secret != nil {
				if output.Secret.Value == nil {
					// For now, we don't support secret references. Secrets are resolved in Operator and passed to the driver as a flat value.
					// In the future we may need to support secret references, that reference Google Secret Manager secrets.
					return nil, fmt.Errorf("secret with name %s: secret references are not supported: require secret value to be provided", name)
				}
				isSecret = true
				str, err := utils.DecodeToString(output.Secret.Value)
				if err != nil {
					return nil, fmt.Errorf("resolving placeholder ${%s}: %w", placeholder, err)
				}
				placeholderStrs[placeholder] = str
			} else if output.Value != nil {
				str, err := utils.DecodeToString(output.Value)
				if err != nil {
					return nil, fmt.Errorf("resolving placeholder ${%s}: %w", placeholder, err)
				}
				placeholderStrs[placeholder] = str
			}
		}
		replacedVal, err := ReplaceAllPlaceholdersInString(value, placeholderStrs)
		if err != nil {
			return nil, fmt.Errorf("resolving variable %s in container %s: %w", name, containerName, err)
		}
		if isSecret {
			// Save the secret to GSM and add the env var reference
			secretName := secretmanager.SecretName(b.serviceName, containerName, "env", name)
			secretVersion, err := b.secrets.SaveSecret(ctx, b.serviceName, secretName, replacedVal)
			if err != nil {
				return nil, fmt.Errorf("resolving variable %s in container %s: saving secret to google secret manager: %w", name, containerName, err)
			}
			envVars = append(envVars, corev1.EnvVar{
				Name: name,
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						Key: secretVersion,
						LocalObjectReference: corev1.LocalObjectReference{
							Name: secretName,
						},
					},
				},
			})
		} else {
			envVars = append(envVars, corev1.EnvVar{
				Name:  name,
				Value: replacedVal,
			})
		}
	}
	slices.SortFunc(envVars, func(a, b corev1.EnvVar) int {
		return strings.Compare(a.Name, b.Name)
	})
	return envVars, nil
}

func (b *K8sBuilder) Volumes() (map[string][]corev1.VolumeMount, []corev1.Volume, error) {
	volumes := map[string]corev1.Volume{}
	volumeMounts := map[string][]corev1.VolumeMount{}

	for containerName, container := range b.workload.Containers {
		if len(container.Volumes) > 0 {
			volumeMounts[containerName] = []corev1.VolumeMount{}
		}
		for mountPath, volume := range container.Volumes {
			placeholders := GetAllPlaceholdersInString(volume.Source)
			if len(placeholders) != 1 || !strings.HasPrefix(volume.Source, "${") || !strings.HasSuffix(volume.Source, "}") {
				return nil, nil, fmt.Errorf("resolving volume containers.%s.volumes.%s: source must be a placeholder referencing a volume, got \"%s\"", containerName, mountPath, volume.Source)
			}
			parts := strings.Split(placeholders[0], ".")
			if len(parts) != 2 || parts[0] != "resources" {
				return nil, nil, fmt.Errorf("resolving volume containers.%s.volumes.%s: source must be a placeholder referencing a volume, i.e. of the form ${resources.NAME} got ${%s}", containerName, mountPath, placeholders[0])
			}
			if volumeRes, exists := b.workload.Resources[parts[1]]; exists {
				if volumeRes.Type != volumeResourceType {
					return nil, nil, fmt.Errorf("resolving volume containers.%s.volumes.%s: placeholder ${%s} in source is not of type volume, got %s", containerName, mountPath, placeholders[0], volumeRes.Type)
				}
			} else {
				return nil, nil, fmt.Errorf("resolving volume containers.%s.volumes.%s: placeholder ${%s} cannot be resolved: no resource with name %s", containerName, mountPath, placeholders[0], parts[1])
			}

			volName := strings.ReplaceAll(placeholders[0], ".", "-")

			if _, exists := volumes[volName]; !exists {
				output, err := score.ResolvePlaceholder(placeholders[0], containerName, b.workload, b.substitutions)
				if err != nil {
					return nil, nil, fmt.Errorf("resolving volume resource %s: %w", placeholders[0], err)
				}
				if outputAsMap, ok := output.Value.(map[string]any); !ok {
					return nil, nil, fmt.Errorf("resolving volume resource %s: invalid output for platform %s, expected object, got %T", placeholders[0], Platform, output.Value)
				} else if volumeAsMap, ok := outputAsMap[Platform].(map[string]any); !ok {
					return nil, nil, fmt.Errorf("resolving volume resource %s: platform %s does not exist in output or is not an object", placeholders[0], Platform)
				} else {
					var k8sVolume corev1.Volume
					if err := utils.DecodeViaJSON(volumeAsMap, &k8sVolume); err != nil {
						return nil, nil, fmt.Errorf("resolving volume resource %s: unable to parse output for platform %s as k8s volume: %w", placeholders[0], Platform, err)
					}
					k8sVolume.Name = volName
					volumes[volName] = k8sVolume
				}
			}
			subPath := ""
			if volume.Path != nil {
				subPath = *volume.Path
			}

			readOnly := false
			var recursiveReadOnlyMode *corev1.RecursiveReadOnlyMode
			if volume.ReadOnly != nil {
				readOnly = *volume.ReadOnly
				r := corev1.RecursiveReadOnlyIfPossible
				recursiveReadOnlyMode = &r
			}
			volumeMounts[containerName] = append(volumeMounts[containerName], corev1.VolumeMount{
				Name:              volName,
				ReadOnly:          readOnly,
				RecursiveReadOnly: recursiveReadOnlyMode,
				MountPath:         mountPath,
				SubPath:           subPath,
				MountPropagation:  nil, // Default ti "None"
				SubPathExpr:       "",  // Mutually exclusive to SubPath
			})
		}
	}
	volumesAsSlice := make([]corev1.Volume, 0, len(volumes))
	for _, volume := range volumes {
		volumesAsSlice = append(volumesAsSlice, volume)
	}
	return volumeMounts, volumesAsSlice, nil
}

func (b *K8sBuilder) ContainerFiles(ctx context.Context) (map[string][]corev1.VolumeMount, []corev1.Volume, error) {
	volumes := make([]corev1.Volume, 0)
	volumeMounts := map[string][]corev1.VolumeMount{}

	for containerName, container := range b.workload.Containers {
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

		volumeMounts[containerName] = []corev1.VolumeMount{}

		for dir, files := range dirs {
			if !path.IsAbs(dir) {
				return nil, nil, fmt.Errorf("processing containers.%s.files: \"%s\" is not an absolute path", containerName, dir)
			}
			// guranateed to be 63 characters or less to comply with DNS Label requirement
			volumeName := utils.ToSlug(fmt.Sprintf("%s-%s",
				strLastN(strings.ReplaceAll(dir, "/", "-")[1:], 32),
				strFirstN(hashStr(fmt.Sprintf("%s-%s-%s", b.serviceName, containerName, dir)), 30),
			))

			if len(files) > 1 {
				// See https://docs.cloud.google.com/run/docs/configuring/services/secrets#limitations
				return nil, nil, fmt.Errorf("more then one file specified in directory %s: cloudrun only supports mounting 1 file per directory from google secret manager(gsm), got %d (See: https://docs.cloud.google.com/run/docs/configuring/services/secrets#limitations)", dir, len(files))
			}
			var fileName string
			for fn := range files {
				fileName = fn
			}
			content, err := b.ExpandFile(*files[fileName], containerName)
			if err != nil {
				return nil, nil, err
			}
			if content.Secret == nil {
				return nil, nil, fmt.Errorf("file %s/%s: cloudrun only supports mounting files from google secret manager secrets (gsm)", dir, fileName)
			}
			if content.Secret.Value == nil {
				// For now, we don't support secret references. Secrets are resolved in Operator and passed to the driver as a flat value.
				// In the future we may need to support secret references, that reference Google Secret Manager secrets.
				return nil, nil, fmt.Errorf("file %s/%s: secret references are not supported: require secret value to be provided", dir, fileName)
			}
			fileMode, err := fileModeFromString(files[fileName].Mode)
			if err != nil {
				return nil, nil, fmt.Errorf("file mode for file %s/%s is invalid: %w", dir, fileName, err)
			}
			// Save the secret to GSM and createVolume spec
			str, err := utils.DecodeToString(content.Secret.Value)
			if err != nil {
				return nil, nil, fmt.Errorf("file %s/%s: %w", dir, fileName, err)
			}
			secretName := secretmanager.SecretName(b.serviceName, containerName, "vol", fileName)
			secretVersion, err := b.secrets.SaveSecret(ctx, b.serviceName, secretName, str)
			if err != nil {
				return nil, nil, fmt.Errorf("file %s/%s: saving secret to google secret manager: %w", dir, fileName, err)
			}
			volume := corev1.Volume{
				VolumeSource: corev1.VolumeSource{
					Secret: &corev1.SecretVolumeSource{
						SecretName: secretName,
						Items: []corev1.KeyToPath{
							{
								Key:  secretVersion,
								Path: fileName,
								Mode: fileMode,
							},
						},
					},
				},
				Name: volumeName,
			}
			volumeMounts[containerName] = append(volumeMounts[containerName], corev1.VolumeMount{
				Name:      volume.Name,
				ReadOnly:  true,
				MountPath: dir,
			})
			volumes = append(volumes, volume)
		}

		// Avoid arbitrary changes in order due to map -> slice conversion
		slices.SortFunc(volumeMounts[containerName], func(a, b corev1.VolumeMount) int {
			return strings.Compare(a.Name, b.Name)
		})
	}
	slices.SortFunc(volumes, func(a, b corev1.Volume) int {
		return strings.Compare(a.Name, b.Name)
	})
	return volumeMounts, volumes, nil
}

func (b *K8sBuilder) Pod(ctx context.Context) (*corev1.Pod, error) {
	volumeMounts, volumes, err := b.Volumes()
	if err != nil {
		return nil, err
	}
	fileVolumeMounts, fileVolumes, err := b.ContainerFiles(ctx)
	if err != nil {
		return nil, err
	}
	volumes = append(volumes, fileVolumes...)
	if len(volumes) == 0 {
		// Stay as true as possible to the defaults, nil rather than empty slice
		volumes = nil
	} else {
		slices.SortFunc(volumes, func(a, b corev1.Volume) int {
			return strings.Compare(a.Name, b.Name)
		})
	}
	containers := make([]corev1.Container, 0)
	for containerName, scoreContainer := range b.workload.Containers {
		envVars, err := b.EnvVar(ctx, containerName)
		if err != nil {
			return nil, err
		}
		containerVolumeMounts := make([]corev1.VolumeMount, 0)
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
			// One volume can be mounted at several paths, so the name alone isn't a stable order.
			slices.SortFunc(containerVolumeMounts, func(a, b corev1.VolumeMount) int {
				return cmp.Or(strings.Compare(a.Name, b.Name), strings.Compare(a.MountPath, b.MountPath))
			})
		}
		resReq, err := k8sResourceRequirementsFromScore(scoreContainer.Resources)
		if err != nil {
			return nil, fmt.Errorf("containers.%s: %w", containerName, err)
		}
		livenessProbe, err := k8sProbeFromScore(scoreContainer.LivenessProbe)
		if err != nil {
			return nil, fmt.Errorf("containers.%s.livenessProbe: %w", containerName, err)
		}
		readinessProbe, err := k8sProbeFromScore(scoreContainer.ReadinessProbe)
		if err != nil {
			return nil, fmt.Errorf("containers.%s.readinessProbe: %w", containerName, err)
		}

		containers = append(containers, corev1.Container{
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
	slices.SortFunc(containers, func(a, b corev1.Container) int {
		return strings.Compare(a.Name, b.Name)
	})
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{},
		Spec: corev1.PodSpec{
			Volumes:    volumes,
			Containers: containers,
		},
	}, nil
}

// ExpandFile returns the SubValue that can be used as the value of
// the file
//
// Currnetly, secret templating is not supported except where raw secrets are
// used, so in those cases, unless the secret is on its own in the file, an
// error will be returned.
func (b *K8sBuilder) ExpandFile(file types.ContainerFile, containerName string) (*score.SubValue, error) {
	if file.Content == nil {
		return nil, fmt.Errorf("content missing")
	}
	if file.NoExpand != nil && *file.NoExpand {
		return &score.SubValue{Value: *file.Content}, nil
	}
	placeholders := GetAllPlaceholdersInString(*file.Content)
	placeholderStrs := map[string]string{}
	isOutputSecret := false
	for _, placeholder := range placeholders {
		output, err := score.ResolvePlaceholder(placeholder, containerName, b.workload, b.substitutions)
		if err != nil {
			return nil, err
		}
		if output.Secret != nil {
			if output.Secret.Value != nil {
				if placeholderStrs[placeholder], err = utils.DecodeToString(output.Secret.Value); err != nil {
					return nil, fmt.Errorf("resolving placeholder ${%s}: %w", placeholder, err)
				}
				isOutputSecret = true
			} else {
				// We don't yet support startings that contain more than one secret in files unless the secret value is direct
				if len(placeholders) == 1 && *file.Content == fmt.Sprintf("${%s}", placeholder) {
					return output, nil
				} else {
					return nil, fmt.Errorf("resolving placeholder ${%s}: secret of store type %s can only exist on its own in a file", placeholder, output.Secret.Store)
				}
			}
		} else if output.Value != nil {
			if placeholderStrs[placeholder], err = utils.DecodeToString(output.Value); err != nil {
				return nil, fmt.Errorf("resolving placeholder ${%s}: %w", placeholder, err)
			}
		}
	}
	expandedFile, err := ReplaceAllPlaceholdersInString(*file.Content, placeholderStrs)
	if err != nil {
		return nil, err
	}
	if isOutputSecret {
		return &score.SubValue{Secret: &score.SecretRef{Value: expandedFile, Type: score.DirectSecretType}}, nil
	}
	return &score.SubValue{Value: expandedFile}, nil
}

func k8sResourceRequirementsFromScore(resources *types.ContainerResources) (corev1.ResourceRequirements, error) {
	var resourceReqs corev1.ResourceRequirements
	if resources != nil {
		var err error
		if resources.Requests != nil {
			resourceReqs.Requests, err = k8sResourceListFromScore(resources.Requests)
			if err != nil {
				return corev1.ResourceRequirements{}, fmt.Errorf("converting resources.requests%w", err)
			}
		}
		if resources.Limits != nil {
			resourceReqs.Limits, err = k8sResourceListFromScore(resources.Limits)
			if err != nil {
				return corev1.ResourceRequirements{}, fmt.Errorf("converting resources.limits%w", err)
			}
		}
	}
	return resourceReqs, nil
}

func k8sProbeFromScore(scoreProbe *types.ContainerProbe) (*corev1.Probe, error) {
	if scoreProbe == nil {
		return nil, nil
	}
	probe := corev1.Probe{}
	if httpGet := scoreProbe.HttpGet; httpGet != nil {
		probeHandler := corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{
				Path: httpGet.Path,
				Port: intstr.FromInt32(int32(httpGet.Port)),
			},
		}
		if httpGet.Host != nil {
			probeHandler.HTTPGet.Host = *httpGet.Host
		}
		if httpGet.Scheme != nil {
			probeHandler.HTTPGet.Scheme = corev1.URIScheme(*httpGet.Scheme)
		}

		if len(httpGet.HttpHeaders) > 0 {
			headers := make([]corev1.HTTPHeader, 0, len(httpGet.HttpHeaders))
			for _, header := range httpGet.HttpHeaders {
				headers = append(headers, corev1.HTTPHeader{Name: header.Name, Value: header.Value})
			}
			probeHandler.HTTPGet.HTTPHeaders = headers
		}
		probe.ProbeHandler = probeHandler

	} else if scoreProbe.Exec != nil {
		probe.ProbeHandler = corev1.ProbeHandler{
			Exec: &corev1.ExecAction{
				Command: scoreProbe.Exec.Command,
			},
		}
	} else {
		return &corev1.Probe{}, fmt.Errorf("only \"httpGet\" and \"exec\" probes supported")
	}
	return &probe, nil
}

func k8sResourceListFromScore(input *types.ResourcesLimits) (corev1.ResourceList, error) {
	var err error
	output := corev1.ResourceList{}
	if input.Cpu != nil {
		output["cpu"], err = k8s.ParseQuantity(*input.Cpu)
		if err != nil {
			return nil, fmt.Errorf(".cpu: %w", err)
		}
	}
	if input.Memory != nil {
		output["memory"], err = k8s.ParseQuantity(*input.Memory)
		if err != nil {
			return nil, fmt.Errorf(".memory: %w", err)
		}
	}
	return output, nil
}

func fileModeFromString(mode *string) (*int32, error) {
	if mode == nil {
		return nil, nil
	}
	parsedMode, err := strconv.ParseInt(*mode, 8, 32)
	if err != nil {
		return nil, fmt.Errorf("expected octal number, got \"%s\": %w", *mode, err)
	}
	return new(int32(parsedMode)), nil
}

func strFirstN(s string, n int) string {
	l := len(s)
	if n > l {
		return s
	}
	return s[:n]
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
