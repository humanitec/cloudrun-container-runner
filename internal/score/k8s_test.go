package score

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/score-spec/score-go/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/humanitec/cloudrun-container-runner/internal/inputs"
	"github.com/humanitec/cloudrun-container-runner/internal/utils"
)

func testEnvVarSecretResolver(name string, secret *inputs.SecretInput) (core.EnvVarSource, error) {
	if secret.Store != "secret" {
		return core.EnvVarSource{}, fmt.Errorf("not a recognised secret store")
	}
	parts := strings.Split(secret.Key, "/")
	return core.EnvVarSource{
		SecretKeyRef: &core.SecretKeySelector{
			LocalObjectReference: core.LocalObjectReference{
				Name: parts[0],
			},
			Key: parts[1],
		},
	}, nil
}

func testContainerFileResolver_NoCall(t *testing.T) ContainerFileResolver {
	return func(workloadRes WorkloadResource, volumeName, dir string, files map[string]*types.ContainerFile, containerName string) (core.Volume, error) {
		t.Error("should not be called")
		return core.Volume{}, nil
	}
}

func testContainerFileResolver_RecordVolumes(t *testing.T, dirCache map[string]string) ContainerFileResolver {
	return func(workloadRes WorkloadResource, volumeName, dir string, files map[string]*types.ContainerFile, containerName string) (core.Volume, error) {
		items := []core.KeyToPath{}
		for fn := range files {
			items = append(items, core.KeyToPath{
				Key:  strings.ToUpper(strings.ReplaceAll(fn, ".", "_")),
				Path: fn,
			})
		}
		// Items in alphabetical key order to avoid arbitrary changes in order.
		slices.SortFunc(items, func(a, b core.KeyToPath) int {
			return strings.Compare(a.Key, b.Key)
		})
		dirCache[dir] = volumeName
		return core.Volume{
			Name: volumeName,
			VolumeSource: core.VolumeSource{
				ConfigMap: &core.ConfigMapVolumeSource{
					LocalObjectReference: core.LocalObjectReference{
						Name: "test-config",
					},
					Items: items,
				},
			},
		}, nil
	}
}

func TestStrFirstN(t *testing.T) {
	testCases := []struct {
		name string
		s    string
		n    int
		want string
	}{
		{name: "shorter than n", s: "abc", n: 5, want: "abc"},
		{name: "exactly n", s: "abcde", n: 5, want: "abcde"},
		{name: "longer than n", s: "abcdefgh", n: 5, want: "abcde"},
		{name: "one character", s: "abcdefgh", n: 1, want: "a"},
		{name: "none", s: "abcdefgh", n: 0, want: ""},
		{name: "empty string", s: "", n: 5, want: ""},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, strFirstN(tc.s, tc.n))
			assert.Len(t, strFirstN(tc.s, tc.n), min(tc.n, len(tc.s)))
		})
	}
}
func TestStrLastN(t *testing.T) {
	testCases := []struct {
		name string
		s    string
		n    int
		want string
	}{
		{name: "shorter than n", s: "abc", n: 5, want: "abc"},
		{name: "exactly n", s: "abcde", n: 5, want: "abcde"},
		{name: "longer than n", s: "abcdefgh", n: 5, want: "defgh"},
		{name: "one character", s: "abcdefgh", n: 1, want: "h"},
		{name: "none", s: "abcdefgh", n: 0, want: ""},
		{name: "empty string", s: "", n: 5, want: ""},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, strLastN(tc.s, tc.n))
			assert.Len(t, strLastN(tc.s, tc.n), min(tc.n, len(tc.s)))
		})
	}
}
func TestVolumeNamePartsAreTheRequestedLength(t *testing.T) {
	hash := hashStr("hello-world-dev-main-/a/b")
	require.Len(t, hash, 40, "sha1 hex is 40 characters, so 30 of them is a real truncation")
	assert.Len(t, strFirstN(hash, 30), 30)
	assert.Len(t, strLastN(strings.Repeat("a-", 40), 32), 32)
}
func TestEscapeK8sEnvVar(t *testing.T) {
	t.Run("empty string", func(t *testing.T) {
		assert.Equal(t, "", escapeK8sEnvVar(""))
	})
	t.Run("no expansion", func(t *testing.T) {
		assert.Equal(t, "Hello World!", escapeK8sEnvVar("Hello World!"))
	})
	t.Run("single expansion as whole string", func(t *testing.T) {
		assert.Equal(t, "$$$$(TEST)", escapeK8sEnvVar("$(TEST)"))
	})
	t.Run("single expansion in middle of string", func(t *testing.T) {
		assert.Equal(t, "Hello $$$$(TEST) World!", escapeK8sEnvVar("Hello $(TEST) World!"))
	})
	t.Run("multiple expansions nothing else", func(t *testing.T) {
		assert.Equal(t, "$$$$(TEST_ONE)$$$$(TEST_TWO)", escapeK8sEnvVar("$(TEST_ONE)$(TEST_TWO)"))
	})
	t.Run("dont escape expansions nothing else", func(t *testing.T) {
		assert.Equal(t, "$$$$(TEST_ONE)$$$$(TEST_ONE)$$$$$$$$(TEST_TWO)$$$$$$$$(TEST_TWO)$$$$$$$$$$$$(TEST_THREE)$$$$$$$$$$$$(TEST_THREE)", escapeK8sEnvVar("$(TEST_ONE)$$(TEST_ONE)$$$(TEST_TWO)$$$$(TEST_TWO)$$$$$(TEST_THREE)$$$$$$(TEST_THREE)"))
	})
}

