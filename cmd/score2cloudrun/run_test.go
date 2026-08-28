package main

import (
	"flag"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

// runWith calls run with args, on a flag set of its own so that repeated calls
// in one process do not trip over the flags a previous one registered.
//
// It starts in a directory of the test's own, both because run writes files
// relative to the working directory and because run may change it.
func runWith(t *testing.T, args ...string) error {
	t.Helper()

	t.Chdir(t.TempDir())

	saved := os.Args
	t.Cleanup(func() { os.Args = saved })

	os.Args = append([]string{"score2cloudrun"}, args...)
	flag.CommandLine = flag.NewFlagSet(os.Args[0], flag.ContinueOnError)
	flag.CommandLine.SetOutput(io.Discard)

	return run(t.Context())
}

// inputsFile writes helloWorldInputs somewhere run can read it.
func inputsFile(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "inputs.json")
	require.NoError(t, os.WriteFile(path, []byte(helloWorldInputs), 0o600))
	return path
}

// Converting is what the binary does on its own. Deploying is the image's
// doing, through the -deploy in its entrypoint, so a bare run must not reach
// for Google credentials.
func TestRunWritesTheManifestAndStops(t *testing.T) {
	inputs := inputsFile(t)
	output := filepath.Join(t.TempDir(), "service.yaml")

	require.NoError(t, runWith(t, "-output", output, inputs))

	var service map[string]any
	b, err := os.ReadFile(output)
	require.NoError(t, err)
	require.NoError(t, yaml.Unmarshal(b, &service))

	assert.Equal(t, "serving.knative.dev/v1", service["apiVersion"])
	assert.Equal(t, "hello-world-dev", service["metadata"].(map[string]any)["name"])
}

// The Container Driver passes the inputs file through the environment rather
// than on a command line it does not control.
func TestRunTakesTheInputsFileFromTheEnvironment(t *testing.T) {
	inputs := inputsFile(t)
	output := filepath.Join(t.TempDir(), "service.yaml")
	t.Setenv(EnvResourceInputsFile, inputs)

	require.NoError(t, runWith(t, "-output", output))

	assert.FileExists(t, output)
}

// Every relative path the Driver hands over resolves against the shared
// directory, so run has to be inside it before it writes anything.
func TestRunWritesInsideTheSharedDirectory(t *testing.T) {
	inputs := inputsFile(t)
	shared := t.TempDir()
	t.Setenv(EnvScriptsDirectory, shared)

	require.NoError(t, runWith(t, "-output", "service.yaml", inputs))

	assert.FileExists(t, filepath.Join(shared, "service.yaml"))
}

func TestRunRejectsAnUnknownAction(t *testing.T) {
	err := runWith(t, "-deploy", "-action", "update", inputsFile(t))

	require.Error(t, err)
	assert.Contains(t, err.Error(), `unsupported -action "update"`)
}

// Destroying without -deploy would quietly convert instead, which is the
// opposite of what was asked for.
func TestRunRefusesToDestroyWithoutDeploy(t *testing.T) {
	err := runWith(t, "-action", ActionDestroy, inputsFile(t))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "needs -deploy")
}

func TestRunNeedsAnInputsFile(t *testing.T) {
	t.Setenv(EnvResourceInputsFile, "")

	err := runWith(t)

	require.Error(t, err)
	assert.Contains(t, err.Error(), EnvResourceInputsFile)
}

func TestWriteDriverOutputsPublishesBothFiles(t *testing.T) {
	dir := t.TempDir()
	outputs := filepath.Join(dir, "outputs.json")
	secretOutputs := filepath.Join(dir, "secret-outputs.json")
	t.Setenv(EnvOutputsFile, outputs)
	t.Setenv(EnvSecretOutputsFile, secretOutputs)

	require.NoError(t, writeDriverOutputs(map[string]any{"url": "https://example.run.app"}))

	assert.JSONEq(t, `{"url": "https://example.run.app"}`, read(t, outputs))
	// Nothing the runner produces is sensitive, but the Driver still wants the file.
	assert.JSONEq(t, `{}`, read(t, secretOutputs))
}

// Run by hand there is no Driver to write outputs for, and no files to write.
func TestWriteDriverOutputsSkipsFilesNobodyAskedFor(t *testing.T) {
	t.Setenv(EnvOutputsFile, "")
	t.Setenv(EnvSecretOutputsFile, "")

	assert.NoError(t, writeDriverOutputs(map[string]any{}))
}

// The Orchestrator shows ERROR_FILE as the deployment error message, so
// whatever went wrong has to reach it as well as the Job log.
func TestReportErrorAppendsToTheErrorFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "error.txt")
	t.Setenv(EnvErrorFile, path)

	reportError(assert.AnError)

	assert.Contains(t, read(t, path), assert.AnError.Error())
}

func TestReportErrorIsQuietWithoutAnErrorFile(t *testing.T) {
	t.Setenv(EnvErrorFile, "")

	assert.NotPanics(t, func() { reportError(assert.AnError) })
}

// The Driver asks for create or destroy through ACTION, which -action only
// overrides.
func TestActionComesFromTheEnvironment(t *testing.T) {
	t.Setenv(EnvAction, ActionDestroy)

	err := runWith(t, inputsFile(t))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "needs -deploy")
}

func TestActionDefaultsToCreate(t *testing.T) {
	t.Setenv(EnvAction, "")
	output := filepath.Join(t.TempDir(), "service.yaml")

	require.NoError(t, runWith(t, "-output", output, inputsFile(t)))
	assert.FileExists(t, output)
}

func read(t *testing.T, path string) string {
	t.Helper()

	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(b)
}
