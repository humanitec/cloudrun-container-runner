package utils

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestToSlug(t *testing.T) {
	t.Run("is slug no change", func(t *testing.T) {
		assert.Equal(t, "this-is-a-valid-slug", ToSlug("this-is-a-valid-slug"))
	})

	t.Run("ileading dash", func(t *testing.T) {
		assert.Equal(t, "this-is-a-valid-slug", ToSlug("-this-is-a-valid-slug"))
	})

	t.Run("has uppercase", func(t *testing.T) {
		assert.Equal(t, "this-is-a-valid-slug", ToSlug("This-Is-A-Valid-slug"))
	})

	t.Run("non alphanumeric", func(t *testing.T) {
		assert.Equal(t, "should-t-this-be-invalid", ToSlug("Should't this be invalid?"))
	})

	t.Run("runs of dashes", func(t *testing.T) {
		assert.Equal(t, "there-no", ToSlug("There---no----"))
	})

	t.Run("runs of dashes from invalid chars", func(t *testing.T) {
		assert.Equal(t, "there-no", ToSlug("There... ^&*(\"^)??no!!!"))
	})

	t.Run("rcannot make a valid slug", func(t *testing.T) {
		assert.Equal(t, "", ToSlug(""))
	})
}
