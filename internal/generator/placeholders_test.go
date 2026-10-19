package generator

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetAllPlaceholdersInString(t *testing.T) {
	testCases := []struct {
		name     string
		str      string
		expected []string
	}{
		{
			name:     "no placeholders",
			str:      "hello world",
			expected: []string{},
		},
		{
			name: "one placeholder whole string",
			str:  "${resources.bucket.name}",
			expected: []string{
				"resources.bucket.name",
			},
		},
		{
			name: "2 placeholders",
			str:  "${resources.bucket.name}${resources.db.host}",
			expected: []string{
				"resources.bucket.name",
				"resources.db.host",
			},
		},
		{
			name: "2 placeholders big string",
			str:  "Hello ${resources.bucket.name} world ${resources.db.host}!",
			expected: []string{
				"resources.bucket.name",
				"resources.db.host",
			},
		},
		{
			name:     "no placeholders, escaped",
			str:      "Hello $${resources.bucket.name} world $${resources.db.host}!",
			expected: []string{},
		},

		{
			name: "repleasted placeholders",
			str:  "Hello ${resources.bucket.name} world ${resources.db.host}! I said ${resources.bucket.name}!",
			expected: []string{
				"resources.bucket.name",
				"resources.db.host",
			},
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			actual := GetAllPlaceholdersInString(testCase.str)
			assert.ElementsMatch(t, testCase.expected, actual)
		})
	}
}

func TestReplaceAllPlaceholdersInString(t *testing.T) {
	placeholderStr := map[string]string{
		"resources.bucket.name": "nameOfBucket",
		"resources.db.host":     "hostOfDatabase",
	}
	testCases := []struct {
		name     string
		str      string
		expected string
	}{
		{
			name:     "no placeholders",
			str:      "hello world",
			expected: "hello world",
		},
		{
			name:     "one placeholder whole string",
			str:      "${resources.bucket.name}",
			expected: "nameOfBucket",
		},
		{
			name:     "2 placeholders",
			str:      "${resources.bucket.name}${resources.db.host}",
			expected: "nameOfBuckethostOfDatabase",
		},
		{
			name:     "2 placeholders big string",
			str:      "Hello ${resources.bucket.name} world ${resources.db.host}!",
			expected: "Hello nameOfBucket world hostOfDatabase!",
		},
		{
			name:     "no placeholders, escaped",
			str:      "Hello $${resources.bucket.name} world $${resources.db.host}!",
			expected: "Hello ${resources.bucket.name} world ${resources.db.host}!",
		},

		{
			name:     "incremental escaping",
			str:      "Hello ${resources.bucket.name} world $${resources.bucket.name} cheese $$${resources.bucket.name} and $${${resources.bucket.name}}",
			expected: "Hello nameOfBucket world ${resources.bucket.name} cheese $nameOfBucket and ${nameOfBucket}",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			actual, err := ReplaceAllPlaceholdersInString(testCase.str, placeholderStr)
			require.NoError(t, err)
			assert.Equal(t, testCase.expected, actual)
		})
	}
}
func TestReplaceAllPlaceholdersInString_Error(t *testing.T) {
	placeholderStr := map[string]string{
		"resources.bucket.name": "nameOfBucket",
		"resources.db.host":     "hostOfDatabase",
	}
	testCases := []struct {
		name string
		str  string
	}{
		{
			name: "one placeholder whole string",
			str:  "${resources.missing.name}",
		},
		{
			name: "2 placeholders, one missing",
			str:  "${resources.bucket.name}${resources.missing.host}",
		},
		{
			name: "incomplete placeholder whole string",
			str:  "${resources.missing.name",
		},
		{
			name: "incomplete placeholder middle of string",
			str:  "Hello ${resources.bucket.name world!",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := ReplaceAllPlaceholdersInString(testCase.str, placeholderStr)
			assert.Error(t, err)
		})
	}
}
