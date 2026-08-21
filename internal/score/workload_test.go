package score

import (
	"testing"

	"github.com/score-spec/score-go/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/humanitec/cloudrun-container-runner/internal/inputs"
)

func TestGetAllPlaceholdersInString(t *testing.T) {
	testCases := []struct {
		name     string
		str      string
		expected []string
	}{
		{
			name:     "no placeholders",
			str:      "hello world",
			expected: []string{},
		},
		{
			name: "one placeholder whole string",
			str:  "${resources.bucket.name}",
			expected: []string{
				"resources.bucket.name",
			},
		},
		{
			name: "2 placeholders",
			str:  "${resources.bucket.name}${resources.db.host}",
			expected: []string{
				"resources.bucket.name",
				"resources.db.host",
			},
		},
		{
			name: "2 placeholders big string",
			str:  "Hello ${resources.bucket.name} world ${resources.db.host}!",
			expected: []string{
				"resources.bucket.name",
				"resources.db.host",
			},
		},
		{
			name:     "no placeholders, escaped",
			str:      "Hello $${resources.bucket.name} world $${resources.db.host}!",
			expected: []string{},
		},

		{
			name: "repleasted placeholders",
			str:  "Hello ${resources.bucket.name} world ${resources.db.host}! I said ${resources.bucket.name}!",
			expected: []string{
				"resources.bucket.name",
				"resources.db.host",
			},
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			actual := GetAllPlaceholdersInString(testCase.str)
			assert.ElementsMatch(t, testCase.expected, actual)
		})
	}
}

func TestGetAllPlaceholders(t *testing.T) {
	testCases := []struct {
		name     string
		input    any
		expected []string
	}{
		{
			name:     "no placeholders",
			input:    []any{"hello world"},
			expected: []string{},
		},
		{
			name: "placeholders in map",
			input: map[string]any{
				"one":   "${resources.bucket.name}",
				"two":   "${resources.db.host}",
				"three": "Hello world",
			},
			expected: []string{
				"resources.bucket.name",
				"resources.db.host",
			},
		},

		{
			name: "placeholders in slice",
			input: []any{
				"${resources.bucket.name}",
				"${resources.db.host}",
				"Hello world",
			},
			expected: []string{
				"resources.bucket.name",
				"resources.db.host",
			},
		},
		{
			name: "complex object with duplicate placeholders",
			input: map[string]any{
				"sliceOfMaps": []any{
					map[string]any{
						"one": "Escaped $${resources.bucket.horse}",
						"two": "Unescaped ${resources.bucket.name}",
					},
					map[string]any{
						"one": "This is ${resources.db.host} of the cheese",
						"two": "Unescaped ${resources.bucket.name}",
					},
				},
				"mapOfSlices": map[string]any{
					"One": []any{
						"hello world",
						"${resources.bucket.name}",
						"${resources.db.port}",
					},
					"Two": []any{
						"hello world",
						"${resources.bucket.name}",
						"${resources.db.username}",
					},
				},
			},
			expected: []string{
				"resources.bucket.name",
				"resources.db.host",
				"resources.db.username",
				"resources.db.port",
			},
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			actual := GetAllPlaceholders(testCase.input)
			assert.ElementsMatch(t, testCase.expected, actual)
		})
	}
}

