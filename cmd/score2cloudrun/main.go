package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/score-spec/score-go/types"
	"sigs.k8s.io/yaml"

	"github.com/humanitec/cloudrun-container-runner/internal/google"
	"github.com/humanitec/cloudrun-container-runner/internal/google/cloudrun"
	"github.com/humanitec/cloudrun-container-runner/internal/google/secretmanager"
	"github.com/humanitec/cloudrun-container-runner/internal/inputs"
	scorecloudrun "github.com/humanitec/cloudrun-container-runner/internal/score/cloudrun"
)

var version = "dev"

const (
	// The Container Driver runs this binary as the runner container of a Kubernetes Job
	// and talks to it purely through the environment.

	// EnvAction specifies the Driver lifecycle action (create or destroy)
	EnvAction = "ACTION"

	// EnvResourceInputsFile specifies the resource inputs file.
	EnvResourceInputsFile = "RESOURCE_INPUTS_FILE"

	// EnvOutputsFile specifies the file where the non-secret outputs are written to.
	EnvOutputsFile = "OUTPUTS_FILE"

	// EnvSecretOutputsFile specifies the file where the secret outputs are written to. Not used, but reserved for hypothetical use.
	EnvSecretOutputsFile = "SECRET_OUTPUTS_FILE"

	// EnvErrorFile specifies the file where the error message is written to.
	EnvErrorFile = "ERROR_FILE"

	// EnvScriptsDirectory specifies the working directory.
	EnvScriptsDirectory = "SCRIPTS_DIRECTORY"

	// EnvRuntimeServiceAccount names the identity the deployed service runs as,
	// as opposed to the one this binary deploys with.
	EnvRuntimeServiceAccount = "CLOUDRUN_RUNTIME_SERVICE_ACCOUNT"

	// EnvServiceName is the name of the Cloud Run service.
	EnvServiceName = "CLOUDRUN_SERVICE_NAME"

	// EnvServiceNamePrefix is the prefix of the Cloud Run service name.
	EnvServiceNamePrefix = "CLOUDRUN_SERVICE_NAME_PREFIX"

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

// readResourceInputs loads the resource inputs (JSON) the Container Driver writes for the runner.
// See examples/resource-inputs.json. Unknown keys are ignored.
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
	// Empty workloads are not allowed, catch it right away.
	if len(in.Spec.Containers) == 0 {
		return ResourceInputs{}, fmt.Errorf("resource inputs %s: \"spec.containers\" is empty, a Cloud Run service needs at least one container", path)
	}

	return in, nil
}

func getServiceName(workloadName string) string {
	if name := os.Getenv(EnvServiceName); name != "" {
		return name
	}
	if prefix := os.Getenv(EnvServiceNamePrefix); prefix != "" {
		return prefix + "-" + workloadName
	}
	return workloadName
}

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
		return fmt.Errorf("-action %s needs -deploy: there is nothing to do for a destroy unless the deployment happens", ActionDestroy)
	}

	// The Driver expects the runner to work inside the shared directory (the output files, the error file, the credentials file).
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
		return destroy(ctx, target, getServiceName(in.Id))
	}
	return create(ctx, in, target, *outputPath, true)
}

// create converts the workload into a service manifest, writes it to
// outputPath, and when deploy is set applies it to Cloud Run.
func create(ctx context.Context, in ResourceInputs, target google.Target, outputPath string, deploy bool) error {
	serviceName := getServiceName(in.Id)
	secrets := secretmanager.New(target, serviceName)
	defer func() {
		if err := secrets.Close(); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "score2cloudrun: closing the secret manager client: %v\n", err)
		}
	}()

	manifest, err := scorecloudrun.FromScoreWorkload(ctx, scorecloudrun.Options{
		Name:           serviceName,
		Workload:       &in.Spec,
		Substitutions:  substitutionsToInputs(in.Substitutions),
		Extension:      in.Extensions[scorecloudrun.ExtensionName],
		ServiceAccount: os.Getenv(EnvRuntimeServiceAccount),
	}, secrets)
	if err != nil {
		return fmt.Errorf("converting the Score workload into a Cloud Run service: %w", err)
	}

	if b, err := yaml.Marshal(manifest); err != nil {
		return fmt.Errorf("serialising the service manifest: %w", err)
	} else if err = os.WriteFile(outputPath, b, 0o600); err != nil {
		return fmt.Errorf("writing the service manifest: %w", err)
	}
	if !deploy {
		return nil
	}

	result, err := cloudrun.New(target).Deploy(ctx, manifest)
	if err != nil {
		return err
	}

	if err := secrets.DeleteUnused(ctx); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "score2cloudrun: cleaning up the secrets the workload no longer references: %v\n", err)
	}

	if err := writeJSON(os.Getenv(EnvOutputsFile), result); err != nil {
		return fmt.Errorf("writing the deployment outputs: %w", err)
	}
	return nil
}

// destroy deletes the Cloud Run service the workload deployed to and deletes related secrets.
func destroy(ctx context.Context, target google.Target, service string) error {
	if err := cloudrun.New(target).Delete(ctx, service); err != nil {
		return err
	}

	secrets := secretmanager.New(target, service)
	defer func() {
		if err := secrets.Close(); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "score2cloudrun: closing the secret manager client: %v\n", err)
		}
	}()

	if err := secrets.DeleteUnused(ctx); err != nil {
		return fmt.Errorf("deleting the secrets of service %s: %w", service, err)
	}
	return nil
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

// reportError appends reason to ERROR_FILE.
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

func main() {
	if err := run(context.Background()); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "score2cloudrun: %v\n", err)
		reportError(err)
		os.Exit(1)
	}
}
