package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/score-spec/score-go/types"
	core "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	servingv1 "knative.dev/serving/pkg/apis/serving/v1"
	"sigs.k8s.io/yaml"

	"github.com/humanitec/cloudrun-container-runner/internal/google"
	"github.com/humanitec/cloudrun-container-runner/internal/google/secretmanager"
	"github.com/humanitec/cloudrun-container-runner/internal/inputs"
	"github.com/humanitec/cloudrun-container-runner/internal/score"
	"github.com/humanitec/cloudrun-container-runner/internal/utils"
)

var version = "dev"

const CloudRunExtensionName = "cloudrun"

type SecretRef struct {
	Store   string `json:"store,omitempty"`
	Ref     string `json:"ref,omitempty"`
	Version string `json:"version,omitempty"`
}

// Substitution defines with what a Score placeholder should be substituted.
type Substitution struct {
	Secret bool       `json:"secret"`
	Value  any        `json:"value,omitempty"`
	Ref    *SecretRef `json:"ref,omitempty"`
}

// ResourceInputs represents driver resource inputs. In this case it should contain Score specification,
// substitution map for placeholders replacement and Cloud Run specific extension.
type ResourceInputs struct {
	Id            string                    `json:"id"`
	Spec          types.Workload            `json:"spec"`
	Substitutions map[string]Substitution   `json:"substitutions,omitempty"`
	Extensions    map[string]map[string]any `json:"extensions,omitempty"`
}

type WorkloadOutput struct {
	Manifests []map[string]any
}

func substitutionsToInputs(subs map[string]Substitution) map[string]inputs.Input {
	out := make(map[string]inputs.Input, len(subs))
	for key, s := range subs {
		if s.Secret {
			var secret *inputs.SecretInput
			if s.Ref == nil {
				secret = &inputs.SecretInput{
					Value: s.Value,
				}
			} else {
				secret = &inputs.SecretInput{
					Store:   s.Ref.Store,
					Key:     s.Ref.Ref,
					Version: s.Ref.Version,
				}
			}
			out[key] = inputs.Input{Secret: secret}
			continue
		}
		out[key] = inputs.Input{Value: s.Value}
	}
	return out
}

func fileModeFromString(mode *string) (*int32, error) {
	if mode == nil {
		return nil, nil
	}
	parsedMode, err := strconv.ParseInt(*mode, 8, 32)
	if err != nil {
		return nil, fmt.Errorf("expected octal number, got \"%s\": %w", *mode, err)
	}
	return utils.ToPtr(int32(parsedMode)), nil
}

// applyExtensionToPod applies the extension to the Pod Object.
func applyExtensionToPod(pod core.Pod, extension map[string]any) (core.Pod, error) {
	// TODO: implement
	return pod, nil
}

// secretSaver stores a secret value under name and returns the version the
// generated manifest should reference. *secretmanager.Client implements it.
type secretSaver interface {
	SaveSecret(ctx context.Context, name, value string) (string, error)
}

