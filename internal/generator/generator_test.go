package generator

import (
	"encoding/json"
	"testing"

	"github.com/score-spec/score-go/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	servingv1 "knative.dev/serving/pkg/apis/serving/v1"

	"github.com/humanitec/cloudrun-container-runner/internal/google/secretmanager"
	"github.com/humanitec/cloudrun-container-runner/internal/score"
)

// A generator is not bound to one service: each workload names its own service and secrets.
func TestGenerateMultipleWorkloads(t *testing.T) {
	substitutions := map[string]score.SubValue{
		"resources.db.password": {Secret: &score.SecretRef{Type: score.DirectSecretType, Value: "s3cr3t"}},
	}
	workloadFor := func(name string) types.Workload {
		return types.Workload{
			Metadata: types.WorkloadMetadata{"name": name},
			Containers: types.WorkloadContainers{"main": {
				Image:     "busybox:latest",
				Variables: types.ContainerVariables{"WORKLOAD": "${metadata.name}", "PASSWORD": "${resources.db.password}"},
			}},
			Resources: types.WorkloadResources{"db": {Type: "postgres"}},
		}
	}
	saver := newFakeSecretSaver()
	gen := NewCloudRunGenerator(saver)

	for _, tc := range []struct{ workload, service string }{
		{workload: "one", service: "one-dev"},
		{workload: "two", service: "two-dev"},
	} {
		workload := WithServiceName(workloadFor(tc.workload), tc.service)
		manifests, err := gen.Generate(t.Context(), &workload, substitutions)
		require.NoError(t, err)

		var service servingv1.Service
		require.NoError(t, json.Unmarshal(manifests["service"], &service))
		assert.Equal(t, tc.service, service.Name)
		env := service.Spec.Template.Spec.Containers[0].Env
		require.Len(t, env, 2)
		assert.Equal(t, tc.workload, env[1].Value, "${metadata.name} is the workload's name")

		secretName := secretmanager.SecretName(tc.service, "main", "env", "PASSWORD")
		assert.Equal(t, secretName, env[0].ValueFrom.SecretKeyRef.Name)
		assert.Equal(t, tc.service, saver.services[secretName], "the secret is saved for its own service")
	}
}

// A service block without containers has no container to put the port on.
func TestGenerateNoContainers(t *testing.T) {
	workload := types.Workload{
		Metadata: types.WorkloadMetadata{"name": "hello-world"},
		Service:  &types.WorkloadService{},
	}

	manifests, err := NewCloudRunGenerator(newFakeSecretSaver()).Generate(t.Context(), &workload, nil)

	require.ErrorContains(t, err, "needs at least one container")
	assert.Nil(t, manifests)
}

// Without a name, secrets would be saved under "" before Cloud Run rejects the service.
func TestGenerateNoServiceName(t *testing.T) {
	workload := types.Workload{
		Containers: types.WorkloadContainers{"main": {
			Image:     "busybox:latest",
			Variables: types.ContainerVariables{"PASSWORD": "${resources.db.password}"},
		}},
		Resources: types.WorkloadResources{"db": {Type: "postgres"}},
	}
	substitutions := map[string]score.SubValue{
		"resources.db.password": {Secret: &score.SecretRef{Type: score.DirectSecretType, Value: "s3cr3t"}},
	}
	saver := newFakeSecretSaver()

	manifests, err := NewCloudRunGenerator(saver).Generate(t.Context(), &workload, substitutions)

	require.ErrorContains(t, err, "needs a name")
	assert.Nil(t, manifests)
	assert.Empty(t, saver.services, "no secret is saved")
}

func TestGenerateNilWorkload(t *testing.T) {
	manifests, err := NewCloudRunGenerator(newFakeSecretSaver()).Generate(t.Context(), nil, nil)

	require.ErrorContains(t, err, "no workload")
	assert.Nil(t, manifests)
}
