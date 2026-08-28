package cloudrun

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/score-spec/score-go/types"
	"github.com/stretchr/testify/require"
	servingv1 "knative.dev/serving/pkg/apis/serving/v1"

	"github.com/humanitec/cloudrun-container-runner/internal/inputs"
	"github.com/humanitec/cloudrun-container-runner/internal/utils"
)

// fakeSecretSaver stands in for Google Secret Manager, recording what would be
// written and handing back a fixed version, so that conversion can be exercised
// without Google credentials.
type fakeSecretSaver struct {
	saved   map[string]string
	version string
}

func newFakeSecretSaver() *fakeSecretSaver {
	return &fakeSecretSaver{saved: map[string]string{}, version: "1"}
}

func (f *fakeSecretSaver) SaveSecret(_ context.Context, name, value string) (string, error) {
	f.saved[name] = value
	return f.version, nil
}

// driverInputs is the shape the Container Driver sends, as much of it as the
// converter needs. The converter itself does not know about it, but the
// fixtures below are easier to read as the JSON that actually arrives.
type driverInputs struct {
	Id            string         `json:"id"`
	Spec          types.Workload `json:"spec"`
	Substitutions map[string]struct {
		Secret bool `json:"secret"`
		Value  any  `json:"value,omitempty"`
	} `json:"substitutions,omitempty"`
	Extensions map[string]map[string]any `json:"extensions,omitempty"`
}

// optionsFrom turns the Driver's JSON into what FromScoreWorkload takes.
func optionsFrom(t *testing.T, inputsJSON, serviceAccount string) Options {
	t.Helper()

	var in driverInputs
	require.NoError(t, json.Unmarshal([]byte(inputsJSON), &in))

	substitutions := make(map[string]inputs.Input, len(in.Substitutions))
	for key, s := range in.Substitutions {
		if s.Secret {
			substitutions[key] = inputs.Input{Secret: &inputs.SecretInput{Value: s.Value}}
			continue
		}
		substitutions[key] = inputs.Input{Value: s.Value}
	}

	return Options{
		Name:           in.Id,
		Workload:       &in.Spec,
		Substitutions:  substitutions,
		Extension:      in.Extensions[ExtensionName],
		ServiceAccount: serviceAccount,
	}
}

// convert runs the converter over inputsJSON and decodes the manifest it
// produces, with no runtime service account.
func convert(t *testing.T, inputsJSON string, saver SecretSaver) servingv1.Service {
	t.Helper()
	return convertAs(t, inputsJSON, "", saver)
}

// convertAs is convert for a workload that runs as serviceAccount.
func convertAs(t *testing.T, inputsJSON, serviceAccount string, saver SecretSaver) servingv1.Service {
	t.Helper()

	var service servingv1.Service
	require.NoError(t, utils.DecodeViaJSON(convertToMap(t, inputsJSON, serviceAccount, saver), &service))
	return service
}

// convertToMap is convertAs stopping short of the typed manifest, for the tests
// that care whether a field is present at all rather than what it decodes to.
func convertToMap(t *testing.T, inputsJSON, serviceAccount string, saver SecretSaver) map[string]any {
	t.Helper()

	service, err := FromScoreWorkload(t.Context(), optionsFrom(t, inputsJSON, serviceAccount), saver)
	require.NoError(t, err)
	require.NotNil(t, service)

	manifest, err := utils.AsMap(service)
	require.NoError(t, err)
	return manifest
}
