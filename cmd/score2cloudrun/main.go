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
	"time"

	"github.com/score-spec/score-go/types"
	runv1 "google.golang.org/api/run/v1"
	core "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	servingv1 "knative.dev/serving/pkg/apis/serving/v1"
	"sigs.k8s.io/yaml"

	"github.com/humanitec/cloudrun-container-runner/internal/google"
	"github.com/humanitec/cloudrun-container-runner/internal/google/cloudrun"
	"github.com/humanitec/cloudrun-container-runner/internal/google/secretmanager"
	"github.com/humanitec/cloudrun-container-runner/internal/inputs"
	"github.com/humanitec/cloudrun-container-runner/internal/score"
	"github.com/humanitec/cloudrun-container-runner/internal/utils"
)

var version = "dev"

const (
	CloudRunExtensionName = "cloudrun"

	// The Container Driver runs this binary as the runner container of a Kubernetes Job
	// and talks to it purely through the environment.

	EnvAction             = "ACTION"
	EnvResourceInputsFile = "RESOURCE_INPUTS_FILE"
	EnvOutputsFile        = "OUTPUTS_FILE"
	EnvSecretOutputsFile  = "SECRET_OUTPUTS_FILE"
	EnvErrorFile          = "ERROR_FILE"
	EnvScriptsDirectory   = "SCRIPTS_DIRECTORY"

	// EnvRuntimeServiceAccount names the identity the deployed service runs as,
	// as opposed to the one this binary deploys with.
	EnvRuntimeServiceAccount = "CLOUDRUN_RUNTIME_SERVICE_ACCOUNT"

	// ActionCreate and ActionDestroy are the two halves of a resource's life,
	// and the only values the Driver puts in ACTION.
	ActionCreate  = "create"
	ActionDestroy = "destroy"

	// defaultOutputPath is where the manifest lands without -output.
	defaultOutputPath = "service.yaml"

	// defaultTimeout bounds the wait for Cloud Run to settle a deployment. A
	// service that has not come up by then is not going to.
	defaultTimeout = 10 * time.Minute
)

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
	Manifests []*runv1.Service
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
func scoreWorkloadToCloudRunService(ctx context.Context, in ResourceInputs, serviceAccount string, secrets secretSaver) (WorkloadOutput, error) {
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
	// The ServiceAccountName field is omitempty, so an empty account leaves the manifest exactly as it was.
	pod.Spec.ServiceAccountName = serviceAccount
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
	// The Knative types and the Admin API's own are the same schema from two
	// generators, so the translation is a JSON round trip. Doing it here means
	// what -output shows is what gets deployed, rather than a second rendering.
	var manifest runv1.Service
	if err := utils.DecodeViaJSON(service, &manifest); err != nil {
		return WorkloadOutput{}, fmt.Errorf("unable to marshal knative service manifest: %w", err)
	}
	// TODO: apply extensions to Service

	return WorkloadOutput{
		Manifests: []*runv1.Service{&manifest},
	}, nil

}

// readResourceInputs loads the JSON the Container Driver writes for the runner:
// the Score workload, the substitutions for its placeholders and the Cloud Run
// extension. See examples/resource-inputs.json. Keys the runner does not
// recognise are ignored; the deployment target is not in here at all, it
// arrives through CLOUDSDK_CORE_PROJECT and CLOUDSDK_RUN_REGION.
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
		reportError(err)
		os.Exit(1)
	}
}

// run converts the Score workload named on the command line into a Cloud Run
// service manifest, written as YAML to the file named by -output. With -deploy
// it applies that manifest to Cloud Run, or deletes the service it describes.
func run(ctx context.Context) error {
	printVersion := flag.Bool("version", false, "print the version and exit")
	outputPath := flag.String("output", defaultOutputPath, "file to write the service manifest to")
	deploy := flag.Bool("deploy", false, "apply the manifest to Cloud Run rather than only writing it")
	action := flag.String("action", envOr(EnvAction, ActionCreate), "with -deploy, either \"create\" or \"destroy\"")
	timeout := flag.Duration("timeout", defaultTimeout, "how long to wait for Cloud Run to settle the deployment")
	flag.Usage = usage
	flag.Parse()

	if *printVersion {
		fmt.Println(version)
		return nil
	}

	inputsPath := os.Getenv(EnvResourceInputsFile)
	switch flag.NArg() {
	case 0:
		if inputsPath == "" {
			flag.Usage()
			return fmt.Errorf("no resource inputs file: pass one as an argument or set %s", EnvResourceInputsFile)
		}
	case 1:
		inputsPath = flag.Arg(0)
	default:
		flag.Usage()
		return fmt.Errorf("expected at most one argument, the resource inputs file, got %d", flag.NArg())
	}

	if *action != ActionCreate && *action != ActionDestroy {
		return fmt.Errorf("unsupported -action %q, expected %q or %q", *action, ActionCreate, ActionDestroy)
	}
	if *action == ActionDestroy && !*deploy {
		return fmt.Errorf("-action %s needs -deploy: there is nothing to destroy short of Cloud Run itself", ActionDestroy)
	}

	// The Driver expects the runner to work inside the shared directory, and
	// every relative path it hands over resolves against it: the credentials
	// file, the outputs files and the error file alike.
	if dir := os.Getenv(EnvScriptsDirectory); dir != "" {
		if err := os.Chdir(dir); err != nil {
			return fmt.Errorf("entering the working directory %s: %w", dir, err)
		}
	}

	in, err := readResourceInputs(inputsPath)
	if err != nil {
		return err
	}

	target := google.TargetFromEnv()
	if !*deploy {
		return create(ctx, in, target, *outputPath, false)
	}

	// Everything past here waits on Cloud Run, so it is bounded.
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()

	if *action == ActionDestroy {
		return destroy(ctx, target, in.Id)
	}
	return create(ctx, in, target, *outputPath, true)
}