func TestReplaceAllPlaceholdersInString(t *testing.T) {
	placeholderStr := map[string]string{
		"resources.bucket.name": "nameOfBucket",
		"resources.db.host":     "hostOfDatabase",
	}
	testCases := []struct {
		name     string
		str      string
		expected string
	}{
		{
			name:     "no placeholders",
			str:      "hello world",
			expected: "hello world",
		},
		{
			name:     "one placeholder whole string",
			str:      "${resources.bucket.name}",
			expected: "nameOfBucket",
		},
		{
			name:     "2 placeholders",
			str:      "${resources.bucket.name}${resources.db.host}",
			expected: "nameOfBuckethostOfDatabase",
		},
		{
			name:     "2 placeholders big string",
			str:      "Hello ${resources.bucket.name} world ${resources.db.host}!",
			expected: "Hello nameOfBucket world hostOfDatabase!",
		},
		{
			name:     "no placeholders, escaped",
			str:      "Hello $${resources.bucket.name} world $${resources.db.host}!",
			expected: "Hello ${resources.bucket.name} world ${resources.db.host}!",
		},

		{
			name:     "incremental escaping",
			str:      "Hello ${resources.bucket.name} world $${resources.bucket.name} cheese $$${resources.bucket.name} and $${${resources.bucket.name}}",
			expected: "Hello nameOfBucket world ${resources.bucket.name} cheese $nameOfBucket and ${nameOfBucket}",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			actual, err := ReplaceAllPlaceholdersInString(testCase.str, placeholderStr)
			require.NoError(t, err)
			assert.Equal(t, testCase.expected, actual)
		})
	}
}
func TestReplaceAllPlaceholdersInString_Error(t *testing.T) {
	placeholderStr := map[string]string{
		"resources.bucket.name": "nameOfBucket",
		"resources.db.host":     "hostOfDatabase",
	}
	testCases := []struct {
		name string
		str  string
	}{
		{
			name: "one placeholder whole string",
			str:  "${resources.missing.name}",
		},
		{
			name: "2 placeholders, one missing",
			str:  "${resources.bucket.name}${resources.missing.host}",
		},
		{
			name: "incomplete placeholder whole string",
			str:  "${resources.missing.name",
		},
		{
			name: "incomplete placeholder middle of string",
			str:  "Hello ${resources.bucket.name world!",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := ReplaceAllPlaceholdersInString(testCase.str, placeholderStr)
			assert.Error(t, err)
		})
	}
}

func TestReplaceAllPlaceholders(t *testing.T) {
	placeholderStr := map[string]string{
		"resources.bucket.name": "nameOfBucket",
		"resources.db.host":     "hostOfDatabase",
		"resources.db.port":     "portOfDatabase",
		"resources.db.username": "userOfDatabase",
	}
	testCases := []struct {
		name     string
		input    any
		expected any
	}{
		{
			name:     "no placeholders slice",
			input:    []any{"hello world"},
			expected: []any{"hello world"},
		}, {
			name: "no placeholders map",
			input: map[string]any{
				"one": "hello world",
			},
			expected: map[string]any{
				"one": "hello world",
			},
		},
		{
			name: "placeholders in map",
			input: map[string]any{
				"one":   "${resources.bucket.name}",
				"two":   "${resources.db.host}",
				"three": "Hello world",
			},
			expected: map[string]any{
				"one":   "nameOfBucket",
				"two":   "hostOfDatabase",
				"three": "Hello world",
			},
		},

		{
			name: "placeholders in slice",
			input: []any{
				"${resources.bucket.name}",
				"${resources.db.host}",
				"Hello world",
			},
			expected: []any{
				"nameOfBucket",
				"hostOfDatabase",
				"Hello world",
			},
		},
		{
			name: "complex object with duplicate placeholders",
			input: map[string]any{
				"sliceOfMaps": []any{
					map[string]any{
						"one": "Escaped $${resources.bucket.horse}",
						"two": "Unescaped ${resources.bucket.name}",
					},
					map[string]any{
						"one": "This is ${resources.db.host} of the cheese",
						"two": "Unescaped ${resources.bucket.name}",
					},
				},
				"mapOfSlices": map[string]any{
					"One": []any{
						"hello world",
						"${resources.bucket.name}",
						"${resources.db.port}",
					},
					"Two": []any{
						"hello world",
						"${resources.bucket.name}",
						"${resources.db.username}",
					},
				},
			},
			expected: map[string]any{
				"sliceOfMaps": []any{
					map[string]any{
						"one": "Escaped ${resources.bucket.horse}",
						"two": "Unescaped nameOfBucket",
					},
					map[string]any{
						"one": "This is hostOfDatabase of the cheese",
						"two": "Unescaped nameOfBucket",
					},
				},
				"mapOfSlices": map[string]any{
					"One": []any{
						"hello world",
						"nameOfBucket",
						"portOfDatabase",
					},
					"Two": []any{
						"hello world",
						"nameOfBucket",
						"userOfDatabase",
					},
				},
			},
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			actual, err := ReplaceAllPlaceholders(testCase.input, placeholderStr)
			require.NoError(t, err)
			assert.Equal(t, testCase.expected, actual)
		})
	}
}

