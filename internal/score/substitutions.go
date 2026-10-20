package score

import (
	"fmt"
	"strings"
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

// ResolvePlaceholder resolves a placeholder expression to a SubValue object from the substitutions map.
//
// A placeholder expression is surrounded by ${...}, and can contain two or more parts, separated with a dot.
// Generator supports only placeholders starting with "resources". Other types of placeholders should be
// replaced when the spec is normalized
func ResolvePlaceholder(placeholder string, substitutions map[string]SubValue) (*SubValue, error) {
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
	default:
		return nil, fmt.Errorf("invalid placeholder: must start with \"resources\", got \"%s\"", placeholder)
	}
}