// create converts the workload into a service manifest, writes it to
// outputPath, and when deploy is set applies it to Cloud Run.
func create(ctx context.Context, in ResourceInputs, target google.Target, outputPath string, deploy bool) error {
	secrets := secretmanager.New(target)
	defer func() {
		if err := secrets.Close(); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "score2cloudrun: closing the secret manager client: %v\n", err)
		}
	}()

	out, err := scoreWorkloadToCloudRunService(ctx, in, os.Getenv(EnvRuntimeServiceAccount), secrets)
	if err != nil {
		return fmt.Errorf("converting the Score workload into a Cloud Run service: %w", err)
	}
	// A Cloud Run deployment is one service, so anything else is a bug in the
	// converter rather than something to hand on to the API.
	if len(out.Manifests) != 1 {
		return fmt.Errorf("expected exactly one manifest, got %d", len(out.Manifests))
	}
	manifest := out.Manifests[0]

	if err := writeManifest(outputPath, manifest); err != nil {
		return err
	}
	if !deploy {
		return nil
	}

	result, err := cloudrun.New(target).Deploy(ctx, manifest)
	if err != nil {
		return err
	}
	return writeDriverOutputs(result)
}

// destroy deletes the Cloud Run service the workload deployed to.
//
// It deliberately skips the conversion, which would resolve the workload's
// secrets and write them to Secret Manager on the way to a manifest nobody is
// going to deploy. The service name is the resource id, the same thing the
// converter would have put in metadata.name.
func destroy(ctx context.Context, target google.Target, service string) error {
	if err := cloudrun.New(target).Delete(ctx, service); err != nil {
		return err
	}
	return writeDriverOutputs(map[string]any{})
}

// writeManifest serialises the service manifest as YAML.
func writeManifest(path string, manifest *runv1.Service) error {
	b, err := yaml.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("serialising the service manifest: %w", err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return fmt.Errorf("writing the service manifest: %w", err)
	}
	return nil
}

// writeDriverOutputs publishes the workload's outputs where the Container
// Driver reads them back. Nothing the runner produces is sensitive, so the
// secret outputs are always an empty object, but the Driver still wants a file.
func writeDriverOutputs(outputs any) error {
	if err := writeJSON(os.Getenv(EnvOutputsFile), outputs); err != nil {
		return fmt.Errorf("writing the deployment outputs: %w", err)
	}
	if err := writeJSON(os.Getenv(EnvSecretOutputsFile), map[string]any{}); err != nil {
		return fmt.Errorf("writing the secret deployment outputs: %w", err)
	}
	return nil
}

// writeJSON serialises v to path. An empty path means nothing asked for the
// file, which is the normal case when score2cloudrun runs outside a Job.
func writeJSON(path string, v any) error {
	if path == "" {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}

// reportError appends reason to ERROR_FILE, which the Orchestrator shows as the
// deployment error message. The Driver may not have named one, in which case
// the Job log is the only record and stderr has already carried it there.
func reportError(reason error) {
	path := os.Getenv(EnvErrorFile)
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "score2cloudrun: opening %s: %v\n", path, err)
		return
	}
	defer func() { _ = f.Close() }()
	if _, err := fmt.Fprintln(f, reason); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "score2cloudrun: writing %s: %v\n", path, err)
	}
}

// envOr returns the environment variable name, or fallback when it is unset.
func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func usage() {
	out := flag.CommandLine.Output()
	_, _ = fmt.Fprintf(out, "Usage: %s [flags] [RESOURCE_INPUTS_FILE]\n\n", filepath.Base(os.Args[0]))
	_, _ = fmt.Fprint(out, "Converts the Score workload in RESOURCE_INPUTS_FILE into a Cloud Run\n"+
		"service manifest, written as YAML to the file named by -output. With\n"+
		"-deploy the manifest is applied to Cloud Run as well.\n\n"+
		"RESOURCE_INPUTS_FILE defaults to $"+EnvResourceInputsFile+".\n\nFlags:\n")
	flag.PrintDefaults()
	_, _ = fmt.Fprint(out, "\nEnvironment:\n"+
		"  "+google.EnvProject+"\n\tthe Google Cloud project to deploy into\n"+
		"  "+google.EnvRegion+"\n\tthe Cloud Run region to deploy into\n"+
		"  "+EnvRuntimeServiceAccount+"\n\tthe identity the deployed service runs as\n"+
		"  GOOGLE_APPLICATION_CREDENTIALS\n\ta service account key or external account configuration;\n"+
		"\twithout one the credentials come from the metadata server\n"+
		"  "+EnvScriptsDirectory+"\n\tworking directory to enter first, which every other\n"+
		"\trelative path resolves against\n"+
		"  "+EnvAction+", "+EnvOutputsFile+", "+EnvSecretOutputsFile+", "+EnvErrorFile+"\n"+
		"\tthe Container Driver's side of the contract\n")
}
