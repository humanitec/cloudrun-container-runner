package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	servingv1 "knative.dev/serving/pkg/apis/serving/v1"

	"github.com/humanitec/cloudrun-container-runner/internal/utils"
)

func envForSubstitution(t *testing.T, substitutionJSON, variable string) string {
	t.Helper()

	inputsJSON := `{
	  "id": "hello-world-dev",
	  "spec": {
	    "apiVersion": "score.dev/v1b1",
	    "metadata": {"name": "hello-world"},
	    "containers": {
	      "main": {
	        "image": "busybox:latest",
	        "variables": {"VALUE": "` + variable + `"}
	      }
	    },
	    "resources": {"db": {"type": "postgres"}}
	  },
	  "substitutions": {"resources.db.field": ` + substitutionJSON + `}
	}`

	path := filepath.Join(t.TempDir(), "inputs.json")
	require.NoError(t, os.WriteFile(path, []byte(inputsJSON), 0o600))

	in, err := readResourceInputs(path)
	require.NoError(t, err)

	out, err := scoreWorkloadToCloudRunService(in)
	require.NoError(t, err)
	require.Len(t, out.Manifests, 1)

	var service servingv1.Service
	require.NoError(t, utils.DecodeViaJSON(out.Manifests[0], &service))
	require.Len(t, service.Spec.Template.Spec.Containers, 1)

	env := service.Spec.Template.Spec.Containers[0].Env
	require.Len(t, env, 1)
	require.Equal(t, "VALUE", env[0].Name)
	return env[0].Value
}

func TestSubstitutionValueTypes(t *testing.T) {
	testCases := []struct {
		name     string
		value    string
		variable string
		expected string
	}{
		{
			name:     "string",
			value:    `{"secret": false, "value": "db.example.com"}`,
			variable: "${resources.db.field}",
			expected: "db.example.com",
		},
		{
			name:     "integer keeps its integral form",
			value:    `{"secret": false, "value": 5432}`,
			variable: "${resources.db.field}",
			expected: "5432",
		},
		{
			name:     "negative integer",
			value:    `{"secret": false, "value": -1}`,
			variable: "${resources.db.field}",
			expected: "-1",
		},
		{
			name:     "float",
			value:    `{"secret": false, "value": 1.5}`,
			variable: "${resources.db.field}",
			expected: "1.5",
		},
		{
			name:     "boolean",
			value:    `{"secret": false, "value": true}`,
			variable: "${resources.db.field}",
			expected: "true",
		},
		{
			name:     "object renders as compact JSON",
			value:    `{"secret": false, "value": {"sslmode": "require"}}`,
			variable: "${resources.db.field}",
			expected: `{"sslmode":"require"}`,
		},
		{
			name:     "array renders as compact JSON",
			value:    `{"secret": false, "value": [1, "two"]}`,
			variable: "${resources.db.field}",
			expected: `[1,"two"]`,
		},
		{
			name:     "integer embedded in a larger string",
			value:    `{"secret": false, "value": 5432}`,
			variable: "postgresql://db.example.com:${resources.db.field}/app",
			expected: "postgresql://db.example.com:5432/app",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			assert.Equal(t, testCase.expected, envForSubstitution(t, testCase.value, testCase.variable))
		})
	}
}
