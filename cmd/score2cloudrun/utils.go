package main

import (
	"encoding/json"
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