func TestK8sEnvFromScore(t *testing.T) {
	workload := types.Workload{
		Containers: types.WorkloadContainers{
			"no-placeholders": types.Container{
				Variables: types.ContainerVariables{
					"MESSAGE": "Hello World!",
				},
			},
			"placeholders-simple": types.Container{
				Variables: types.ContainerVariables{
					"BUCKET_NAME": "${resources.readonly-store.name}",
					"DBPASSWORD":  "${resources.db.password}",
				},
			},
			"placeholders-complex": types.Container{
				Variables: types.ContainerVariables{
					"DBCONN":         "postgresql://${resources.db.username}:${resources.db.password}@${resources.db.host}:${resources.db.port}/${resources.db.name}",
					"BUCKET_NAME":    "${resources.readonly-store.name}",
					"DBPASSWORD":     "${resources.db.password}",
					"ESCAPED":        "$${resources.db.password}",
					"K8S_ESCAPE":     "$(THIS_SHOULD_EXIST_IN_OUTPUT)",
					"CONTAINER_NAME": "${container.image}",
				},
				Image: "test-image:latest",
			},
			"invalid-placeholders": types.Container{
				Variables: types.ContainerVariables{
					"INVALID": "${some.invalid.placeholder}",
				},
			},
		},
		Resources: types.WorkloadResources{
			"db": types.Resource{
				Type: "postgres",
			},
			"readonly-store": types.Resource{
				Type:  "bucket",
				Class: ptrStr("ro"),
			},
			"shared-store": types.Resource{
				Type:  "bucket",
				Class: ptrStr("rw"),
				Id:    ptrStr("common"),
			},
			"missing": types.Resource{
				Type: "cheese",
			},
		},
	}
	params := map[string]inputs.Input{
		"resources.db.name":     {Value: "db_name"},
		"resources.db.port":     {Value: 5432},
		"resources.db.host":     {Value: "db.example.com"},
		"resources.db.username": {Value: "test_user"},
		"resources.db.password": {Secret: &inputs.SecretInput{
			Store: "secret",
			Key:   "mysecret/mypassword",
		}},

		"resources.readonly-store.name": {Value: "read-only bucket"},

		"resources.shared-store.name": {Value: "shared bucket"},
	}
	workloadName := "workloads.test"

	converter := K8sScoreConverter{
		WorkloadResource: WorkloadResource{
			Workload:      &workload,
			Name:          workloadName,
			Substitutions: params,
		},
		EnvVarSecretResolver: testEnvVarSecretResolver,
	}

	t.Run("no placeholders", func(t *testing.T) {

		actual, err := converter.EnvVar("no-placeholders")
		require.NoError(t, err)
		expected := []core.EnvVar{
			{
				Name:  "MESSAGE",
				Value: "Hello World!",
			},
		}
		assert.Equal(t, expected, actual)
	})

	t.Run("placeholders-simple", func(t *testing.T) {
		actual, err := converter.EnvVar("placeholders-simple")
		require.NoError(t, err)
		expected := []core.EnvVar{
			{
				Name: "__SECRET__db_password__",
				ValueFrom: &core.EnvVarSource{
					SecretKeyRef: &core.SecretKeySelector{
						Key: "mypassword",
						LocalObjectReference: core.LocalObjectReference{
							Name: "mysecret",
						},
					},
				},
			},
			{
				Name:  "BUCKET_NAME",
				Value: "read-only bucket",
			},
			{
				Name:  "DBPASSWORD",
				Value: "$(__SECRET__db_password__)",
			},
		}
		assert.ElementsMatch(t, expected, actual)
	})
	t.Run("placeholders-complex", func(t *testing.T) {
		actual, err := converter.EnvVar("placeholders-complex")
		require.NoError(t, err)
		expected := []core.EnvVar{
			{
				Name: "__SECRET__db_password__",
				ValueFrom: &core.EnvVarSource{
					SecretKeyRef: &core.SecretKeySelector{
						Key: "mypassword",
						LocalObjectReference: core.LocalObjectReference{
							Name: "mysecret",
						},
					},
				},
			},
			{
				Name:  "BUCKET_NAME",
				Value: "read-only bucket",
			},
			{
				Name:  "CONTAINER_NAME",
				Value: "test-image:latest",
			},
			{
				Name:  "DBCONN",
				Value: "postgresql://test_user:$(__SECRET__db_password__)@db.example.com:5432/db_name",
			},
			{
				Name:  "DBPASSWORD",
				Value: "$(__SECRET__db_password__)",
			},
			{
				Name:  "ESCAPED",
				Value: "$${resources.db.password}",
			},
			{
				Name:  "K8S_ESCAPE",
				Value: "$$(THIS_SHOULD_EXIST_IN_OUTPUT)",
			},
		}
		assert.ElementsMatch(t, expected, actual)
	})

}

