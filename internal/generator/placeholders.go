package generator

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	placeholderMatch = regexp.MustCompile(`\$\{[^}]+\}`)
)

// allPlaceholdersInString returns all placeholders in the string.
// All placeholders are returned, including duplicates.
func allPlaceholdersInString(s string) []string {
	// Escaping rule for $ is every pair of $$ should bcome a literal $
	// This means that $$${placeholder} should resolve to $<placeholder value>.
	// As we are only interested in the placeholder and not the rest of the
	// string, we can replace all $$ with another character and then look
	// for the placeholder.
	return placeholderMatch.FindAllString(strings.ReplaceAll(s, "$$", "_"), -1)
}

// GetAllPlaceholdersInString returns all the unique placeholders in a string
//
// Placeholders of the form: ${...} are returned excluding the surrounding
// ${}. $$ is escaped as a literal $ so placeholders of the form "$${x.y.z}"
// will not be returned.
func GetAllPlaceholdersInString(s string) []string {
	placeholders := []string{}
	placeholderMap := map[string]struct{}{}
	for _, p := range allPlaceholdersInString(s) {
		if _, exists := placeholderMap[p]; !exists {
			placeholderMap[p] = struct{}{}
			placeholders = append(placeholders, p[2:len(p)-1])
		}
	}
	return placeholders
}

// ReplaceAllPlaceholdersInString returns a string containing expanded
// placeholders and escaped "$$"
//
// Placeholders are always replaced with a string. placeholderStrs contains the
// strings that a placeholder should be replaced with.
//
// An error is returned for incomplete placeholders or placeholders not present
// in placeholderStrs.
func ReplaceAllPlaceholdersInString(str string, placeholderStrs map[string]string) (string, error) {
	s := strings.Builder{}
	var prevRune rune
	placeholderRunes := []rune{}
	inPlaceholder := false
	for _, r := range str {
		if inPlaceholder {
			if r == '}' {
				inPlaceholder = false
				if value, exists := placeholderStrs[string(placeholderRunes)]; exists {
					s.WriteString(value)
				} else {
					return "", fmt.Errorf("cannot replace placeholder ${%s}: not found", string(placeholderRunes))
				}
				placeholderRunes = []rune{}
			} else {
				placeholderRunes = append(placeholderRunes, r)
			}
		} else {
			switch r {
			case '$':
				if prevRune == '$' {
					s.WriteRune('$')
					prevRune = 0
					// reset prefRune, use continue to avoid final prevRune
					continue
				}
			case '{':
				if prevRune == '$' {
					inPlaceholder = true
				} else {
					s.WriteRune(r)
				}
			default:
				s.WriteRune(r)
			}
		}
		prevRune = r
	}
	if inPlaceholder {
		return "", fmt.Errorf("incomplete placeholder \"%s\"", string(placeholderRunes))
	}
	return s.String(), nil
}
