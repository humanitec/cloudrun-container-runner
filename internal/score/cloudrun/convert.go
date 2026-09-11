// Package cloudrun converts a Score workload into a Cloud Run service
// manifest.
//
// It is the Cloud Run adapter over the generic converter in internal/score:
// that one turns a Score workload into a Kubernetes PodSpec and leaves the
// environment variables, secrets and file mounts to its caller, which is what
// this package fills in. Secrets go to Google Secret Manager, because that is
// the only store Cloud Run mounts.
package cloudrun

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/score-spec/score-go/types"
	runv1 "google.golang.org/api/run/v1"
	core "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	servingv1 "knative.dev/serving/pkg/apis/serving/v1"

	"github.com/humanitec/cloudrun-container-runner/internal/google/secretmanager"
	"github.com/humanitec/cloudrun-container-runner/internal/inputs"
	"github.com/humanitec/cloudrun-container-runner/internal/score"
	"github.com/humanitec/cloudrun-container-runner/internal/utils"
)

// ExtensionName is the key the Cloud Run extension travels under.
const ExtensionName = "cloudrun"

// SecretSaver stores a secret value under name and returns the version the
// generated manifest should reference. *secretmanager.Client implements it.
type SecretSaver interface {
	SaveSecret(ctx context.Context, name, value string) (string, error)
}

// Options is the workload to convert. It is deliberately not the Container
// Driver's resource inputs: what the Driver sends is its own contract, and
// the conversion only needs the workload out of it.
type Options struct {
	// Name is what the service is called in Cloud Run.
	Name     string
	Workload *types.Workload

	// Substitutions is what the workload's ${...} placeholders resolve to.
	Substitutions map[string]inputs.Input

	// Extension is the Cloud Run extension, under ExtensionName.
	Extension map[string]any

	// ServiceAccount is the identity the deployed service runs as. Empty
	// leaves it to Cloud Run, which uses the project's default.
	ServiceAccount string
}