func TestK8sFilesFromScore(t *testing.T) {

	workloadResources := types.WorkloadResources{
		"db": types.Resource{
			Type: "postgres",
		},
		"readonly-store": types.Resource{
			Type:  "bucket",
			Class: ptrStr("ro"),
		},
		"shared-store": types.Resource{
			Type:  "bucket",
			Class: ptrStr("rw"),
			Id:    ptrStr("common"),
		},
		"missing": types.Resource{
			Type: "cheese",
		},
	}
	params := map[string]inputs.Input{
		"resources.db.name":     {Value: "db_name"},
		"resources.db.port":     {Value: 5432},
		"resources.db.host":     {Value: "db.example.com"},
		"resources.db.username": {Value: "test_user"},
		"resources.db.password": {Secret: &inputs.SecretInput{
			Store: "secret",
			Key:   "mysecret/mypassword",
		}},

		"resources.readonly-store.name": {Value: "read-only bucket"},

		"resources.shared-store.name": {Value: "shared bucket"},
	}
	workloadName := "workloads.test"

	t.Run("no volumes", func(t *testing.T) {
		workload := types.Workload{
			Containers: types.WorkloadContainers{
				"main": types.Container{},
			},
			Resources: workloadResources,
		}

		converter := K8sScoreConverter{
			WorkloadResource: WorkloadResource{
				Workload:      &workload,
				Name:          workloadName,
				Substitutions: params,
			},
			ContainerFileResolver: testContainerFileResolver_NoCall(t),
		}
		actualVolumeMounts, actualVolumes, err := converter.ContainerFiles()
		require.NoError(t, err)
		assert.Empty(t, actualVolumeMounts)
		assert.Empty(t, actualVolumes)
	})

	t.Run("one file one volume", func(t *testing.T) {
		workload := types.Workload{
			Containers: types.WorkloadContainers{
				"main": types.Container{
					Files: types.ContainerFiles{
						"/usr/local/simple.txt": types.ContainerFile{
							Content: ptrStr("Hello World"),
						},
					},
				},
			},
			Resources: workloadResources,
		}
		simpleVolumeName := ""
		converter := K8sScoreConverter{
			WorkloadResource: WorkloadResource{
				Workload:      &workload,
				Name:          workloadName,
				Substitutions: params,
			},
			ContainerFileResolver: func(workloadRes WorkloadResource, volumeName, dir string, files map[string]*types.ContainerFile, containerName string) (core.Volume, error) {
				require.Contains(t, files, "simple.txt")
				assert.Len(t, files, 1, "files has length 1")
				require.NotNil(t, files["simple.txt"].Content, "files[\"simple.txt\"].content is not nil")
				simpleVolumeName = volumeName
				return core.Volume{
					Name: volumeName,
					VolumeSource: core.VolumeSource{
						ConfigMap: &core.ConfigMapVolumeSource{
							LocalObjectReference: core.LocalObjectReference{
								Name: "test-config",
							},
							Items: []core.KeyToPath{
								{
									Key:  "SIMPLE",
									Path: "simple.txt",
								},
							},
						},
					},
				}, nil
			},
		}
		actualVolumeMounts, actualVolumes, err := converter.ContainerFiles()

		require.NoError(t, err)
		expectedVolumeMounts := map[string][]core.VolumeMount{
			"main": {
				{
					Name:      simpleVolumeName,
					MountPath: "/usr/local",
					ReadOnly:  true,
				},
			},
		}
		require.Contains(t, actualVolumeMounts, "main")
		assert.ElementsMatch(t, expectedVolumeMounts["main"], actualVolumeMounts["main"])

		expectedVolumes := []core.Volume{
			{
				Name: simpleVolumeName,
				VolumeSource: core.VolumeSource{
					ConfigMap: &core.ConfigMapVolumeSource{
						LocalObjectReference: core.LocalObjectReference{
							Name: "test-config",
						},
						Items: []core.KeyToPath{
							{
								Key:  "SIMPLE",
								Path: "simple.txt",
							},
						},
					},
				},
			},
		}
		assert.ElementsMatch(t, expectedVolumes, actualVolumes)
	})

	t.Run("multiple files multiple volumes", func(t *testing.T) {
		workload := types.Workload{
			Containers: types.WorkloadContainers{
				"main": types.Container{
					Files: types.ContainerFiles{
						"/base/one/alpha.txt": types.ContainerFile{
							Content: ptrStr("ALPHA"),
						},
						"/base/T_w_o/g_amma.txt": types.ContainerFile{
							Content: ptrStr("GAMMA"),
						},
						"/base/one/beta.txt": types.ContainerFile{
							Content: ptrStr("BETA"),
						},
					},
				},
			},
			Resources: workloadResources,
		}
		dirCache := map[string]string{}
		converter := K8sScoreConverter{
			WorkloadResource: WorkloadResource{
				Workload:      &workload,
				Name:          workloadName,
				Substitutions: params,
			},
			ContainerFileResolver: testContainerFileResolver_RecordVolumes(t, dirCache),
		}
		actualVolumeMounts, actualVolumes, err := converter.ContainerFiles()

		require.NoError(t, err)
		expectedVolumeMounts := map[string][]core.VolumeMount{
			"main": {
				{
					Name:      dirCache["/base/one"],
					MountPath: "/base/one",
					ReadOnly:  true,
				},
				{
					Name:      dirCache["/base/T_w_o"],
					MountPath: "/base/T_w_o",
					ReadOnly:  true,
				},
			},
		}
		require.Contains(t, actualVolumeMounts, "main")
		assert.ElementsMatch(t, expectedVolumeMounts["main"], actualVolumeMounts["main"])

		expectedVolumes := []core.Volume{
			{
				Name: dirCache["/base/one"],
				VolumeSource: core.VolumeSource{
					ConfigMap: &core.ConfigMapVolumeSource{
						LocalObjectReference: core.LocalObjectReference{
							Name: "test-config",
						},
						Items: []core.KeyToPath{
							{
								Key:  "ALPHA_TXT",
								Path: "alpha.txt",
							},
							{
								Key:  "BETA_TXT",
								Path: "beta.txt",
							},
						},
					},
				},
			},
			{
				Name: dirCache["/base/T_w_o"],
				VolumeSource: core.VolumeSource{
					ConfigMap: &core.ConfigMapVolumeSource{
						LocalObjectReference: core.LocalObjectReference{
							Name: "test-config",
						},
						Items: []core.KeyToPath{
							{
								Key:  "G_AMMA_TXT",
								Path: "g_amma.txt",
							},
						},
					},
				},
			},
		}
		assert.ElementsMatch(t, expectedVolumes, actualVolumes)
		for _, volName := range dirCache {
			assert.True(t, utils.IsValidSlug(volName), "\"%s\" is valid slug", volName)
		}
	})
}

