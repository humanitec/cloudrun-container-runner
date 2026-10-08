package score

import "github.com/score-spec/score-go/types"

type Generator interface {
	// Generate produces deployable artefacts for the target platform.
	Generate(*types.Workload, map[string]SubValue) (map[string][]byte, error)
}
