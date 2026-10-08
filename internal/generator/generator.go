package generator

import (
	"github.com/score-spec/score-go/types"

	"github.com/humanitec/cloudrun-container-runner/internal/score"
)

type CloudRunGenerator struct{}

// Generate generates a Cloud Run Service manifest from the score spec and the substitution map.
func (p *CloudRunGenerator) Generate(spec *types.Workload, substitutions map[string]score.SubValue) (map[string][]byte, error) {
	return map[string][]byte{}, nil
}
