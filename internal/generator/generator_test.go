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
