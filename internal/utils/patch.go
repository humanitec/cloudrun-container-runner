package utils

import (
	"encoding/json"
	"fmt"

	"k8s.io/apimachinery/pkg/util/strategicpatch"
)

// ApplyPatch patches the base object with the patch and returns the merged object.
func ApplyPatch[T any](base T, patch any) (T, error) {
	baseJSON, err := json.Marshal(base)
	if err != nil {
		return base, fmt.Errorf("unable to marshal %T: %w", base, err)
	}
	patchJSON, err := json.Marshal(patch)
	if err != nil {
		return base, fmt.Errorf("unable to marshal patch for %T: %w", base, err)
	}
	mergedJSON, err := strategicpatch.StrategicMergePatch(baseJSON, patchJSON, base)
	if err != nil {
		return base, fmt.Errorf("unable to patch %T: %w", base, err)
	}
	var merged T
	if err = json.Unmarshal(mergedJSON, &merged); err != nil {
		return base, fmt.Errorf("unable to unmarshal patched %T: %w", base, err)
	}
	return merged, nil
}