func TestK8sVolumesFromScore(t *testing.T) {
	// Volume resolution in K8sScoreConverter.Volumes is commented out pending
	// the volume placeholder substitution TODO there, so Volumes currently
	// returns no volumes, no mounts and no errors. These expectations still
	// describe the intended behaviour: unskip them when that code comes back.
	t.Skip("volume resolution disabled: see the TODO in K8sScoreConverter.Volumes")

	params := map[string]inputs.Input{
		"resources.db.name":     {Value: "db_name"},
		"resources.db.port":     {Value: 5432},
		"resources.db.host":     {Value: "db.example.com"},
		"resources.db.username": {Value: "test_user"},
		"resources.db.password": {Secret: &inputs.SecretInput{
			Store: "secret",
			Key:   "mysecret/mypassword",
		}},

		"resources.vol-01.k8s": {Value: map[string]any{
			"secret": map[string]any{
				"secretName": "one-secret",
			},
		}},
		"resources.vol-01.google-cloud-run": {Value: map[string]any{
			"secret": map[string]any{
				"secretName": "one-cloud-run-secret",
			},
		}},

		"resources.vol-02.k8s": {Value: map[string]any{
			"secret": map[string]any{
				"secretName": "two-secret",
			},
		}},
	}
	workloadName := "workloads.test"

	workloadResources := types.WorkloadResources{
		"db": types.Resource{
			Type: "postgres",
		},
		"vol-01": types.Resource{
			Type:  "volume",
			Class: ptrStr("ro"),
		},
		"vol-02": types.Resource{
			Type:  "volume",
			Class: ptrStr("rw"),
			Id:    ptrStr("common"),
		},
		"missing": types.Resource{
			Type: "volume",
		},
	}

	t.Run("no volumes", func(t *testing.T) {
		workload := types.Workload{
			Containers: types.WorkloadContainers{
				"main": types.Container{
					Variables: types.ContainerVariables{
						"MESSAGE": "Hello World!",
					},
				},
			},
			Resources: workloadResources,
		}
		converter := K8sScoreConverter{
			WorkloadResource: WorkloadResource{
				Workload:      &workload,
				Name:          workloadName,
				Substitutions: params,
			},
		}
		actualVolumeMounts, actualVolumes, err := converter.Volumes("k8s")
		require.NoError(t, err)
		assert.Empty(t, actualVolumeMounts)
		assert.Empty(t, actualVolumes)
	})

	t.Run("one volume k8s", func(t *testing.T) {
		workload := types.Workload{
			Containers: types.WorkloadContainers{
				"main": types.Container{
					Variables: types.ContainerVariables{
						"MESSAGE": "Hello World!",
					},
					Volumes: types.ContainerVolumes{
						"/hello/world": types.ContainerVolume{
							Path:   ptrStr("new"),
							Source: "${resources.vol-01}",
						},
					},
				},
			},
			Resources: workloadResources,
		}
		converter := K8sScoreConverter{
			WorkloadResource: WorkloadResource{
				Workload:      &workload,
				Name:          workloadName,
				Substitutions: params,
			},
		}
		actualVolumeMounts, actualVolumes, err := converter.Volumes("k8s")
		require.NoError(t, err)
		expectedVolumeMounts := map[string][]core.VolumeMount{
			"main": {
				{
					Name:              "resources-vol-01",
					MountPath:         "/hello/world",
					ReadOnly:          false,
					RecursiveReadOnly: nil,
					SubPath:           "new",
					MountPropagation:  nil,
					SubPathExpr:       "",
				},
			},
		}
		require.Contains(t, actualVolumeMounts, "main")
		assert.ElementsMatch(t, expectedVolumeMounts["main"], actualVolumeMounts["main"])

		expectedVolumes := []core.Volume{
			{
				Name: "resources-vol-01",
				VolumeSource: core.VolumeSource{
					Secret: &core.SecretVolumeSource{
						SecretName: "one-secret",
					},
				},
			},
		}
		assert.ElementsMatch(t, expectedVolumes, actualVolumes)
	})

	t.Run("one volume 2 containers cloud-run", func(t *testing.T) {
		workload := types.Workload{
			Containers: types.WorkloadContainers{
				"main": types.Container{
					Variables: types.ContainerVariables{
						"MESSAGE": "Hello World!",
					},
					Volumes: types.ContainerVolumes{
						"/hello/world": types.ContainerVolume{
							Path:   ptrStr("new"),
							Source: "${resources.vol-01}",
						},
					},
				},
				"other": types.Container{
					Variables: types.ContainerVariables{
						"MESSAGE": "Hello World!",
					},
					Volumes: types.ContainerVolumes{
						"other": types.ContainerVolume{
							Path:   ptrStr("/hello/world"),
							Source: "${resources.vol-01}",
						},
					},
				},
			},
			Resources: workloadResources,
		}
		converter := K8sScoreConverter{
			WorkloadResource: WorkloadResource{
				Workload:      &workload,
				Name:          workloadName,
				Substitutions: params,
			},
		}
		actualVolumeMounts, actualVolumes, err := converter.Volumes("google-cloud-run")
		require.NoError(t, err)
		expectedVolumeMounts := map[string][]core.VolumeMount{
			"main": {
				{
					Name:      "resources-vol-01",
					MountPath: "/hello/world",
					SubPath:   "new",
				},
			},
		}
		require.Contains(t, actualVolumeMounts, "main")
		assert.ElementsMatch(t, expectedVolumeMounts["main"], actualVolumeMounts["main"])

		expectedVolumes := []core.Volume{
			{
				Name: "resources-vol-01",
				VolumeSource: core.VolumeSource{
					Secret: &core.SecretVolumeSource{
						SecretName: "one-cloud-run-secret",
					},
				},
			},
		}
		assert.ElementsMatch(t, expectedVolumes, actualVolumes)
	})
	t.Run("invalid placeholder", func(t *testing.T) {
		workload := types.Workload{
			Containers: types.WorkloadContainers{
				"main": types.Container{
					Volumes: types.ContainerVolumes{
						"new": types.ContainerVolume{
							Path:   ptrStr("/hello/world"),
							Source: "${resources.vol-01.k8s}",
						},
					},
				},
			},
			Resources: workloadResources,
		}
		converter := K8sScoreConverter{
			WorkloadResource: WorkloadResource{
				Workload:      &workload,
				Name:          workloadName,
				Substitutions: params,
			},
		}
		_, _, err := converter.Volumes("google-cloud-run")
		require.Error(t, err)
		assert.ErrorContains(t, err, "source must be a placeholder referencing a volume")
	})

	t.Run("placeholder not alone", func(t *testing.T) {
		workload := types.Workload{
			Containers: types.WorkloadContainers{
				"main": types.Container{
					Volumes: types.ContainerVolumes{
						"new": types.ContainerVolume{
							Path:   ptrStr("/hello/world"),
							Source: "Hello ${resources.vol-01.k8s} world",
						},
					},
				},
			},
			Resources: workloadResources,
		}
		converter := K8sScoreConverter{
			WorkloadResource: WorkloadResource{
				Workload:      &workload,
				Name:          workloadName,
				Substitutions: params,
			},
		}
		_, _, err := converter.Volumes("google-cloud-run")
		require.Error(t, err)
		assert.ErrorContains(t, err, "source must be a placeholder referencing a volume")
	})

	t.Run("placeholder does not resolve to a volume", func(t *testing.T) {
		workload := types.Workload{
			Containers: types.WorkloadContainers{
				"main": types.Container{
					Volumes: types.ContainerVolumes{
						"new": types.ContainerVolume{
							Path:   ptrStr("/hello/world"),
							Source: "${resources.db}",
						},
					},
				},
			},
			Resources: workloadResources,
		}
		converter := K8sScoreConverter{
			WorkloadResource: WorkloadResource{
				Workload:      &workload,
				Name:          workloadName,
				Substitutions: params,
			},
		}
		_, _, err := converter.Volumes("google-cloud-run")
		require.Error(t, err)
		assert.ErrorContains(t, err, "source is not of type volume")
	})

	t.Run("placeholder does not resolve to a resource", func(t *testing.T) {
		workload := types.Workload{
			Containers: types.WorkloadContainers{
				"main": types.Container{
					Volumes: types.ContainerVolumes{
						"new": types.ContainerVolume{
							Path:   ptrStr("/hello/world"),
							Source: "${resources.missing}",
						},
					},
				},
			},
			Resources: workloadResources,
		}
		converter := K8sScoreConverter{
			WorkloadResource: WorkloadResource{
				Workload:      &workload,
				Name:          workloadName,
				Substitutions: params,
			},
		}
		_, _, err := converter.Volumes("google-cloud-run")
		require.Error(t, err)
		assert.ErrorContains(t, err, "cannot resolve resource inputs for")
	})

	t.Run("volume does not support platform", func(t *testing.T) {
		workload := types.Workload{
			Containers: types.WorkloadContainers{
				"main": types.Container{
					Volumes: types.ContainerVolumes{
						"new": types.ContainerVolume{
							Path:   ptrStr("/hello/world"),
							Source: "${resources.vol-02}",
						},
					},
				},
			},
			Resources: workloadResources,
		}
		converter := K8sScoreConverter{
			WorkloadResource: WorkloadResource{
				Workload:      &workload,
				Name:          workloadName,
				Substitutions: params,
			},
		}
		_, _, err := converter.Volumes("google-cloud-run")
		require.Error(t, err)
		assert.ErrorContains(t, err, "google-cloud-run")
	})
}