// scoreWorkloadToCloudRunService converts a Score workload into a Cloud Run service manifest.
// Any secret the workload resolves is written to gsm secrets, and the manifest references its version.
func scoreWorkloadToCloudRunService(ctx context.Context, in ResourceInputs, secrets secretSaver) (WorkloadOutput, error) {
	workloadName := in.Id
	workload := in.Spec
	var converter score.K8sScoreConverter
	converter = score.K8sScoreConverter{
		WorkloadResource: score.WorkloadResource{
			Workload:      &workload,
			Substitutions: substitutionsToInputs(in.Substitutions),
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
	podSpec, err := converter.PodSpec("google-cloud-run")
	if err != nil {
		return WorkloadOutput{}, err
	}
	pod := core.Pod{
		ObjectMeta: metav1.ObjectMeta{},
		Spec:       podSpec,
	}
	if ext := in.Extensions[CloudRunExtensionName]; ext != nil {
		pod, err = applyExtensionToPod(pod, ext)
		if err != nil {
			return WorkloadOutput{}, err
		}
	}

	if workload.Service != nil {
		if len(workload.Service.Ports) > 1 {
			return WorkloadOutput{}, fmt.Errorf("cloudrun only supports a single port, got %d ports", len(workload.Service.Ports))
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
			return WorkloadOutput{}, fmt.Errorf("cloudrun only supports ingress on a single container: ambiguous container as there are %d containers", len(workload.Containers))
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
	serviceAsMap, err := utils.AsMap(service)
	if err != nil {
		return WorkloadOutput{}, fmt.Errorf("unable to marshal knative service manifest: %w", err)
	}
	// TODO: apply extensions to Service

	return WorkloadOutput{
		Manifests: []map[string]any{
			serviceAsMap,
		},
	}, nil

}

// readResourceInputs loads RESOURCE_INPUTS_FILE, the JSON the Container Driver
// writes for the runner, and the entrypoint passes to us as an argument. See
// examples/resource-inputs.json. Keys the runner does not recognise are
// ignored; the deployment target is not in here at all, it reaches gcloud
// through CLOUDSDK_CORE_PROJECT and CLOUDSDK_RUN_REGION.
func readResourceInputs(path string) (ResourceInputs, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return ResourceInputs{}, fmt.Errorf("reading resource inputs: %w", err)
	}

	var in ResourceInputs
	if err := json.Unmarshal(b, &in); err != nil {
		return ResourceInputs{}, fmt.Errorf("parsing resource inputs %s: %w", path, err)
	}

	if in.Id == "" {
		return ResourceInputs{}, fmt.Errorf("resource inputs %s: \"id\" is required, it names the Cloud Run service to deploy", path)
	}
	// scoreWorkloadToCloudRunService indexes the first container once the
	// workload exposes a service, so catch an empty workload here, where we can
	// say something useful about it.
	if len(in.Spec.Containers) == 0 {
		return ResourceInputs{}, fmt.Errorf("resource inputs %s: \"spec.containers\" is empty, a Cloud Run service needs at least one container", path)
	}

	return in, nil
}

func main() {
	if err := run(context.Background()); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "score2cloudrun: %v\n", err)
		os.Exit(1)
	}
}

// run converts the Score workload named on the command line into a Cloud Run
// service manifest on stdout.
//
// Nothing else may go to stdout: the entrypoint redirects it straight into
// service.yaml. Diagnostics belong on stderr, which lands in the Job log.
func run(ctx context.Context) error {
	printVersion := flag.Bool("version", false, "print the version and exit")
	flag.Usage = func() {
		out := flag.CommandLine.Output()
		_, _ = fmt.Fprintf(out, "Usage: %s [flags] RESOURCE_INPUTS_FILE\n\n", filepath.Base(os.Args[0]))
		_, _ = fmt.Fprint(out, "Converts the Score workload in RESOURCE_INPUTS_FILE into a Cloud Run\n"+
			"service manifest, written to stdout as YAML.\n\nFlags:\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	if *printVersion {
		fmt.Println(version)
		return nil
	}

	if flag.NArg() != 1 {
		flag.Usage()
		return fmt.Errorf("expected one argument, the resource inputs file, got %d", flag.NArg())
	}

	in, err := readResourceInputs(flag.Arg(0))
	if err != nil {
		return err
	}

	secrets := secretmanager.New(google.TargetFromEnv())
	defer func() {
		if err := secrets.Close(); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "score2cloudrun: closing the secret manager client: %v\n", err)
		}
	}()

	out, err := scoreWorkloadToCloudRunService(ctx, in, secrets)
	if err != nil {
		return fmt.Errorf("converting the Score workload into a Cloud Run service: %w", err)
	}

	// The entrypoint reads .metadata.name off the whole file and feeds it to
	// `gcloud run services replace`, so exactly one document has to come out.
	if len(out.Manifests) != 1 {
		return fmt.Errorf("expected exactly one manifest, got %d", len(out.Manifests))
	}

	manifest, err := yaml.Marshal(out.Manifests[0])
	if err != nil {
		return fmt.Errorf("serialising the service manifest: %w", err)
	}
	if _, err := os.Stdout.Write(manifest); err != nil {
		return fmt.Errorf("writing the service manifest: %w", err)
	}
	return nil
}
