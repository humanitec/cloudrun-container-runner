package utils

import (
	"regexp"
	"strings"
)

const SlugRegexPattern = "[a-z0-9](?:-?[a-z0-9]+)*"

var slugRegex = regexp.MustCompile("^" + SlugRegexPattern + "$")

var illegalSlugChars = regexp.MustCompile(`[^0-9a-z-]`)
var runsOfDashes = regexp.MustCompile(`--+`)

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