func TestReplaceAllPlaceholders_Error(t *testing.T) {
	placeholderStr := map[string]string{
		"resources.bucket.name": "nameOfBucket",
		"resources.db.host":     "hostOfDatabase",
		"resources.db.port":     "portOfDatabase",
		"resources.db.username": "userOfDatabase",
	}
	testCases := []struct {
		name  string
		input any
	}{
		{
			name:  "missing placeholders slice",
			input: []any{"${resources.missing.name}"},
		}, {
			name: "missing placeholders map",
			input: map[string]any{
				"one": "${resources.missing.name}",
			},
		},
		{
			name: "complex object with error deep in map",
			input: map[string]any{
				"sliceOfMaps": []any{
					map[string]any{
						"one": "Escaped ${resources.bucket.name",
						"two": "Unescaped ${resources.bucket.name}",
					},
					map[string]any{
						"one": "This is ${resources.db.host} of the cheese",
						"two": "Unescaped ${resources.bucket.name}",
					},
				},
			},
		},
		{
			name: "complex object with error deep in slice",
			input: map[string]any{
				"mapOfSlices": map[string]any{
					"One": []any{
						"hello world",
						"${resources.bucket.name",
						"${resources.db.port}",
					},
					"Two": []any{
						"hello world",
						"${resources.bucket.name}",
						"${resources.db.username}",
					},
				},
			},
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := ReplaceAllPlaceholders(testCase.input, placeholderStr)
			assert.Error(t, err)
		})
	}
}

func TestOutputForPlaceholder(t *testing.T) {
	wr := &WorkloadResource{
		Workload: &types.Workload{
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
					Class: ptrStr("ro"),
				},
				"shared-store": types.Resource{
					Type:  "bucket",
					Class: ptrStr("rw"),
					Id:    ptrStr("common"),
				},
				"missing": types.Resource{
					Type: "cheese",
				},
			},
		},
		Substitutions: map[string]inputs.Input{
			"resources.db.name":     {Value: "db_name"},
			"resources.db.port":     {Value: 5432},
			"resources.db.host":     {Value: "db.example.com"},
			"resources.db.username": {Value: "test-user"},
			"resources.db.password": {Secret: &inputs.SecretInput{
				Store: "secrets",
				Key:   "my/db/password",
			}},

			"resources.readonly-store.name": {Value: "read-only bucket"},

			"resources.shared-store.name": {Value: "shared bucket"},
		},
		Name: "workloads.test",
	}
	testCases := []struct {
		name          string
		placeholder   string
		expected      inputs.Input
		containerName string
		shouldFail    bool
	}{
		{
			name:        "basic name",
			placeholder: "resources.db.name",
			expected:    inputs.Input{Value: "db_name"},
		},
		{
			name:        "non-default class",
			placeholder: "resources.readonly-store.name",
			expected:    inputs.Input{Value: "read-only bucket"},
		},
		{
			name:        "id specified",
			placeholder: "resources.shared-store.name",
			expected:    inputs.Input{Value: "shared bucket"},
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
			expected:    inputs.Input{Value: "test"},
		},
		{
			name:        "metadata annotation",
			placeholder: "metadata.annotations.one",
			expected:    inputs.Input{Value: "VALUE1"},
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
			expected:      inputs.Input{Value: "image-two:latest"},
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			actual, err := wr.OutputForPlaceholder(testCase.placeholder, testCase.containerName)
			if testCase.shouldFail {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, testCase.expected, actual)
			}
		})
	}
}
func TestOutputForPlaceholder_EmptyWorkload(t *testing.T) {
	wr := &WorkloadResource{
		Workload: &types.Workload{},
	}
	t.Run("no metadata", func(t *testing.T) {
		_, err := wr.OutputForPlaceholder("metadata.name", "container-one")
		assert.Error(t, err)
	})
	t.Run("no resources", func(t *testing.T) {
		_, err := wr.OutputForPlaceholder("resources.does-not-exist.name", "container-one")
		assert.Error(t, err)
	})

	t.Run("no container", func(t *testing.T) {
		_, err := wr.OutputForPlaceholder("container.image", "container-one")
		assert.Error(t, err)
	})

}

