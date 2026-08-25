package google

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTargetFromEnv(t *testing.T) {
	t.Setenv(EnvProject, "my-gcp-project")
	t.Setenv(EnvRegion, "europe-west1")

	assert.Equal(t, Target{Project: "my-gcp-project", Region: "europe-west1"}, TargetFromEnv())
}

// A workload that needs nothing from Google Cloud has to keep working with
// neither variable set, so reading the target must not fail on its own.
func TestTargetFromEnvToleratesAnEmptyEnvironment(t *testing.T) {
	t.Setenv(EnvProject, "")
	t.Setenv(EnvRegion, "")

	assert.Equal(t, Target{}, TargetFromEnv())
}

func TestTargetValidate(t *testing.T) {
	testCases := []struct {
		name        string
		target      Target
		wantErrText string
	}{
		{
			name:   "complete",
			target: Target{Project: "my-gcp-project", Region: "europe-west1"},
		},
		{
			name:        "no project",
			target:      Target{Region: "europe-west1"},
			wantErrText: EnvProject,
		},
		{
			name:        "no region",
			target:      Target{Project: "my-gcp-project"},
			wantErrText: EnvRegion,
		},
		{
			// The project is reported first, so the operator fixes one at a time.
			name:        "neither",
			target:      Target{},
			wantErrText: EnvProject,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.target.Validate()
			if tc.wantErrText == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tc.wantErrText)
		})
	}
}
