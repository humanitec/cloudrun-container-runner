package score

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolvePlaceholder(t *testing.T) {
	substitutions := map[string]SubValue{
		"resources.db.name":     {Value: "db_name"},
		"resources.db.port":     {Value: 5432},
		"resources.db.host":     {Value: "db.example.com"},
		"resources.db.username": {Value: "test-user"},
		"resources.db.password": {Secret: &SecretRef{
			Store: "secrets",
			Ref:   "my/db/password",
		}},

		"resources.readonly-store.name": {Value: "read-only bucket"},

		"resources.shared-store.name": {Value: "shared bucket"},
	}
	testCases := []struct {
		name        string
		placeholder string
		expected    *SubValue
		shouldFail  bool
	}{
		{
			name:        "basic name",
			placeholder: "resources.db.name",
			expected:    &SubValue{Value: "db_name"},
		},
		{
			name:        "non-default class",
			placeholder: "resources.readonly-store.name",
			expected:    &SubValue{Value: "read-only bucket"},
		},
		{
			name:        "id specified",
			placeholder: "resources.shared-store.name",
			expected:    &SubValue{Value: "shared bucket"},
		},
		{
			name:        "name does not exist",
			placeholder: "resources.no-exist.name",
			shouldFail:  true,
		},
		// metadata and container placeholders are replaced when the spec is normalized.
		{
			name:        "metadata placeholder",
			placeholder: "metadata.name",
			shouldFail:  true,
		},
		{
			name:        "container placeholder",
			placeholder: "container.image",
			shouldFail:  true,
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			actual, err := ResolvePlaceholder(testCase.placeholder, substitutions)
			if testCase.shouldFail {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, testCase.expected, actual)
			}
		})
	}
}
func TestResolvePlaceholder_NoSubstitutions(t *testing.T) {
	t.Run("no metadata", func(t *testing.T) {
		_, err := ResolvePlaceholder("metadata.name", nil)
		assert.Error(t, err)
	})
	t.Run("no resources", func(t *testing.T) {
		_, err := ResolvePlaceholder("resources.does-not-exist.name", nil)
		assert.Error(t, err)
	})

	t.Run("no container", func(t *testing.T) {
		_, err := ResolvePlaceholder("container.image", nil)
		assert.Error(t, err)
	})

}
