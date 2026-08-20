// Package inputs describes the values a driver receives for a Score workload's
// resources: plain values, secret references, and the nested structures that
// hold them.
package inputs

// ValueInput describes a value that can be any valid JSON type
type ValueInput any

// SecretInput describes a value should be handled as sensitive.
// It can be any valid JSON type.
type SecretInput struct {
	// Store is the ID of the Secret Store already registered.
	Store string `json:"store,omitempty"`

	// Key is the key to look up in the secret store.
	Key string `json:"key,omitempty"`

	// Version is the version of the secret in the secret store.
	Version string `json:"version,omitempty"`

	// Path is the path the the value to resolve to if the secret is a map.
	// Default value is "." inidcating the complete value at the key.
	Path string `json:"path,omitempty"`

	// Value holds a secret value. Must be nil if any other propety is set
	Value any `json:"value,omitempty"`
}

type Input struct {
	Value  ValueInput   `json:"value,omitempty"`
	Secret *SecretInput `json:"secret,omitempty"`

	// Allows for nesting of Inputs.
	Map map[string]Input `json:"map,omitzero"`
}
