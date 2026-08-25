package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	servingv1 "knative.dev/serving/pkg/apis/serving/v1"

	"github.com/humanitec/cloudrun-container-runner/internal/google/secretmanager"
	"github.com/humanitec/cloudrun-container-runner/internal/utils"
)

// fakeSecretSaver stands in for Google Secret Manager, recording what would be
// written and handing back a fixed version, so that conversion can be exercised
// without Google credentials.
type fakeSecretSaver struct {
	saved   map[string]string
	version string
}

func newFakeSecretSaver() *fakeSecretSaver {
	return &fakeSecretSaver{saved: map[string]string{}, version: "1"}
}

func (f *fakeSecretSaver) SaveSecret(_ context.Context, name, value string) (string, error) {
	f.saved[name] = value
	return f.version, nil
}

// convert runs the converter over inputsJSON and decodes the single manifest it
// produces.
func convert(t *testing.T, inputsJSON string, saver secretSaver) servingv1.Service {
	t.Helper()

	inputsPath := filepath.Join(t.TempDir(), "inputs.json")
	require.NoError(t, os.WriteFile(inputsPath, []byte(inputsJSON), 0o600))

	in, err := readResourceInputs(inputsPath)
	require.NoError(t, err)

	out, err := scoreWorkloadToCloudRunService(t.Context(), in, saver)
	require.NoError(t, err)
	require.Len(t, out.Manifests, 1)

	var service servingv1.Service
	require.NoError(t, utils.DecodeViaJSON(out.Manifests[0], &service))
	return service
}

func TestEnvVarSecretIsSavedAndReferencedByVersion(t *testing.T) {
	saver := newFakeSecretSaver()
	saver.version = "7"

	service := convert(t, `{
	  "id": "hello-world-dev",
	  "spec": {
	    "apiVersion": "score.dev/v1b1",
	    "metadata": {"name": "hello-world"},
	    "containers": {
	      "main": {
	        "image": "busybox:latest",
	        "variables": {"DB_URL": "postgres://${resources.db.password}@db"}
	      }
	    },
	    "resources": {"db": {"type": "postgres"}}
	  },
	  "substitutions": {"resources.db.password": {"secret": true, "value": "s3cr3t"}}
	}`, saver)

	wantName := secretmanager.SecretName("hello-world-dev", "main", "env", "DB_URL")

	require.Len(t, service.Spec.Template.Spec.Containers, 1)
	env := service.Spec.Template.Spec.Containers[0].Env
	require.Len(t, env, 1)
	assert.Equal(t, "DB_URL", env[0].Name)
	assert.Empty(t, env[0].Value, "the secret must not be inlined into the manifest")

	require.NotNil(t, env[0].ValueFrom)
	require.NotNil(t, env[0].ValueFrom.SecretKeyRef)
	assert.Equal(t, wantName, env[0].ValueFrom.SecretKeyRef.Name)
	assert.Equal(t, "7", env[0].ValueFrom.SecretKeyRef.Key, "the manifest pins the version that was just written")

	// The whole substituted string is stored, not the bare secret value.
	assert.Equal(t, map[string]string{wantName: "postgres://s3cr3t@db"}, saver.saved)
}

func TestFileSecretIsSavedAndReferencedByVersion(t *testing.T) {
	saver := newFakeSecretSaver()
	saver.version = "3"

	service := convert(t, `{
	  "id": "hello-world-dev",
	  "spec": {
	    "apiVersion": "score.dev/v1b1",
	    "metadata": {"name": "hello-world"},
	    "containers": {
	      "main": {
	        "image": "busybox:latest",
	        "files": {
	          "/etc/app/config.yaml": {"content": "${resources.db.password}", "mode": "0400"}
	        }
	      }
	    },
	    "resources": {"db": {"type": "postgres"}}
	  },
	  "substitutions": {"resources.db.password": {"secret": true, "value": "s3cr3t"}}
	}`, saver)

	wantName := secretmanager.SecretName("hello-world-dev", "main", "vol", "config.yaml")

	volumes := service.Spec.Template.Spec.Volumes
	require.Len(t, volumes, 1)
	require.NotNil(t, volumes[0].Secret)
	assert.Equal(t, wantName, volumes[0].Secret.SecretName)

	require.Len(t, volumes[0].Secret.Items, 1)
	assert.Equal(t, "3", volumes[0].Secret.Items[0].Key, "the manifest pins the version that was just written")
	assert.Equal(t, "config.yaml", volumes[0].Secret.Items[0].Path)
	require.NotNil(t, volumes[0].Secret.Items[0].Mode)
	assert.Equal(t, int32(0o400), *volumes[0].Secret.Items[0].Mode)

	require.Len(t, service.Spec.Template.Spec.Containers, 1)
	mounts := service.Spec.Template.Spec.Containers[0].VolumeMounts
	require.Len(t, mounts, 1)
	assert.Equal(t, "/etc/app", mounts[0].MountPath)
	assert.Equal(t, volumes[0].Name, mounts[0].Name)

	assert.Equal(t, map[string]string{wantName: "s3cr3t"}, saver.saved)
}

func TestWorkloadWithoutSecretsNeverReachesSecretManager(t *testing.T) {
	saver := newFakeSecretSaver()

	service := convert(t, `{
	  "id": "hello-world-dev",
	  "spec": {
	    "apiVersion": "score.dev/v1b1",
	    "metadata": {"name": "hello-world"},
	    "containers": {
	      "main": {
	        "image": "busybox:latest",
	        "variables": {"DB_HOST": "${resources.db.host}"}
	      }
	    },
	    "resources": {"db": {"type": "postgres"}}
	  },
	  "substitutions": {"resources.db.host": {"secret": false, "value": "db.example.com"}}
	}`, saver)

	require.Len(t, service.Spec.Template.Spec.Containers, 1)
	env := service.Spec.Template.Spec.Containers[0].Env
	require.Len(t, env, 1)
	assert.Equal(t, "db.example.com", env[0].Value)
	assert.Empty(t, saver.saved, "a workload without secrets must convert without touching Secret Manager")
}
