package utils

import (
	"encoding/json"
	"fmt"
)

func AsMap(obj any) (map[string]any, error) {
	var objAsMap map[string]any
	objAsBytes, err := json.Marshal(obj)
	if err != nil {
		return nil, fmt.Errorf("unable to serialize inputs: %w", err)
	}
	err = json.Unmarshal(objAsBytes, &objAsMap)
	if err != nil {
		return nil, fmt.Errorf("unable to deserialize inputs: %w", err)
	}
	return objAsMap, err
}

func DecodeViaJSON(in, out any) error {
	objAsBytes, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("unable to serialize input to json: %w", err)
	}
	err = json.Unmarshal(objAsBytes, out)
	if err != nil {
		return fmt.Errorf("unable to deserialize output from json: %w", err)
	}
	return err
}

func ToPtr[T any](t T) *T {
	return &t
}
