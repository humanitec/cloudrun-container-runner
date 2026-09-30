package score

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
