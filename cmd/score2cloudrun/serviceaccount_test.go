package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// helloWorldInputs is the smallest workload that converts, for the tests that
// are about the service around the container rather than the container itself.
const helloWorldInputs = `{
  "id": "hello-world-dev",
  "spec": {
    "apiVersion": "score.dev/v1b1",
    "metadata": {"name": "hello-world"},
    "containers": {
      "main": {"image": "busybox:latest"}
    }
  }
}`

func TestRuntimeServiceAccountReachesTheRevisionSpec(t *testing.T) {
	const account = "cloudrun-runtime@my-gcp-project.iam.gserviceaccount.com"

	service := convertAs(t, helloWorldInputs, account, newFakeSecretSaver())

	assert.Equal(t, account, service.Spec.Template.Spec.ServiceAccountName)
}

// Cloud Run reads the runtime identity from spec.template.spec.serviceAccountName,
// so the field has to sit under the revision template rather than on the
// service. Knative inlines the Pod spec, which is what puts it there, and the
// typed round trip in convertAs would hide the field moving.
func TestRuntimeServiceAccountIsNestedUnderTheRevisionTemplate(t *testing.T) {
	const account = "cloudrun-runtime@my-gcp-project.iam.gserviceaccount.com"

	manifest := convertToMap(t, helloWorldInputs, account, newFakeSecretSaver())

	spec, ok := manifest["spec"].(map[string]any)
	require.True(t, ok, "manifest has no spec object")
	template, ok := spec["template"].(map[string]any)
	require.True(t, ok, "spec has no template object")
	templateSpec, ok := template["spec"].(map[string]any)
	require.True(t, ok, "template has no spec object")

	assert.Equal(t, account, templateSpec["serviceAccountName"])
}

// An empty account has to leave the key out altogether rather than send an
// empty string, which is how Cloud Run is asked for the project's default
// compute service account.
func TestNoRuntimeServiceAccountOmitsTheField(t *testing.T) {
	manifest := convertToMap(t, helloWorldInputs, "", newFakeSecretSaver())

	spec := manifest["spec"].(map[string]any)
	template := spec["template"].(map[string]any)
	templateSpec := template["spec"].(map[string]any)

	assert.NotContains(t, templateSpec, "serviceAccountName")
}

// The runtime account is orthogonal to everything the Score file says, so it
// must not disturb the rest of the conversion.
func TestRuntimeServiceAccountLeavesTheWorkloadAlone(t *testing.T) {
	const account = "cloudrun-runtime@my-gcp-project.iam.gserviceaccount.com"

	withAccount := convertToMap(t, helloWorldInputs, account, newFakeSecretSaver())
	withoutAccount := convertToMap(t, helloWorldInputs, "", newFakeSecretSaver())

	spec := withAccount["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
	delete(spec, "serviceAccountName")

	assert.Equal(t, withoutAccount, withAccount)
}
