package utils

import (
	"math/rand/v2"
	"regexp"
	"strings"
)

// const idLength = 24 // 24 characters is an 120bit id

const SlugRegexPattern = "[a-z0-9](?:-?[a-z0-9]+)*"

var slugRegex = regexp.MustCompile("^" + SlugRegexPattern + "$")

const uuidRegexPattern = "[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}"

var uuidRegex = regexp.MustCompile("^" + uuidRegexPattern + "$")

const hexStringPattern = "[0-9a-f]+"

var hexStringRegex = regexp.MustCompile("^" + hexStringPattern + "$")

var illegalSlugChars = regexp.MustCompile(`[^0-9a-z-]`)
var runsOfDashes = regexp.MustCompile(`--+`)

// https://git-scm.com/docs/git-check-ref-format
//
// - Must start with refs/
//
// - Must contain at least 2 /
//
// -
// We don't check for a segment of just "@" the pattern "@{" in a segment
const gitRefPattern = `refs(/[^.~:\\?*\[^]+(\.[^.~:\\?*\[^]+)*)+`

var gitRefRegex = regexp.MustCompile("^" + gitRefPattern + "$")

const (
	alpha        = "abcdefghijklmnopqrstuvwxyz"
	alphanumeric = "0123456789" + alpha
	idLength     = 24
)

// GenerateID creates a 24 character ID that is extremely likely to be unique
// It is guaranteed to start or end with a letter and be lowercase
// alphanumeric. It has an entropy of about 123bits which puts it on par with
// a UUID while also having some other useful properties.
//
// It is explicitly intended to be easy to use as follows:
//
// - As a C Identifier
//
// - As part of a DNS label (both RFC 1123 & RFC 1035)
//
//   - Easily shortened by using substr while still being able to guarantee it
//     being a valid C Identifier and DNS label.
func GenerateID() string {
	idBytes := make([]byte, idLength)
	idBytes[0] = alpha[rand.IntN(len(alpha))]
	for i := 1; i < idLength-1; i++ {
		idBytes[i] = alphanumeric[rand.IntN(len(alphanumeric))]
	}
	idBytes[idLength-1] = alpha[rand.IntN(len(alpha))]
	return string(idBytes)
}

// IsValidSlug checks whether the supplied string is a slug.
//
// A slug:
//
// - only has alphanumeric lowercase values an "-"
//
// - does not start or end with a "-"
//
// - does not have two or more "-" in sequence.
func IsValidSlug(slug string) bool {
	return slugRegex.MatchString(slug)
}

// ToSlug converts any string into a slug.
//
// Process:
//
// - all uppercase -> lowercase
//
// - all non alphanumeric converted to "-"
//
// - runs of "-"" replaced with a single dash
//
// - leading and trailing dashes removed
//
// NOTE: if a stringc cannot be turned into a slug, an empty string is
// returned.
func ToSlug(s string) string {
	return strings.Trim(runsOfDashes.ReplaceAllString(illegalSlugChars.ReplaceAllString(strings.ToLower(s), "-"), "-"), "-")
}

// SliceToJSONPointer converts a slice of strings into a JSON Pointer (RFC6901)
//
// - a zero length slice represents a JSONPointer of ""
// - a slice containing a single empty string represents a JSON Pointer of "/"
func SliceToJSONPointer(s []string) string {
	parts := make([]string, len(s)+1)
	for i, part := range s {
		part = strings.ReplaceAll(part, "~", "~0")
		part = strings.ReplaceAll(part, "/", "~1")
		parts[i+1] = part
	}
	return strings.Join(parts, "/")
}

// JSONPointerToSlice converts a JSON Pointer into a slice of strings (RFC6901)
//
// In general, this supports a slight superset of JSON Pointers (RFC-6901). The
// superset is:
//
// - The leading "/" can be omitted and will be automatically inserted except:
//
//   - when the empty string is used - then it refers to the whole document. To
//     reference a JSON object key withe value "" use "/"
//
// Edge cases:
//
// - "" returns an empty slice
//
// - "/" returns a slice of length one containing an empty string
func JSONPointerToSlice(p string) []string {
	parts := strings.Split(strings.TrimPrefix(p, "/"), "/")
	for i, part := range parts {
		part = strings.ReplaceAll(part, "~1", "/")
		part = strings.ReplaceAll(part, "~0", "~")
		parts[i] = part
	}
	return parts
}

// IsUUID returns true if the ID looks like a UUID
func IsUUID(id string) bool {
	return uuidRegex.MatchString(id)
}

func IsLowercaseHexString(s string) bool {
	return hexStringRegex.MatchString(s)
}

func IsGitRef(s string) bool {
	return gitRefRegex.MatchString(s)
}
