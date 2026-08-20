package utils

import (
	"encoding/json"
	"fmt"

	"sigs.k8s.io/yaml"
)

func MustAsMap(obj any) map[string]any {
	out, err := AsMap(obj)
	if err != nil {
		panic(err)
	}
	return out
}

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

func MustAsSlice(obj any) []any {
	out, err := AsSlice(obj)
	if err != nil {
		panic(err)
	}
	return out
}

func AsSlice(obj any) ([]any, error) {
	var objAsSlice []any
	objAsBytes, err := json.Marshal(obj)
	if err != nil {
		return nil, fmt.Errorf("unable to serialize inputs: %w", err)
	}
	err = json.Unmarshal(objAsBytes, &objAsSlice)
	if err != nil {
		return nil, fmt.Errorf("unable to deserialize inputs: %w", err)
	}
	return objAsSlice, err
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

func MustToYAMLString(o any) string {
	b, err := yaml.Marshal(o)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func MustToJSONString(o any) string {
	b, err := json.Marshal(o)
	if err != nil {
		panic(err)
	}
	return string(b)
}
