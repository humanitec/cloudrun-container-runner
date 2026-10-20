package generator

import (
	"testing"

	"github.com/score-spec/score-go/types"
	"github.com/stretchr/testify/assert"
)

func TestServiceName(t *testing.T) {
	testCases := []struct {
		name     string
		metadata types.WorkloadMetadata
		want     string
	}{
		{name: "no metadata", want: ""},
		{name: "metadata.name", metadata: types.WorkloadMetadata{"name": "hello-world"}, want: "hello-world"},
		{
			name: "annotation wins",
			metadata: types.WorkloadMetadata{
				"name":        "hello-world",
				"annotations": map[string]any{ServiceNameAnnotation: "hello-world-dev"},
			},
			want: "hello-world-dev",
		},
		{
			name: "empty annotation falls back",
			metadata: types.WorkloadMetadata{
				"name":        "hello-world",
				"annotations": map[string]any{ServiceNameAnnotation: ""},
			},
			want: "hello-world",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			assert.Equal(t, testCase.want, ServiceName(&types.Workload{Metadata: testCase.metadata}))
		})
	}
}

func TestWithServiceName(t *testing.T) {
	t.Run("leaves the original untouched", func(t *testing.T) {
		original := types.Workload{Metadata: types.WorkloadMetadata{
			"name":        "hello-world",
			"annotations": map[string]any{"team": "platform"},
		}}

		named := WithServiceName(original, "hello-world-dev")

		assert.Equal(t, "hello-world-dev", ServiceName(&named))
		assert.Equal(t, "hello-world", named.Metadata["name"], "metadata.name is kept")
		assert.Equal(t, "platform", named.Metadata["annotations"].(map[string]any)["team"], "other annotations are kept")
		assert.Equal(t, types.WorkloadMetadata{
			"name":        "hello-world",
			"annotations": map[string]any{"team": "platform"},
		}, original.Metadata)
	})

	t.Run("no metadata", func(t *testing.T) {
		named := WithServiceName(types.Workload{}, "hello-world-dev")
		assert.Equal(t, "hello-world-dev", ServiceName(&named))
	})
}
