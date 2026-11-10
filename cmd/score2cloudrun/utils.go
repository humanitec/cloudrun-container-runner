package main

import (
	"encoding/json"
	"fmt"
	"os"
)

func writeJSON(path string, v any) error {
	if path == "" {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func decodeViaJSON(in, out any) error {
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
