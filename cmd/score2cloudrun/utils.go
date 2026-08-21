package main

import (
	"slices"
	"strings"

	"github.com/humanitec/cloudrun-container-runner/internal/inputs"
)

func secretAsURL(secret inputs.SecretInput) string {
	url := "secret://"
	switch {
	case secret.Store == "" && secret.Value != nil:
		// TODO: Check if is string, if not - json encode
		return url + "direct" + "?value=" // + base64.StdEncoding.EncodeToString(secret.Value.([]byte))
	case secret.Store == "":
		url += "unknown"
	default:
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