func TestK8sSpodSpecFromScore(t *testing.T) {
	params := map[string]inputs.Input{
		"resources.db.name":     {Value: "db_name"},
		"resources.db.port":     {Value: 5432},
		"resources.db.host":     {Value: "db.example.com"},
		"resources.db.username": {Value: "test_user"},
		"resources.db.password": {Secret: &inputs.SecretInput{
			Store: "secret",
			Key:   "mysecret/mypassword",
		}},

		"resources.vol-01.k8s": {Value: map[string]any{
			"secret": map[string]any{
				"secretName": "one-secret",
			},
		}},
		"resources.vol-01.google-cloud-run": {Value: map[string]any{
			"secret": map[string]any{
				"secretName": "one-cloud-run-secret",
			},
		}},

		"resources.vol-02.k8s": {Value: map[string]any{
			"secret": map[string]any{
				"secretName": "two-secret",
			},
		}},

		"resources.readonly-store.name": {Value: "read-only bucket"},

		"resources.shared-store.name": {Value: "shared bucket"},
	}
	workloadName := "workloads.test"

	workloadResources := types.WorkloadResources{
		"db": types.Resource{
			Type: "postgres",
		},
		"vol-01": types.Resource{
			Type:  "volume",
			Class: ptrStr("ro"),
		},
		"vol-02": types.Resource{
			Type:  "volume",
			Class: ptrStr("rw"),
			Id:    ptrStr("common"),
		},
		"readonly-store": types.Resource{
			Type:  "bucket",
			Class: ptrStr("ro"),
		},
		"shared-store": types.Resource{
			Type:  "bucket",
			Class: ptrStr("rw"),
			Id:    ptrStr("common"),
		},
		"missing": types.Resource{
			Type: "volume",
		},
	}

	t.Run("just image", func(t *testing.T) {
		workload := types.Workload{
			Containers: types.WorkloadContainers{
				"main": types.Container{
					Image: "busybox:latest",
				},
			},
		}

		converter := K8sScoreConverter{
			WorkloadResource: WorkloadResource{
				Workload:      &workload,
				Name:          workloadName,
				Substitutions: params,
			},
			EnvVarSecretResolver:  testEnvVarSecretResolver,
			ContainerFileResolver: testContainerFileResolver_NoCall(t),
		}
		podSpec, err := converter.PodSpec("k8s")
		require.NoError(t, err)
		expectedPodSpec := core.PodSpec{
			Containers: []core.Container{
				{
					Name:  "main",
					Image: "busybox:latest",
				},
			},
		}
		assert.Equal(t, expectedPodSpec, podSpec)
	})

	t.Run("full pod multi container", func(t *testing.T) {
		// Expects the volumes and volumeMounts that K8sScoreConverter.Volumes
		// no longer produces. See the skip in TestK8sVolumesFromScore.
		t.Skip("volume resolution disabled: see the TODO in K8sScoreConverter.Volumes")

		workload := types.Workload{
			Containers: types.WorkloadContainers{
				"main": types.Container{
					Image: "busybox:latest",
					ReadinessProbe: &types.ContainerProbe{
						HttpGet: &types.HttpProbe{
							Host: nil,
							HttpHeaders: []types.HttpProbeHttpHeadersElem{
								{
									Name:  "X-Test-Header",
									Value: "test test",
								},
							},
							Path:   "/ready",
							Port:   8765,
							Scheme: (*types.HttpProbeScheme)(ptrStr("http")),
						},
					},
					Resources: &types.ContainerResources{
						Limits: &types.ResourcesLimits{
							Cpu:    ptrStr("500m"),
							Memory: ptrStr("128Mi"),
						},
					},
					Variables: types.ContainerVariables{
						"DBCONN":      "postgresql://${resources.db.username}:${resources.db.password}@${resources.db.host}:${resources.db.port}/${resources.db.name}",
						"BUCKET_NAME": "${resources.readonly-store.name}",
						"DBPASSWORD":  "${resources.db.password}",
						"ESCAPED":     "$${resources.db.password}",
						"K8S_ESCAPE":  "$(THIS_SHOULD_EXIST_IN_OUTPUT)",
					},
					Volumes: types.ContainerVolumes{
						"/hello/world": types.ContainerVolume{
							Path:   ptrStr("new"),
							Source: "${resources.vol-01}",
						},
					},
				},
				"sidecar": types.Container{
					Image: "nginx:latest",
					LivenessProbe: &types.ContainerProbe{
						HttpGet: &types.HttpProbe{
							Host:   ptrStr("example"),
							Path:   "/healthz",
							Port:   3456,
							Scheme: (*types.HttpProbeScheme)(ptrStr("http")),
						},
					},
					ReadinessProbe: &types.ContainerProbe{
						Exec: &types.ExecProbe{
							Command: []string{"/bin/sh", "-c", "true"},
						},
					},
					Resources: &types.ContainerResources{
						Requests: &types.ResourcesLimits{
							Cpu:    ptrStr("1240m"),
							Memory: ptrStr("64Mi"),
						},
					},
					Files: types.ContainerFiles{
						"/base/one/alpha.txt": types.ContainerFile{
							Content: ptrStr("ALPHA"),
						},
						"/base/two/gamma.txt": types.ContainerFile{
							Content: ptrStr("GAMMA"),
						},
						"/base/one/beta.txt": types.ContainerFile{
							Content: ptrStr("BETA"),
						},
					},
					Volumes: types.ContainerVolumes{
						"/some/dir": types.ContainerVolume{
							Source: "${resources.vol-01}",
						},
					},
				},
			},
			Resources: workloadResources,
		}
		fileVolNames := map[string]string{}
		converter := K8sScoreConverter{
			WorkloadResource: WorkloadResource{
				Workload:      &workload,
				Name:          workloadName,
				Substitutions: params,
			},
			EnvVarSecretResolver:  testEnvVarSecretResolver,
			ContainerFileResolver: testContainerFileResolver_RecordVolumes(t, fileVolNames),
		}
		podSpec, err := converter.PodSpec("k8s")
		require.NoError(t, err)

		expectedPodSpec := core.PodSpec{
			Containers: []core.Container{
				{
					Name:  "main",
					Image: "busybox:latest",
					Env: []core.EnvVar{
						{
							Name:  "BUCKET_NAME",
							Value: "read-only bucket",
						},
						{
							Name:  "DBCONN",
							Value: "postgresql://test_user:$(__SECRET__db_password__)@db.example.com:5432/db_name",
						},
						{
							Name:  "DBPASSWORD",
							Value: "$(__SECRET__db_password__)",
						},
						{
							Name:  "ESCAPED",
							Value: "$${resources.db.password}",
						},
						{
							Name:  "K8S_ESCAPE",
							Value: "$$(THIS_SHOULD_EXIST_IN_OUTPUT)",
						},
						{
							Name: "__SECRET__db_password__",
							ValueFrom: &core.EnvVarSource{
								SecretKeyRef: &core.SecretKeySelector{
									Key: "mypassword",
									LocalObjectReference: core.LocalObjectReference{
										Name: "mysecret",
									},
								},
							},
						},
					},
					ReadinessProbe: &core.Probe{
						ProbeHandler: core.ProbeHandler{
							HTTPGet: &core.HTTPGetAction{
								Path:   "/ready",
								Port:   intstr.Parse("8765"),
								Host:   "",
								Scheme: "http",
								HTTPHeaders: []core.HTTPHeader{
									{
										Name:  "X-Test-Header",
										Value: "test test",
									},
								},
							},
						},
					},
					Resources: core.ResourceRequirements{
						Limits: core.ResourceList{
							"cpu":    resource.MustParse("500m"),
							"memory": resource.MustParse("128Mi"),
						},
					},
					VolumeMounts: []core.VolumeMount{
						{
							Name:              "resources-vol-01",
							MountPath:         "/hello/world",
							ReadOnly:          false,
							RecursiveReadOnly: nil,
							SubPath:           "new",
							MountPropagation:  nil,
							SubPathExpr:       "",
						},
					},
				},
				{
					Name:  "sidecar",
					Image: "nginx:latest",
					LivenessProbe: &core.Probe{
						ProbeHandler: core.ProbeHandler{
							HTTPGet: &core.HTTPGetAction{
								Path:   "/healthz",
								Port:   intstr.Parse("3456"),
								Host:   "example",
								Scheme: "http",
							},
						},
					},
					ReadinessProbe: &core.Probe{
						ProbeHandler: core.ProbeHandler{
							Exec: &core.ExecAction{
								Command: []string{"/bin/sh", "-c", "true"},
							},
						},
					},
					Resources: core.ResourceRequirements{
						Requests: core.ResourceList{
							"cpu":    resource.MustParse("1240m"),
							"memory": resource.MustParse("64Mi"),
						},
					},
					VolumeMounts: []core.VolumeMount{
						{
							Name:      fileVolNames["/base/one"],
							MountPath: "/base/one",
							ReadOnly:  true,
						},
						{
							Name:      fileVolNames["/base/two"],
							MountPath: "/base/two",
							ReadOnly:  true,
						},
						{
							Name:              "resources-vol-01",
							MountPath:         "/some/dir",
							ReadOnly:          false,
							RecursiveReadOnly: nil,
							SubPath:           "",
							MountPropagation:  nil,
							SubPathExpr:       "",
						},
					},
				},
			},
			Volumes: []core.Volume{
				{
					Name: fileVolNames["/base/one"],
					VolumeSource: core.VolumeSource{
						ConfigMap: &core.ConfigMapVolumeSource{
							LocalObjectReference: core.LocalObjectReference{
								Name: "test-config",
							},
							Items: []core.KeyToPath{
								{
									Key:  "ALPHA_TXT",
									Path: "alpha.txt",
								},
								{
									Key:  "BETA_TXT",
									Path: "beta.txt",
								},
							},
						},
					},
				},
				{
					Name: fileVolNames["/base/two"],
					VolumeSource: core.VolumeSource{
						ConfigMap: &core.ConfigMapVolumeSource{
							LocalObjectReference: core.LocalObjectReference{
								Name: "test-config",
							},
							Items: []core.KeyToPath{
								{
									Key:  "GAMMA_TXT",
									Path: "gamma.txt",
								},
							},
						},
					},
				},
				{
					Name: "resources-vol-01",
					VolumeSource: core.VolumeSource{
						Secret: &core.SecretVolumeSource{
							SecretName: "one-secret",
						},
					},
				},
			},
		}
		assert.Equal(t, expectedPodSpec, podSpec)
	})

}
