package main

import (
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/humanitec/cloudrun-container-runner/internal/inputs"
)

var windowsDrivePrefix = regexp.MustCompile(`^[a-zA-Z]:[\\/]`)

func filesSafeRelativePaths(files map[string]string) (map[string]string, error) {
	newFiles := map[string]string{}
	for filePath, fileContent := range files {
		newFilePath, err := safeRelativePath(filePath)
		if err != nil {
			return nil, err
		}
		newFiles[newFilePath] = fileContent
	}
	return newFiles, nil
}

func safeRelativePath(p string) (string, error) {
	if strings.Contains(p, "\\") {
		return "", fmt.Errorf("\"%s\" contains \"\\\"", p)
	}
	cleanP := path.Clean(p)
	if strings.HasPrefix(cleanP, "/") || windowsDrivePrefix.MatchString(cleanP) {
		return "", fmt.Errorf("\"%s\" is not a relative path", cleanP)
	}
	// NOTE: path.Clean removes all inner /../ so we just need to check if the start is
	// a directory traversal.
	if strings.HasPrefix(cleanP, "../") {
		return "", fmt.Errorf("\"%s\" contains directory traversals", p)
	}
	return cleanP, nil
}

func secretAsURL(secret inputs.SecretInput) string {
	url := "secret://"
	if secret.Store == "" && secret.Value != nil {
		// TODO: Check if is string, if not - json encode
		return url + "direct" + "?value=" // + base64.StdEncoding.EncodeToString(secret.Value.([]byte))
	} else if secret.Store == "" {
		url += "unknown"
	} else {
		url += secret.Store
	}
	url += "/" + secret.Key
	if secret.Version != "" {
		url += "?version=" + secret.Version
	}
	return url
}

func uniqueOrderedSecrets(secrets []inputs.SecretInput) []inputs.SecretInput {
	if len(secrets) <= 1 {
		return secrets
	}
	slices.SortFunc(secrets, func(a, b inputs.SecretInput) int {
		return strings.Compare(secretAsURL(a), secretAsURL(b))
	})

	j := 0
	for i := 1; i < len(secrets); i++ {
		if secretAsURL(secrets[j]) == secretAsURL(secrets[i]) {
			continue
		}
		j++
		// preserve the original data
		// in[i], in[j] = in[j], in[i]
		// only set what is required
		secrets[j] = secrets[i]
	}
	return secrets[:j+1]
}
