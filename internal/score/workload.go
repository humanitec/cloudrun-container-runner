// Package score provides Ternki specific functions for working with Score
// workloads.
//
// They do duplicate some functionality provided in
// github.com/score-spec/score-go, but this has been done to allow for easier
// integration into the Ternki system.
package score

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/humanitec/cloudrun-container-runner/internal/inputs"
	"github.com/score-spec/score-go/types"
)

var (
	placeholderMatch = regexp.MustCompile(`\$\{[^}]+\}`)
)

// allPlaceholdersInString returns all placeholders in the string.
// All plaecholders are returned, including duplicates.
func allPlaceholdersInString(s string) []string {
	// Escaping rule for $ is every pair of $$ should bcome a literal $
	// This means that $$${placeholder} should resolve to $<placeholder value>.
	// As we are only interested in the placeholder and not the rest of the
	// string, we can replace all $$ with another character and then look
	// for the placeholder.
	return placeholderMatch.FindAllString(strings.ReplaceAll(s, "$$", "_"), -1)
}

// allPlaceholdersIn returns all placeholders in the map or slice.
// All plaecholders are returned, including duplicates.
func allPlaceholdersIn(o any) []string {
	placeholders := []string{}
	if o == nil {
		return placeholders
	}
	switch v := o.(type) {
	case map[string]any:
		for _, val := range v {
			placeholders = append(placeholders, allPlaceholdersIn(val)...)
		}
	case []any:
		for _, val := range v {
			placeholders = append(placeholders, allPlaceholdersIn(val)...)
		}
	case types.ContainerVariables:
		for _, val := range v {
			placeholders = append(placeholders, allPlaceholdersIn(val)...)
		}
	case map[string]string:
		for _, val := range v {
			placeholders = append(placeholders, allPlaceholdersIn(val)...)
		}
	case string:
		placeholders = allPlaceholdersInString(v)
	}
	return placeholders
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

// GetAllPlaceholders returns all the unique placeholders in an untyped
// structure made up of either []any or map[string]any.
//
// Placeholders of the form: ${...} are returned excluding the surrounding
// ${}. $$ is escaped as a literal $ so placeholders of the form "$${x.y.z}"
// will not be returned.
func GetAllPlaceholders(obj any) []string {
	placeholders := []string{}
	placeholderMap := map[string]struct{}{}
	for _, p := range allPlaceholdersIn(obj) {
		if _, exists := placeholderMap[p]; !exists {
			placeholderMap[p] = struct{}{}
			placeholders = append(placeholders, p[2:len(p)-1])
		}
	}
	return placeholders
}

// ReplaceAllPlaceholdersInString returns a string containing expanded
// placeholders and escapped "$$"
//
// Placeholders are always replaces with a string. placeholderStrs contains the
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

// ReplaceAllPlaceholders returns a copy of an an untyped structure made up of
// either []any or map[string]any with any placeholders replaced.
//
// See ReplaceAllPlaceholdersInString for details of placeholderStrs and
// returned errors.
func ReplaceAllPlaceholders(obj any, placeholderStrs map[string]string) (any, error) {
	if obj == nil {
		return nil, nil
	}
	var err error
	switch v := obj.(type) {
	case map[string]any:
		out := map[string]any{}
		for key, val := range v {
			out[key], err = ReplaceAllPlaceholders(val, placeholderStrs)
			if err != nil {
				return nil, err
			}
		}
		return out, nil
	case []any:
		out := make([]any, len(v))
		for i, val := range v {
			out[i], err = ReplaceAllPlaceholders(val, placeholderStrs)
			if err != nil {
				return nil, err
			}
		}
		return out, nil
	case types.ContainerVariables, map[string]string:
		out := map[string]any{}
		for key, val := range v.(map[string]string) {
			out[key], err = ReplaceAllPlaceholdersInString(val, placeholderStrs)
			if err != nil {
				return nil, err
			}
		}
		return out, nil
	case string:
		return ReplaceAllPlaceholdersInString(v, placeholderStrs)
	default:
		return obj, nil
	}
}

// WorkloaResourceOutputsByName fetches the associated resource outputs
// Dprecated. Use OutputForPlaceholder instead.
func WorkloadResourceInputByName(workload types.Workload, params map[string]inputs.Input, scoreResName, workloadName string) (inputs.Input, error) {
	res, exists := workload.Resources[scoreResName]
	if !exists {
		return inputs.Input{}, fmt.Errorf("workload does not have a resource with name \"%s\"", scoreResName)
	}
	class := "default"
	if res.Class != nil && *res.Class != "" {
		class = *res.Class
	}
	resName := workloadName + ".resources." + scoreResName
	if res.Id != nil && *res.Id != "" {
		resName = *res.Id
	}
	resID := res.Type + "." + class + "#" + resName

	input, exists := params["resource::"+resID]
	if !exists {
		return inputs.Input{}, fmt.Errorf("internal error: cannot resolve resource inputs for %s in workload %s", resID, workloadName)
	}
	return input, nil
}

// ResolveResourceInputByPath returns the resource output based on path.
// loc is only used for error messages - should be set to resources.<resName>
// Dprecated. Use OutputForPlaceholder instead.
func ResolveResourceOutputByPath(input inputs.Input, path []string, loc string) (inputs.Input, error) {
	if len(path) == 0 {
		return input, nil
	}

	if input.Map != nil {
		if newInput, exists := input.Map[path[0]]; exists {
			return ResolveResourceOutputByPath(newInput, path[1:], loc+"."+path[0])
		}
		return inputs.Input{}, fmt.Errorf("cannot resolve path %s.%s in resource input", loc, path[0])
	}
	return inputs.Input{}, fmt.Errorf("cannot resolve path, expected %s to be a map", path[0])
}

type WorkloadResource struct {
	Workload *types.Workload
	Params   map[string]inputs.Input
	Name     string
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

// OutputForPlaceholder resolves the placeholder to either a value or secret.
//
// Any issues with resolution results in an error.
func (w *WorkloadResource) OutputForPlaceholder(placeholder, containerName string) (inputs.Input, error) {
	parts := strings.Split(placeholder, ".")
	if len(parts) < 2 {
		return inputs.Input{}, fmt.Errorf("invalid placeholder: must have at least 2 parts, got \"%s\"", placeholder)
	}
	switch parts[0] {
	case "resources":
		input, err := WorkloadResourceInputByName(*w.Workload, w.Params, parts[1], w.Name)
		if err != nil {
			return inputs.Input{}, err
		}
		return ResolveResourceOutputByPath(input, parts[2:], strings.Join(parts[:2], "."))
	case "metadata":
		val, err := placeholderResolveInMap(parts[1:], w.Workload.Metadata)
		if err != nil {
			return inputs.Input{}, fmt.Errorf("resolving placeholder \"%s\": %w", placeholder, err)
		}
		return inputs.Input{Value: val}, nil
	case "container":
		if container, exists := w.Workload.Containers[containerName]; exists {
			if parts[1] == "image" {
				return inputs.Input{Value: container.Image}, nil
			}
			return inputs.Input{}, fmt.Errorf("resolving placeholder \"%s\": container can only reference image", placeholder)
		}
		return inputs.Input{}, fmt.Errorf("resolving placeholder \"%s\": container %s not found", placeholder, containerName)
	default:
		return inputs.Input{}, fmt.Errorf("invalid placeholder: must start with \"resources\", \"container\" or \"metadata\", got \"%s\"", placeholder)
	}
}

// ExpandFile returns the a inputs.Input that can be used as the value of
// the file
//
// Currnetly, scecret templating is not supported except where raw secrets are
// used, so in those cases, unless the secret is on its own in the file, an
// error will be returned.
func (w *WorkloadResource) ExpandFile(file types.ContainerFile, containerName string) (inputs.Input, error) {
	if file.Content == nil {
		return inputs.Input{}, fmt.Errorf("content missing")
	}
	if file.NoExpand != nil && *file.NoExpand {
		return inputs.Input{Value: *file.Content}, nil
	}
	placeholders := GetAllPlaceholders(*file.Content)
	placeholderStrs := map[string]string{}
	isOutputSecret := false
	for _, placeholder := range placeholders {
		output, err := w.OutputForPlaceholder(placeholder, containerName)
		if err != nil {
			return inputs.Input{}, err
		}
		if output.Secret != nil {
			if output.Secret.Value != nil {
				if str, ok := output.Secret.Value.(string); ok {
					placeholderStrs[placeholder] = str
				} else {
					b, err := json.Marshal(output.Value)
					if err != nil {
						return inputs.Input{}, fmt.Errorf("resolving placeholder ${%s}: %w", placeholder, err)
					}
					placeholderStrs[placeholder] = string(b)
				}
				isOutputSecret = true
			} else {
				// We don't yet support startings that contain more than one secret in files unless the secret value is direct
				if len(placeholders) == 1 && *file.Content == fmt.Sprintf("${%s}", placeholder) {
					return output, nil
				} else {
					return inputs.Input{}, fmt.Errorf("resolving placeholder ${%s}: secret of store type %s can only exist on its own in a file", placeholder, output.Secret.Store)
				}
			}
		} else if output.Value != nil {
			if str, ok := output.Value.(string); ok {
				placeholderStrs[placeholder] = str
			} else {
				b, err := json.Marshal(output.Value)
				if err != nil {
					return inputs.Input{}, fmt.Errorf("resolving placeholder ${%s}: %w", placeholder, err)
				}
				placeholderStrs[placeholder] = string(b)
			}
		}
	}
	expandedFile, err := ReplaceAllPlaceholdersInString(*file.Content, placeholderStrs)
	if err != nil {
		return inputs.Input{}, err
	}
	if isOutputSecret {
		return inputs.Input{Secret: &inputs.SecretInput{Value: expandedFile}}, nil
	}
	return inputs.Input{Value: expandedFile}, nil
}
