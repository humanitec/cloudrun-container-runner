package utils

import (
	"encoding/json"
	"fmt"
)

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

func DecodeToString(in any) (string, error) {
	if str, ok := in.(string); ok {
		return str, nil
	} else {
		b, err := json.Marshal(in)
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
}
