package main

import (
	"encoding/json"
)

func anyToString(val any) (string, error) {
	if str, ok := val.(string); ok {
		return str, nil
	}
	b, err := json.Marshal(val)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