func TestExpandFile(t *testing.T) {
	wr := &WorkloadResource{
		Workload: &types.Workload{
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
				"cache": types.Resource{
					Type: "redis",
				},
				"shared-store": types.Resource{
					Type:  "bucket",
					Class: ptrStr("rw"),
					Id:    ptrStr("common"),
				},
				"missing": types.Resource{
					Type: "volume",
				},
			},
		},
		Substitutions: map[string]inputs.Input{
			"resources.db.name":     {Value: "db_name"},
			"resources.db.port":     {Value: 5432},
			"resources.db.host":     {Value: "db.example.com"},
			"resources.db.username": {Value: "test_user"},
			"resources.db.password": {Secret: &inputs.SecretInput{
				Store: "secret",
				Key:   "mysecret/mypassword",
			}},

			"resources.shared-store.name": {Value: "shared bucket"},

			"resources.cache.name":     {Value: "redis"},
			"resources.cache.port":     {Value: 6379},
			"resources.cache.host":     {Value: "cache.example.com"},
			"resources.cache.username": {Value: "redis_user"},
			"resources.cache.password": {Secret: &inputs.SecretInput{
				Value: "r3di5-p455w0rd",
			}},
		},
		Name: "workloads.test",
	}
	t.Run("no placeholders", func(t *testing.T) {
		file := types.ContainerFile{
			Content: ptrStr("Hello World!"),
		}
		output, err := wr.ExpandFile(file, "one")
		require.NoError(t, err)
		assert.Equal(t, inputs.Input{Value: "Hello World!"}, output)
	})
	t.Run("placeholders no expand", func(t *testing.T) {
		file := types.ContainerFile{
			Content:  ptrStr("Should not expand ${resources.db.name}"),
			NoExpand: ptr(true),
		}
		output, err := wr.ExpandFile(file, "one")
		require.NoError(t, err)
		assert.Equal(t, inputs.Input{Value: "Should not expand ${resources.db.name}"}, output)
	})
	t.Run("single placeholder expand", func(t *testing.T) {
		file := types.ContainerFile{
			Content: ptrStr("${resources.db.name}"),
		}
		output, err := wr.ExpandFile(file, "one")
		require.NoError(t, err)
		assert.Equal(t, inputs.Input{Value: "db_name"}, output)
	})
	t.Run("container placeholder", func(t *testing.T) {
		file := types.ContainerFile{
			Content: ptrStr("${container.image}"),
		}
		output, err := wr.ExpandFile(file, "one")
		require.NoError(t, err)
		assert.Equal(t, inputs.Input{Value: "image-one:latest"}, output)
	})
	t.Run("multiple placeholder expand", func(t *testing.T) {
		file := types.ContainerFile{
			Content: ptrStr("${resources.db.name} ${resources.shared-store.name}"),
		}
		output, err := wr.ExpandFile(file, "one")
		require.NoError(t, err)
		assert.Equal(t, inputs.Input{Value: "db_name shared bucket"}, output)
	})
	t.Run("single placeholder secret", func(t *testing.T) {
		file := types.ContainerFile{
			Content: ptrStr("${resources.db.password}"),
		}
		output, err := wr.ExpandFile(file, "one")
		require.NoError(t, err)
		assert.Equal(t, inputs.Input{Secret: &inputs.SecretInput{
			Store: "secret",
			Key:   "mysecret/mypassword",
		}}, output)
	})
	t.Run("multiple placeholder expand secrets", func(t *testing.T) {
		file := types.ContainerFile{
			Content: ptrStr("redis://${resources.cache.username}:${resources.cache.password}@${resources.cache.host}:${resources.cache.port}/${resources.cache.name}"),
		}
		output, err := wr.ExpandFile(file, "one")
		require.NoError(t, err)
		assert.Equal(t, inputs.Input{Secret: &inputs.SecretInput{
			Value: "redis://redis_user:r3di5-p455w0rd@cache.example.com:6379/redis",
		}}, output)
	})

	t.Run("multiple placeholder secrets fail as not value", func(t *testing.T) {
		file := types.ContainerFile{
			Content: ptrStr("postgresql://${resources.db.username}:${resources.db.password}@${resources.db.host}:${resources.db.port}/${resources.db.name}"),
		}
		_, err := wr.ExpandFile(file, "one")
		assert.Error(t, err)
	})
}
