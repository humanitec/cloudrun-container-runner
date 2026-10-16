package score

import (
	"fmt"
	"strings"

	"github.com/score-spec/score-go/types"
)

// TODO: these types are supposed to be defined in score-go library. Update when defined.
// See proposal: https://docs.google.com/document/d/1I-5sm_IFoDOiM4aBwXO05iupVLhspQ2tpHsfOCccUGY/edit?tab=t.0

const DirectSecretType = "direct"

// SubValue represents a secret or non-secret value in the substitution map
type SubValue struct {
	Value  any        `json:"value,omitempty"`
	Secret *SecretRef `json:"secret,omitempty"`
}

// SecretRef represents a secret value to be used in substitutions.
type SecretRef struct {
	Type  string `json:"type"`
	Store string `json:"store,omitempty"`
	Ref   string `json:"ref,omitempty"`
	Value any    `json:"value,omitempty"`
}

// ResolvePlaceholder resolves a placeholder expression to a SubValue object, for the given workload
// and substitutions map.
// A placeholder expression is surrounded by ${...}, and can contain two or more parts, separated with a dot.
func ResolvePlaceholder(placeholder, containerName string, workload types.Workload, substitutions map[string]SubValue) (*SubValue, error) {
	parts := strings.Split(placeholder, ".")
	if len(parts) < 2 {
		return nil, fmt.Errorf("invalid placeholder: must have at least 2 parts, got \"%s\"", placeholder)
	}
	switch parts[0] {
	case "resources":
		if sub, exist := substitutions[placeholder]; exist {
			return &sub, nil
		}
		return nil, fmt.Errorf("resolving placeholder \"%s\": no substitution found", placeholder)
	case "metadata":
		val, err := placeholderResolveInMap(parts[1:], workload.Metadata)
		if err != nil {
			return nil, fmt.Errorf("resolving placeholder \"%s\": %w", placeholder, err)
		}
		return &SubValue{Value: val}, nil
	case "container":
		if container, exists := workload.Containers[containerName]; exists {
			if parts[1] == "image" {
				return &SubValue{Value: container.Image}, nil
			}
			return nil, fmt.Errorf("resolving placeholder \"%s\": container can only reference image", placeholder)
		}
		return nil, fmt.Errorf("resolving placeholder \"%s\": container %s not found", placeholder, containerName)
	default:
		return nil, fmt.Errorf("invalid placeholder: must start with \"resources\", \"container\" or \"metadata\", got \"%s\"", placeholder)
	}
}

func placeholderResolveInMap(placeholder []string, m map[string]any) (any, error) {
	if len(placeholder) > 0 {
		if m != nil {
			if val, exists := m[placeholder[0]]; exists {
				if len(placeholder) == 1 {
					return val, nil
				}
				if newM, ok := val.(map[string]any); ok {
					return placeholderResolveInMap(placeholder[1:], newM)
				}
				return nil, fmt.Errorf("\"%s\" does not resolve to an object", placeholder[0])
			}
		}
		return nil, fmt.Errorf("\"%s\" does not exist in object", placeholder[0])
	}
	return nil, fmt.Errorf("invalid placeholder has zero length")
}