// FromScoreWorkload converts a Score workload into a Cloud Run service manifest.
// Any secret the workload resolves is written to gsm secrets, and the manifest references its version.
func FromScoreWorkload(ctx context.Context, opts Options, secrets SecretSaver) (*runv1.Service, error) {
	workloadName := opts.Name
	workload := opts.Workload
	if workload == nil || len(workload.Containers) == 0 {
		return nil, fmt.Errorf("a Cloud Run service needs at least one container")
	}
	var converter score.K8sScoreConverter
	converter = score.K8sScoreConverter{
		WorkloadResource: score.WorkloadResource{
			Workload:      workload,
			Substitutions: opts.Substitutions,
			Name:          workloadName,
		},
		EnvVarOverride: func(containerName string) ([]core.EnvVar, error) {
			envVars := make([]core.EnvVar, 0)
			for name, value := range workload.Containers[containerName].Variables {
				placeholders := score.GetAllPlaceholdersInString(value)

				placeholderStrs := map[string]string{}
				isSecret := false
				for _, placeholder := range placeholders {
					output, err := converter.OutputForPlaceholder(placeholder, containerName)
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
						str, err := anyToString(output.Secret.Value)
						if err != nil {
							return nil, fmt.Errorf("resolving placeholder ${%s}: %w", placeholder, err)
						}
						placeholderStrs[placeholder] = str
					} else if output.Value != nil {
						str, err := anyToString(output.Value)
						if err != nil {
							return nil, fmt.Errorf("resolving placeholder ${%s}: %w", placeholder, err)
						}
						placeholderStrs[placeholder] = str
					}
				}
				replacedVal, err := score.ReplaceAllPlaceholdersInString(value, placeholderStrs)
				if err != nil {
					return nil, fmt.Errorf("resolving variable %s in container %s: %w", name, containerName, err)
				}
				if isSecret {
					// Save the secret to GSM and add the env var reference
					secretName := secretmanager.SecretName(workloadName, containerName, "env", name)
					secretVersion, err := secrets.SaveSecret(ctx, secretName, replacedVal)
					if err != nil {
						return nil, fmt.Errorf("resolving variable %s in container %s: saving secret to google secret manager: %w", name, containerName, err)
					}
					envVars = append(envVars, core.EnvVar{
						Name: name,
						ValueFrom: &core.EnvVarSource{
							SecretKeyRef: &core.SecretKeySelector{
								Key: secretVersion,
								LocalObjectReference: core.LocalObjectReference{
									Name: secretName,
								},
							},
						},
					})
				} else {
					envVars = append(envVars, core.EnvVar{
						Name:  name,
						Value: replacedVal,
					})
				}
			}
			slices.SortFunc(envVars, func(a, b core.EnvVar) int {
				return strings.Compare(a.Name, b.Name)
			})
			return envVars, nil
		},
		EnvVarSecretResolver: func(name string, secret *inputs.SecretInput) (core.EnvVarSource, error) {
			return core.EnvVarSource{}, fmt.Errorf("secret resolver is not implemented for Cloud Run")
		},
		ContainerFileResolver: func(workloadRes score.WorkloadResource, volumeName, dir string, files map[string]*types.ContainerFile, containerName string) (core.Volume, error) {
			if len(files) > 1 {
				// See https://docs.cloud.google.com/run/docs/configuring/services/secrets#limitations
				return core.Volume{}, fmt.Errorf("more then one file specified in directory %s: cloudrun only supports mounting 1 file per directory from google secret manager(gsm), got %d (See: https://docs.cloud.google.com/run/docs/configuring/services/secrets#limitations)", dir, len(files))
			}
			var fileName string
			for fn := range files {
				fileName = fn
			}
			content, err := workloadRes.ExpandFile(*files[fileName], containerName)
			if err != nil {
				return core.Volume{}, err
			}
			if content.Secret == nil {
				return core.Volume{}, fmt.Errorf("file %s/%s: cloudrun only supports mounting files from google secret manager secrets (gsm)", dir, fileName)
			}
			if content.Secret.Value == nil {
				// For now, we don't support secret references. Secrets are resolved in Operator and passed to the driver as a flat value.
				// In the future we may need to support secret references, that reference Google Secret Manager secrets.
				return core.Volume{}, fmt.Errorf("file %s/%s: secret references are not supported: require secret value to be provided", dir, fileName)
			}
			fileMode, err := fileModeFromString(files[fileName].Mode)
			if err != nil {
				return core.Volume{}, fmt.Errorf("file mode for file %s/%s is invalid: %w", dir, fileName, err)
			}
			// Save the secret to GSM and createVolume spec
			str, err := anyToString(content.Secret.Value)
			if err != nil {
				return core.Volume{}, fmt.Errorf("file %s/%s: %w", dir, fileName, err)
			}
			secretName := secretmanager.SecretName(workloadName, containerName, "vol", fileName)
			secretVersion, err := secrets.SaveSecret(ctx, secretName, str)
			if err != nil {
				return core.Volume{}, fmt.Errorf("file %s/%s: saving secret to google secret manager: %w", dir, fileName, err)
			}
			return core.Volume{
				VolumeSource: core.VolumeSource{
					Secret: &core.SecretVolumeSource{
						SecretName: secretName,
						Items: []core.KeyToPath{
							{
								Key:  secretVersion,
								Path: fileName,
								Mode: fileMode,
							},
						},
					},
				},
				Name: volumeName,
			}, nil
		},
	}
	podSpec, err := converter.PodSpec("google_cloud_run")
	if err != nil {
		return nil, err
	}
	pod := core.Pod{
		ObjectMeta: metav1.ObjectMeta{},
		Spec:       podSpec,
	}
	// The ServiceAccountName field is omitempty, so an empty account leaves the manifest exactly as it was.
	pod.Spec.ServiceAccountName = opts.ServiceAccount
	if opts.Extension != nil {
		pod, err = applyExtensionToPod(pod, opts.Extension)
		if err != nil {
			return nil, err
		}
	}

	if workload.Service != nil {
		if len(workload.Service.Ports) > 1 {
			return nil, fmt.Errorf("cloudrun only supports a single port, got %d ports", len(workload.Service.Ports))
		}
		containerPorts := make([]core.ContainerPort, 0)
		for portName, port := range workload.Service.Ports {
			portNum := int32(port.Port)
			if port.TargetPort != nil {
				portNum = int32(*port.TargetPort)
			}
			containerPort := core.ContainerPort{
				ContainerPort: portNum,
			}
			if portName == "http1" || portName == "h2c" {
				containerPort.Name = portName
			}
			containerPorts = append(containerPorts, containerPort)
		}
		if len(workload.Containers) > 1 {
			return nil, fmt.Errorf("cloudrun only supports ingress on a single container: ambiguous container as there are %d containers", len(workload.Containers))
		}
		container := pod.Spec.Containers[0]
		container.Ports = containerPorts
		pod.Spec.Containers[0] = container
	}

	service := servingv1.Service{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "serving.knative.dev/v1",
			Kind:       "Service",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: workloadName,
		},
		Spec: servingv1.ServiceSpec{
			ConfigurationSpec: servingv1.ConfigurationSpec{
				Template: servingv1.RevisionTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{},
					Spec: servingv1.RevisionSpec{
						PodSpec: pod.Spec,
					},
				},
			},
			//RouteSpec:         servingv1.RouteSpec{},
		},
	}

	// TODO: apply extensions to Service

	// The Knative types and the Cloud Run Admin API (V1) have the same schema, do a JSON round trip to convert.
	var manifest runv1.Service
	if err := utils.DecodeViaJSON(service, &manifest); err != nil {
		return nil, fmt.Errorf("unable to marshal knative service manifest: %w", err)
	}
	return &manifest, nil
}

// applyExtensionToPod applies the extension to the Pod Object.
func applyExtensionToPod(pod core.Pod, extension map[string]any) (core.Pod, error) {
	// TODO: implement
	return pod, nil
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

func anyToString(val any) (string, error) {
	if str, ok := val.(string); ok {
		return str, nil
	}
	b, err := json.Marshal(val)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
