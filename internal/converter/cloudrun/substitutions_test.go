package cloudrun

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

	service, err := FromScoreWorkload(t.Context(), optionsFrom(t, inputsJSON, ""), newFakeSecretSaver())
	require.NoError(t, err)
	require.NotNil(t, service)
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
			value:    `{"value": "db.example.com"}`,
			variable: "${resources.db.field}",
			expected: "db.example.com",
		},
		{
			name:     "integer keeps its integral form",
			value:    `{"value": 5432}`,
			variable: "${resources.db.field}",
			expected: "5432",
		},
		{
			name:     "negative integer",
			value:    `{"value": -1}`,
			variable: "${resources.db.field}",
			expected: "-1",
		},
		{
			name:     "float",
			value:    `{"value": 1.5}`,
			variable: "${resources.db.field}",
			expected: "1.5",
		},
		{
			name:     "boolean",
			value:    `{"value": true}`,
			variable: "${resources.db.field}",
			expected: "true",
		},
		{
			name:     "object renders as compact JSON",
			value:    `{"value": {"sslmode": "require"}}`,
			variable: "${resources.db.field}",
			expected: `{"sslmode":"require"}`,
		},
		{
			name:     "array renders as compact JSON",
			value:    `{"value": [1, "two"]}`,
			variable: "${resources.db.field}",
			expected: `[1,"two"]`,
		},
		{
			name:     "integer embedded in a larger string",
			value:    `{"value": 5432}`,
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
