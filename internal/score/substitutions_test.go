package score

import (
	"testing"

	"github.com/score-spec/score-go/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolvePlaceholder(t *testing.T) {
	workload := types.Workload{
		Metadata: map[string]any{
			"name": "test",
			"annotations": map[string]any{
				"one": "VALUE1",
			},
		},
		Containers: map[string]types.Container{
			"one": {
				Image: "image-one:latest",
			},
			"two": {
				Image: "image-two:latest",
			},
		},
		Resources: types.WorkloadResources{
			"db": types.Resource{
				Type: "postgres",
			},
			"readonly-store": types.Resource{
				Type:  "bucket",
				Class: new("ro"),
			},
			"shared-store": types.Resource{
				Type:  "bucket",
				Class: new("rw"),
				Id:    new("common"),
			},
			"missing": types.Resource{
				Type: "cheese",
			},
		},
	}
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
		name          string
		placeholder   string
		expected      *SubValue
		containerName string
		shouldFail    bool
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
		{
			name:        "resource in score but not params",
			placeholder: "resources.missing.name",
			shouldFail:  true,
		},
		{
			name:        "metadata.name",
			placeholder: "metadata.name",
			expected:    &SubValue{Value: "test"},
		},
		{
			name:        "metadata annotation",
			placeholder: "metadata.annotations.one",
			expected:    &SubValue{Value: "VALUE1"},
		},
		{
			name:        "non existent metadata annotation",
			placeholder: "metadata.annotations.does-not-exist",
			shouldFail:  true,
		},

		{
			name:          "container image",
			placeholder:   "container.image",
			containerName: "two",
			expected:      &SubValue{Value: "image-two:latest"},
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			actual, err := ResolvePlaceholder(testCase.placeholder, testCase.containerName, workload, substitutions)
			if testCase.shouldFail {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, testCase.expected, actual)
			}
		})
	}
}
func TestResolvePlaceholder_EmptyWorkload(t *testing.T) {
	workload := types.Workload{}
	t.Run("no metadata", func(t *testing.T) {
		_, err := ResolvePlaceholder("metadata.name", "container-one", workload, nil)
		assert.Error(t, err)
	})
	t.Run("no resources", func(t *testing.T) {
		_, err := ResolvePlaceholder("resources.does-not-exist.name", "container-one", workload, nil)
		assert.Error(t, err)
	})

	t.Run("no container", func(t *testing.T) {
		_, err := ResolvePlaceholder("container.image", "container-one", workload, nil)
		assert.Error(t, err)
	})

}
