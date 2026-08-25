package secretmanager

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

// illegalChars matches characters not allowed in a Google Secret Manager
// secret ID.
var illegalChars = regexp.MustCompile(`[^A-Za-z0-9_-]`)

const (
	// maxNameLen is the longest secret ID Google Secret Manager accepts.
	maxNameLen = 255
	hashLen    = 8
)

// SecretName builds a Google Secret Manager secret ID from a workload name, container name, kind and name
// of the variable. Secret Manager only accepts [A-Za-z0-9_-]{1,255}.
func SecretName(workloadName, containerName, kind, key string) string {
	parts := []string{workloadName, containerName, kind, key}
	h := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	suffix := hex.EncodeToString(h[:])[:hashLen]

	name := illegalChars.ReplaceAllString(strings.Join(parts, "_"), "_")
	if maxLen := maxNameLen - hashLen - 1; len(name) > maxLen {
		name = name[:maxLen]
	}
	return name + "_" + suffix
}
