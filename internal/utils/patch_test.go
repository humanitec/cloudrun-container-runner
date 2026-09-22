package utils

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
)

func basePod() corev1.Pod {
	return corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name:  "main",
				Image: "busybox:latest",
				Env:   []corev1.EnvVar{{Name: "A", Value: "1"}},
			}},
		},
	}
}

func TestApplyPatch(t *testing.T) {
	t.Run("nil patch keeps the base", func(t *testing.T) {
		var patch map[string]any
		got, err := ApplyPatch(basePod(), patch)
		require.NoError(t, err)
		assert.Equal(t, basePod().Spec, got.Spec)
	})

	t.Run("empty patch keeps the base", func(t *testing.T) {
		got, err := ApplyPatch(basePod(), map[string]any{})
		require.NoError(t, err)
		assert.Equal(t, basePod().Spec, got.Spec)
	})

	t.Run("sets new fields", func(t *testing.T) {
		got, err := ApplyPatch(basePod(), map[string]any{
			"metadata": map[string]any{"labels": map[string]any{"team": "a"}},
			"spec":     map[string]any{"serviceAccountName": "sa"},
		})
		require.NoError(t, err)
		assert.Equal(t, map[string]string{"team": "a"}, got.Labels)
		assert.Equal(t, "sa", got.Spec.ServiceAccountName)
		assert.Equal(t, basePod().Spec.Containers, got.Spec.Containers)
	})

	t.Run("merges containers by name", func(t *testing.T) {
		got, err := ApplyPatch(basePod(), map[string]any{
			"spec": map[string]any{
				"containers": []any{map[string]any{
					"name": "main",
					"env":  []any{map[string]any{"name": "B", "value": "2"}},
				}},
			},
		})
		require.NoError(t, err)
		require.Len(t, got.Spec.Containers, 1)
		assert.Equal(t, "busybox:latest", got.Spec.Containers[0].Image)
		assert.ElementsMatch(t, []corev1.EnvVar{{Name: "A", Value: "1"}, {Name: "B", Value: "2"}}, got.Spec.Containers[0].Env)
	})

	t.Run("overrides a merged env var", func(t *testing.T) {
		got, err := ApplyPatch(basePod(), map[string]any{
			"spec": map[string]any{
				"containers": []any{map[string]any{
					"name": "main",
					"env":  []any{map[string]any{"name": "A", "value": "2"}},
				}},
			},
		})
		require.NoError(t, err)
		assert.Equal(t, []corev1.EnvVar{{Name: "A", Value: "2"}}, got.Spec.Containers[0].Env)
	})

	t.Run("adds a container", func(t *testing.T) {
		got, err := ApplyPatch(basePod(), map[string]any{
			"spec": map[string]any{
				"containers": []any{map[string]any{"name": "sidecar", "image": "envoy"}},
			},
		})
		require.NoError(t, err)
		require.Len(t, got.Spec.Containers, 2)
	})

	t.Run("deletes a container with a directive", func(t *testing.T) {
		got, err := ApplyPatch(basePod(), map[string]any{
			"spec": map[string]any{
				"containers": []any{map[string]any{"name": "main", "$patch": "delete"}},
			},
		})
		require.NoError(t, err)
		assert.Empty(t, got.Spec.Containers)
	})

	t.Run("removes a field set to null", func(t *testing.T) {
		pod := basePod()
		pod.Spec.ServiceAccountName = "sa"
		got, err := ApplyPatch(pod, map[string]any{"spec": map[string]any{"serviceAccountName": nil}})
		require.NoError(t, err)
		assert.Empty(t, got.Spec.ServiceAccountName)
	})

	t.Run("leaves the base untouched", func(t *testing.T) {
		pod := basePod()
		_, err := ApplyPatch(pod, map[string]any{"spec": map[string]any{"serviceAccountName": "sa"}})
		require.NoError(t, err)
		assert.Equal(t, basePod(), pod)
	})

	t.Run("patch of the wrong type", func(t *testing.T) {
		pod := basePod()
		got, err := ApplyPatch(pod, map[string]any{"spec": "not-an-object"})
		require.ErrorContains(t, err, "unable to unmarshal patched v1.Pod")
		assert.Equal(t, pod, got, "the base comes back on error")
	})

	t.Run("patch that cannot be marshalled", func(t *testing.T) {
		_, err := ApplyPatch(basePod(), map[string]any{"spec": func() {}})
		require.ErrorContains(t, err, "unable to marshal patch for v1.Pod")
	})
}
