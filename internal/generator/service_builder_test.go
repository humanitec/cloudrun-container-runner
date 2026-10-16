package generator

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/score-spec/score-go/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/humanitec/cloudrun-container-runner/internal/google/secretmanager"
	"github.com/humanitec/cloudrun-container-runner/internal/score"
	"github.com/humanitec/cloudrun-container-runner/internal/utils"
)

// fakeSecretSaver records what would be written to Secret Manager.
type fakeSecretSaver struct {
	saved map[string]string
	// services is the service each secret was saved for.
	services map[string]string
	err      error
}

func newFakeSecretSaver() *fakeSecretSaver {
	return &fakeSecretSaver{saved: map[string]string{}, services: map[string]string{}}
}

func (f *fakeSecretSaver) SaveSecret(_ context.Context, service, name, value string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	f.saved[name] = value
	f.services[name] = service
	return "7", nil
}

func TestEnvVar(t *testing.T) {
	substitutions := map[string]score.SubValue{
		"resources.db.host":     {Value: "db.example.com"},
		"resources.db.port":     {Value: 5432},
		"resources.db.password": {Secret: &score.SecretRef{Type: score.DirectSecretType, Value: "s3cr3t"}},
		"resources.db.ref":      {Secret: &score.SecretRef{Store: "gsm", Ref: "projects/1/secrets/db/versions/3"}},
	}
	builderFor := func(variables types.ContainerVariables, secrets SecretSaver) *ServiceBuilder {
		return NewServiceBuilder(types.Workload{
			Metadata: map[string]any{"name": "hello-world"},
			Containers: types.WorkloadContainers{
				"main": {Image: "busybox:latest", Variables: variables},
			},
		}, substitutions, secrets, "hello-world")
	}

	t.Run("no variables", func(t *testing.T) {
		env, err := builderFor(nil, newFakeSecretSaver()).EnvVar(t.Context(), "main")
		require.NoError(t, err)
		assert.Empty(t, env)
	})

	t.Run("plain values are inlined and sorted", func(t *testing.T) {
		saver := newFakeSecretSaver()
		env, err := builderFor(types.ContainerVariables{
			"PORT":  "${resources.db.port}",
			"HOST":  "${resources.db.host}",
			"IMAGE": "${container.image}",
			"ESC":   "$${resources.db.host}",
		}, saver).EnvVar(t.Context(), "main")
		require.NoError(t, err)
		assert.Equal(t, []corev1.EnvVar{
			{Name: "ESC", Value: "${resources.db.host}"},
			{Name: "HOST", Value: "db.example.com"},
			{Name: "IMAGE", Value: "busybox:latest"},
			{Name: "PORT", Value: "5432"},
		}, env)
		assert.Empty(t, saver.saved, "nothing secret, nothing stored")
	})

	t.Run("a variable with a secret is stored whole in gsm", func(t *testing.T) {
		saver := newFakeSecretSaver()
		env, err := builderFor(types.ContainerVariables{
			"HOST": "${resources.db.host}",
			"URL":  "pg://${resources.db.password}@${resources.db.host}:${resources.db.port}",
		}, saver).EnvVar(t.Context(), "main")
		require.NoError(t, err)

		wantName := secretmanager.SecretName("hello-world", "main", "env", "URL")
		assert.Equal(t, []corev1.EnvVar{
			{Name: "HOST", Value: "db.example.com"},
			{Name: "URL", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: wantName},
				Key:                  "7",
			}}},
		}, env)
		assert.Equal(t, map[string]string{wantName: "pg://s3cr3t@db.example.com:5432"}, saver.saved)
	})

	t.Run("errors", func(t *testing.T) {
		saveErr := errors.New("permission denied")
		testCases := []struct {
			name        string
			value       string
			saveErr     error
			wantErrText string
		}{
			{name: "secret reference", value: "${resources.db.ref}", wantErrText: "secret with name V: secret references are not supported"},
			{name: "missing substitution", value: "${resources.nope.x}", wantErrText: `resolving placeholder "resources.nope.x": no substitution found`},
			{name: "incomplete placeholder", value: "${resources.db.host", wantErrText: "resolving variable V in container main: incomplete placeholder"},
			{name: "saving fails", value: "${resources.db.password}", saveErr: saveErr, wantErrText: "saving secret to google secret manager"},
		}
		for _, testCase := range testCases {
			t.Run(testCase.name, func(t *testing.T) {
				saver := newFakeSecretSaver()
				saver.err = testCase.saveErr
				_, err := builderFor(types.ContainerVariables{"V": testCase.value}, saver).EnvVar(t.Context(), "main")
				require.ErrorContains(t, err, testCase.wantErrText)
				if testCase.saveErr != nil {
					assert.ErrorIs(t, err, testCase.saveErr)
				}
			})
		}
	})
}

func TestContainerFiles(t *testing.T) {
	substitutions := map[string]score.SubValue{
		"resources.db.host":     {Value: "db.example.com"},
		"resources.db.password": {Secret: &score.SecretRef{Type: score.DirectSecretType, Value: "s3cr3t"}},
		"resources.db.ref":      {Secret: &score.SecretRef{Store: "gsm", Ref: "projects/1/secrets/db/versions/3"}},
	}
	builderFor := func(files types.ContainerFiles, secrets SecretSaver) *ServiceBuilder {
		return NewServiceBuilder(types.Workload{
			Metadata: map[string]any{"name": "hello-world"},
			Containers: types.WorkloadContainers{
				"main": {Image: "busybox:latest", Files: files},
			},
		}, substitutions, secrets, "hello-world")
	}

	t.Run("no files", func(t *testing.T) {
		mounts, volumes, err := builderFor(nil, newFakeSecretSaver()).ContainerFiles(t.Context())
		require.NoError(t, err)
		assert.Empty(t, mounts)
		assert.Empty(t, volumes)
	})

	t.Run("secret files are stored in gsm and mounted", func(t *testing.T) {
		saver := newFakeSecretSaver()
		mounts, volumes, err := builderFor(types.ContainerFiles{
			"/etc/app/config.yaml": {Content: new("password: ${resources.db.password}\nhost: ${resources.db.host}"), Mode: new("0400")},
			"/etc/other/raw":       {Content: new("${resources.db.password}")},
		}, saver).ContainerFiles(t.Context())
		require.NoError(t, err)

		wantConfig := secretmanager.SecretName("hello-world", "main", "vol", "config.yaml")
		wantRaw := secretmanager.SecretName("hello-world", "main", "vol", "raw")
		assert.Equal(t, map[string]string{
			wantConfig: "password: s3cr3t\nhost: db.example.com",
			wantRaw:    "s3cr3t",
		}, saver.saved)

		require.Len(t, volumes, 2)
		require.Len(t, mounts["main"], 2)
		byMountPath := map[string]corev1.VolumeMount{}
		for _, m := range mounts["main"] {
			assert.True(t, m.ReadOnly)
			byMountPath[m.MountPath] = m
		}
		for _, v := range volumes {
			assert.True(t, utils.IsValidSlug(v.Name), "%q is a valid slug", v.Name)
			require.NotNil(t, v.Secret)
			require.Len(t, v.Secret.Items, 1)
			assert.Equal(t, "7", v.Secret.Items[0].Key)
		}
		configVolume := volumes[0]
		if configVolume.Secret.SecretName != wantConfig {
			configVolume = volumes[1]
		}
		assert.Equal(t, wantConfig, configVolume.Secret.SecretName)
		assert.Equal(t, "config.yaml", configVolume.Secret.Items[0].Path)
		assert.Equal(t, new(int32(0o400)), configVolume.Secret.Items[0].Mode)
		assert.Equal(t, configVolume.Name, byMountPath["/etc/app"].Name)
	})

	t.Run("errors", func(t *testing.T) {
		saveErr := errors.New("permission denied")
		testCases := []struct {
			name        string
			files       types.ContainerFiles
			saveErr     error
			wantErrText string
		}{
			{
				name:        "plain file",
				files:       types.ContainerFiles{"/etc/app/f": {Content: new("host: ${resources.db.host}")}},
				wantErrText: "file /etc/app/f: cloudrun only supports mounting files from google secret manager secrets",
			},
			{
				name:        "no expand keeps the file plain",
				files:       types.ContainerFiles{"/etc/app/f": {Content: new("${resources.db.password}"), NoExpand: new(true)}},
				wantErrText: "cloudrun only supports mounting files from google secret manager secrets",
			},
			{
				name: "two files in one directory",
				files: types.ContainerFiles{
					"/etc/app/a": {Content: new("${resources.db.password}")},
					"/etc/app/b": {Content: new("${resources.db.password}")},
				},
				wantErrText: "more then one file specified in directory /etc/app",
			},
			{
				name:        "secret reference",
				files:       types.ContainerFiles{"/etc/app/f": {Content: new("${resources.db.ref}")}},
				wantErrText: "file /etc/app/f: secret references are not supported",
			},
			{
				name:        "relative path",
				files:       types.ContainerFiles{"etc/app/f": {Content: new("${resources.db.password}")}},
				wantErrText: `processing containers.main.files: "etc/app" is not an absolute path`,
			},
			{
				name:        "invalid mode",
				files:       types.ContainerFiles{"/etc/app/f": {Content: new("${resources.db.password}"), Mode: new("9")}},
				wantErrText: "file mode for file /etc/app/f is invalid",
			},
			{
				name:        "saving fails",
				files:       types.ContainerFiles{"/etc/app/f": {Content: new("${resources.db.password}")}},
				saveErr:     saveErr,
				wantErrText: "file /etc/app/f: saving secret to google secret manager",
			},
		}
		for _, testCase := range testCases {
			t.Run(testCase.name, func(t *testing.T) {
				saver := newFakeSecretSaver()
				saver.err = testCase.saveErr
				_, _, err := builderFor(testCase.files, saver).ContainerFiles(t.Context())
				require.ErrorContains(t, err, testCase.wantErrText)
				if testCase.saveErr != nil {
					assert.ErrorIs(t, err, testCase.saveErr)
				}
			})
		}
	})
}

func TestVolumes(t *testing.T) {
	substitutions := map[string]score.SubValue{
		"resources.vol-01": {Value: map[string]any{
			"kubernetes":       map[string]any{"secret": map[string]any{"secretName": "one-secret"}},
			"google-cloud-run": map[string]any{"secret": map[string]any{"secretName": "one-cloud-run-secret"}},
		}},
		"resources.vol-02": {Value: map[string]any{
			"google-cloud-run": map[string]any{"csi": map[string]any{"driver": "gcsfuse.run.googleapis.com"}},
		}},
		"resources.k8s-only": {Value: map[string]any{
			"kubernetes": map[string]any{"secret": map[string]any{"secretName": "k8s-secret"}},
		}},
		"resources.not-an-object": {Value: "just a string"},
		"resources.platform-not-an-object": {Value: map[string]any{
			"google-cloud-run": "just a string",
		}},
		"resources.not-a-volume-spec": {Value: map[string]any{
			"google-cloud-run": map[string]any{"secret": "should be an object"},
		}},
		"resources.secret-volume": {Secret: &score.SecretRef{Store: "secret", Ref: "mysecret/myvolume"}},
	}
	resources := types.WorkloadResources{
		"db":                     {Type: "postgres"},
		"vol-01":                 {Type: "volume"},
		"vol-02":                 {Type: "volume"},
		"k8s-only":               {Type: "volume"},
		"missing":                {Type: "volume"},
		"not-an-object":          {Type: "volume"},
		"platform-not-an-object": {Type: "volume"},
		"not-a-volume-spec":      {Type: "volume"},
		"secret-volume":          {Type: "volume"},
	}
	builderFor := func(containers types.WorkloadContainers) *ServiceBuilder {
		return NewServiceBuilder(types.Workload{
			Metadata:   map[string]any{"name": "hello-world"},
			Containers: containers,
			Resources:  resources,
		}, substitutions, nil, "hello-world")
	}

	t.Run("no volumes", func(t *testing.T) {
		mounts, volumes, err := builderFor(types.WorkloadContainers{"main": {}}).Volumes()
		require.NoError(t, err)
		assert.Empty(t, mounts)
		assert.Empty(t, volumes)
	})

	t.Run("uses the google-cloud-run output", func(t *testing.T) {
		mounts, volumes, err := builderFor(types.WorkloadContainers{
			"main": {Volumes: types.ContainerVolumes{
				"/hello/world": {Path: new("new"), Source: "${resources.vol-01}"},
			}},
		}).Volumes()
		require.NoError(t, err)
		assert.Equal(t, []corev1.Volume{{
			Name:         "resources-vol-01",
			VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "one-cloud-run-secret"}},
		}}, volumes)
		assert.Equal(t, []corev1.VolumeMount{{Name: "resources-vol-01", MountPath: "/hello/world", SubPath: "new"}}, mounts["main"])
	})

	t.Run("read only volume", func(t *testing.T) {
		mounts, _, err := builderFor(types.WorkloadContainers{
			"main": {Volumes: types.ContainerVolumes{
				"/hello/world": {Source: "${resources.vol-01}", ReadOnly: new(true)},
			}},
		}).Volumes()
		require.NoError(t, err)
		require.Len(t, mounts["main"], 1)
		mount := mounts["main"][0]
		assert.True(t, mount.ReadOnly)
		assert.Empty(t, mount.SubPath)
		require.NotNil(t, mount.RecursiveReadOnly)
		assert.Equal(t, corev1.RecursiveReadOnlyIfPossible, *mount.RecursiveReadOnly)
	})

	t.Run("volumes are shared between containers", func(t *testing.T) {
		mounts, volumes, err := builderFor(types.WorkloadContainers{
			"main": {Volumes: types.ContainerVolumes{
				"/one": {Source: "${resources.vol-01}"},
				"/two": {Source: "${resources.vol-02}"},
			}},
			"other": {Volumes: types.ContainerVolumes{
				"/two": {Source: "${resources.vol-02}"},
			}},
		}).Volumes()
		require.NoError(t, err)
		assert.ElementsMatch(t, []corev1.VolumeMount{
			{Name: "resources-vol-01", MountPath: "/one"},
			{Name: "resources-vol-02", MountPath: "/two"},
		}, mounts["main"])
		assert.ElementsMatch(t, []corev1.VolumeMount{
			{Name: "resources-vol-02", MountPath: "/two"},
		}, mounts["other"])
		assert.ElementsMatch(t, []corev1.Volume{
			{Name: "resources-vol-01", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "one-cloud-run-secret"}}},
			{Name: "resources-vol-02", VolumeSource: corev1.VolumeSource{CSI: &corev1.CSIVolumeSource{Driver: "gcsfuse.run.googleapis.com"}}},
		}, volumes)
	})

	t.Run("errors", func(t *testing.T) {
		testCases := []struct {
			name        string
			source      string
			wantErrText string
		}{
			{name: "source is not a placeholder", source: "vol-01", wantErrText: `source must be a placeholder referencing a volume, got "vol-01"`},
			{name: "placeholder is not alone", source: "Hello ${resources.vol-01} world", wantErrText: "source must be a placeholder referencing a volume"},
			{name: "placeholder has too many parts", source: "${resources.vol-01.k8s}", wantErrText: "i.e. of the form ${resources.NAME} got ${resources.vol-01.k8s}"},
			{name: "placeholder is not a resource", source: "${metadata.name}", wantErrText: "i.e. of the form ${resources.NAME} got ${metadata.name}"},
			{name: "resource is not a volume", source: "${resources.db}", wantErrText: "placeholder ${resources.db} in source is not of type volume, got postgres"},
			{name: "resource is not declared", source: "${resources.nowhere}", wantErrText: "placeholder ${resources.nowhere} cannot be resolved: no resource with name nowhere"},
			{name: "resource has no substitution", source: "${resources.missing}", wantErrText: `resolving volume resource resources.missing: resolving placeholder "resources.missing": no substitution found`},
			{name: "platform is missing from the output", source: "${resources.k8s-only}", wantErrText: "resolving volume resource resources.k8s-only: platform google-cloud-run does not exist in output or is not an object"},
			{name: "output is not an object", source: "${resources.not-an-object}", wantErrText: "resolving volume resource resources.not-an-object: invalid output for platform google-cloud-run, expected object"},
			{name: "platform output is not an object", source: "${resources.platform-not-an-object}", wantErrText: "resolving volume resource resources.platform-not-an-object: platform google-cloud-run does not exist in output or is not an object"},
			{name: "platform output is not a volume spec", source: "${resources.not-a-volume-spec}", wantErrText: "resolving volume resource resources.not-a-volume-spec: unable to parse output for platform google-cloud-run as k8s volume"},
			// A volume resource resolves to a spec, never to a secret.
			{name: "output is a secret", source: "${resources.secret-volume}", wantErrText: "resolving volume resource resources.secret-volume: invalid output for platform google-cloud-run, expected object"},
		}
		for _, testCase := range testCases {
			t.Run(testCase.name, func(t *testing.T) {
				_, _, err := builderFor(types.WorkloadContainers{
					"main": {Volumes: types.ContainerVolumes{
						"/hello/world": {Path: new("new"), Source: testCase.source},
					}},
				}).Volumes()
				require.ErrorContains(t, err, testCase.wantErrText)
			})
		}
	})
}

func TestExpandFile(t *testing.T) {
	builder := NewServiceBuilder(types.Workload{
		Containers: map[string]types.Container{
			"one": {
				Image: "image-one:latest",
			},
			"two": {
				Image: "image-two:latest",
			},
		},
		Resources: types.WorkloadResources{
			"db": types.Resource{
				Type: "postgres",
			},
			"cache": types.Resource{
				Type: "redis",
			},
			"shared-store": types.Resource{
				Type:  "bucket",
				Class: new("rw"),
				Id:    new("common"),
			},
			"missing": types.Resource{
				Type: "volume",
			},
		},
	}, map[string]score.SubValue{
		"resources.db.name":     {Value: "db_name"},
		"resources.db.port":     {Value: 5432},
		"resources.db.host":     {Value: "db.example.com"},
		"resources.db.username": {Value: "test_user"},
		"resources.db.password": {Secret: &score.SecretRef{
			Store: "secret",
			Ref:   "mysecret/mypassword",
		}},

		"resources.shared-store.name": {Value: "shared bucket"},

		"resources.cache.name":     {Value: "redis"},
		"resources.cache.port":     {Value: 6379},
		"resources.cache.host":     {Value: "cache.example.com"},
		"resources.cache.username": {Value: "redis_user"},
		"resources.cache.password": {Secret: &score.SecretRef{
			Type:  score.DirectSecretType,
			Value: "r3di5-p455w0rd",
		}},
		"resources.cache.db_index": {Secret: &score.SecretRef{
			Type:  score.DirectSecretType,
			Value: 7,
		}},
		"resources.cache.tls": {Secret: &score.SecretRef{
			Type:  score.DirectSecretType,
			Value: map[string]any{"ca": "root-ca", "verify": true},
		}},
	}, nil, "")
	t.Run("no placeholders", func(t *testing.T) {
		file := types.ContainerFile{
			Content: new("Hello World!"),
		}
		output, err := builder.ExpandFile(file, "one")
		require.NoError(t, err)
		assert.Equal(t, &score.SubValue{Value: "Hello World!"}, output)
	})
	t.Run("placeholders no expand", func(t *testing.T) {
		file := types.ContainerFile{
			Content:  new("Should not expand ${resources.db.name}"),
			NoExpand: new(true),
		}
		output, err := builder.ExpandFile(file, "one")
		require.NoError(t, err)
		assert.Equal(t, &score.SubValue{Value: "Should not expand ${resources.db.name}"}, output)
	})
	t.Run("single placeholder expand", func(t *testing.T) {
		file := types.ContainerFile{
			Content: new("${resources.db.name}"),
		}
		output, err := builder.ExpandFile(file, "one")
		require.NoError(t, err)
		assert.Equal(t, &score.SubValue{Value: "db_name"}, output)
	})
	t.Run("container placeholder", func(t *testing.T) {
		file := types.ContainerFile{
			Content: new("${container.image}"),
		}
		output, err := builder.ExpandFile(file, "one")
		require.NoError(t, err)
		assert.Equal(t, &score.SubValue{Value: "image-one:latest"}, output)
	})
	t.Run("multiple placeholder expand", func(t *testing.T) {
		file := types.ContainerFile{
			Content: new("${resources.db.name} ${resources.shared-store.name}"),
		}
		output, err := builder.ExpandFile(file, "one")
		require.NoError(t, err)
		assert.Equal(t, &score.SubValue{Value: "db_name shared bucket"}, output)
	})
	t.Run("single placeholder secret", func(t *testing.T) {
		file := types.ContainerFile{
			Content: new("${resources.db.password}"),
		}
		output, err := builder.ExpandFile(file, "one")
		require.NoError(t, err)
		assert.Equal(t, &score.SubValue{Secret: &score.SecretRef{
			Store: "secret",
			Ref:   "mysecret/mypassword",
		}}, output)
	})
	t.Run("multiple placeholder expand secrets", func(t *testing.T) {
		file := types.ContainerFile{
			Content: new("redis://${resources.cache.username}:${resources.cache.password}@${resources.cache.host}:${resources.cache.port}/${resources.cache.name}"),
		}
		output, err := builder.ExpandFile(file, "one")
		require.NoError(t, err)
		assert.Equal(t, &score.SubValue{Secret: &score.SecretRef{
			Type:  score.DirectSecretType,
			Value: "redis://redis_user:r3di5-p455w0rd@cache.example.com:6379/redis",
		}}, output)
	})
	t.Run("secret with number value", func(t *testing.T) {
		file := types.ContainerFile{
			Content: new("redis://${resources.cache.host}:${resources.cache.port}/${resources.cache.db_index}"),
		}
		output, err := builder.ExpandFile(file, "one")
		require.NoError(t, err)
		assert.Equal(t, &score.SubValue{Secret: &score.SecretRef{
			Type:  score.DirectSecretType,
			Value: "redis://cache.example.com:6379/7",
		}}, output)
	})
	t.Run("secret with object value", func(t *testing.T) {
		file := types.ContainerFile{
			Content: new("tls = ${resources.cache.tls}"),
		}
		output, err := builder.ExpandFile(file, "one")
		require.NoError(t, err)
		assert.Equal(t, &score.SubValue{Secret: &score.SecretRef{
			Type:  score.DirectSecretType,
			Value: `tls = {"ca":"root-ca","verify":true}`,
		}}, output)
	})

	t.Run("multiple placeholder secrets fail as not value", func(t *testing.T) {
		file := types.ContainerFile{
			Content: new("postgresql://${resources.db.username}:${resources.db.password}@${resources.db.host}:${resources.db.port}/${resources.db.name}"),
		}
		_, err := builder.ExpandFile(file, "one")
		assert.Error(t, err)
	})
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

func TestPod(t *testing.T) {
	substitutions := map[string]score.SubValue{
		"resources.db.name":     {Value: "db_name"},
		"resources.db.port":     {Value: 5432},
		"resources.db.host":     {Value: "db.example.com"},
		"resources.db.username": {Value: "test_user"},
		"resources.db.password": {Secret: &score.SecretRef{Type: score.DirectSecretType, Value: "s3cr3t"}},
		"resources.vol-01": {Value: map[string]any{
			"google-cloud-run": map[string]any{"secret": map[string]any{"secretName": "one-cloud-run-secret"}},
		}},
		"resources.readonly-store.name": {Value: "read-only bucket"},
	}
	resources := types.WorkloadResources{
		"db":             {Type: "postgres"},
		"vol-01":         {Type: "volume"},
		"readonly-store": {Type: "bucket"},
	}

	t.Run("just image", func(t *testing.T) {
		pod, err := NewServiceBuilder(types.Workload{
			Metadata:   map[string]any{"name": "hello-world"},
			Containers: types.WorkloadContainers{"main": {Image: "busybox:latest"}},
		}, substitutions, newFakeSecretSaver(), "hello-world").Pod(t.Context())
		require.NoError(t, err)
		assert.Equal(t, corev1.PodSpec{
			Containers: []corev1.Container{{Name: "main", Image: "busybox:latest", Env: []corev1.EnvVar{}}},
		}, pod.Spec)
	})

	t.Run("full pod multi container", func(t *testing.T) {
		saver := newFakeSecretSaver()
		pod, err := NewServiceBuilder(types.Workload{
			Metadata: map[string]any{"name": "hello-world"},
			Containers: types.WorkloadContainers{
				"main": {
					Image: "busybox:latest",
					ReadinessProbe: &types.ContainerProbe{HttpGet: &types.HttpProbe{
						HttpHeaders: []types.HttpProbeHttpHeadersElem{{Name: "X-Test-Header", Value: "test test"}},
						Path:        "/ready",
						Port:        8765,
						Scheme:      (*types.HttpProbeScheme)(new("http")),
					}},
					Resources: &types.ContainerResources{
						Limits: &types.ResourcesLimits{Cpu: new("500m"), Memory: new("128Mi")},
					},
					Variables: types.ContainerVariables{
						"DBCONN":      "postgresql://${resources.db.username}:${resources.db.password}@${resources.db.host}:${resources.db.port}/${resources.db.name}",
						"BUCKET_NAME": "${resources.readonly-store.name}",
						"ESCAPED":     "$${resources.db.password}",
					},
					Volumes: types.ContainerVolumes{
						"/hello/world": {Path: new("new"), Source: "${resources.vol-01}"},
					},
				},
				"sidecar": {
					Image: "nginx:latest",
					LivenessProbe: &types.ContainerProbe{HttpGet: &types.HttpProbe{
						Host:   new("example"),
						Path:   "/healthz",
						Port:   3456,
						Scheme: (*types.HttpProbeScheme)(new("http")),
					}},
					ReadinessProbe: &types.ContainerProbe{Exec: &types.ExecProbe{Command: []string{"/bin/sh", "-c", "true"}}},
					Resources: &types.ContainerResources{
						Requests: &types.ResourcesLimits{Cpu: new("1240m"), Memory: new("64Mi")},
					},
					Files: types.ContainerFiles{
						"/base/one/alpha.txt": {Content: new("${resources.db.password}")},
					},
					Volumes: types.ContainerVolumes{
						"/some/dir": {Source: "${resources.vol-01}"},
					},
				},
			},
			Resources: resources,
		}, substitutions, saver, "hello-world").Pod(t.Context())
		require.NoError(t, err)

		dbconnSecret := secretmanager.SecretName("hello-world", "main", "env", "DBCONN")
		fileSecret := secretmanager.SecretName("hello-world", "sidecar", "vol", "alpha.txt")
		assert.Equal(t, map[string]string{
			dbconnSecret: "postgresql://test_user:s3cr3t@db.example.com:5432/db_name",
			fileSecret:   "s3cr3t",
		}, saver.saved)

		require.Len(t, pod.Spec.Volumes, 2)
		fileVolume := pod.Spec.Volumes[0]
		require.NotNil(t, fileVolume.Secret)
		assert.Equal(t, fileSecret, fileVolume.Secret.SecretName)

		assert.Equal(t, corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "main",
					Image: "busybox:latest",
					Env: []corev1.EnvVar{
						{Name: "BUCKET_NAME", Value: "read-only bucket"},
						{Name: "DBCONN", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{Name: dbconnSecret},
							Key:                  "7",
						}}},
						{Name: "ESCAPED", Value: "${resources.db.password}"},
					},
					ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{
						Path:        "/ready",
						Port:        intstr.Parse("8765"),
						Scheme:      "http",
						HTTPHeaders: []corev1.HTTPHeader{{Name: "X-Test-Header", Value: "test test"}},
					}}},
					Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{
						"cpu":    resource.MustParse("500m"),
						"memory": resource.MustParse("128Mi"),
					}},
					VolumeMounts: []corev1.VolumeMount{
						{Name: "resources-vol-01", MountPath: "/hello/world", SubPath: "new"},
					},
				},
				{
					Name:  "sidecar",
					Image: "nginx:latest",
					Env:   []corev1.EnvVar{},
					LivenessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{
						Path:   "/healthz",
						Port:   intstr.Parse("3456"),
						Host:   "example",
						Scheme: "http",
					}}},
					ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{Exec: &corev1.ExecAction{
						Command: []string{"/bin/sh", "-c", "true"},
					}}},
					Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
						"cpu":    resource.MustParse("1240m"),
						"memory": resource.MustParse("64Mi"),
					}},
					VolumeMounts: []corev1.VolumeMount{
						{Name: fileVolume.Name, MountPath: "/base/one", ReadOnly: true},
						{Name: "resources-vol-01", MountPath: "/some/dir"},
					},
				},
			},
			Volumes: []corev1.Volume{
				fileVolume,
				{Name: "resources-vol-01", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "one-cloud-run-secret"}}},
			},
		}, pod.Spec)
	})

	t.Run("mounts of one volume have a stable order", func(t *testing.T) {
		// Map iteration is random, so a single run could pass by luck.
		for range 50 {
			pod, err := NewServiceBuilder(types.Workload{
				Metadata: map[string]any{"name": "hello-world"},
				Containers: types.WorkloadContainers{"main": {
					Image: "busybox:latest",
					Volumes: types.ContainerVolumes{
						"/data2": {Source: "${resources.vol-01}"},
						"/data1": {Source: "${resources.vol-01}"},
						"/data3": {Source: "${resources.vol-01}"},
					},
				}},
				Resources: resources,
			}, substitutions, newFakeSecretSaver(), "hello-world").Pod(t.Context())
			require.NoError(t, err)
			assert.Equal(t, []corev1.VolumeMount{
				{Name: "resources-vol-01", MountPath: "/data1"},
				{Name: "resources-vol-01", MountPath: "/data2"},
				{Name: "resources-vol-01", MountPath: "/data3"},
			}, pod.Spec.Containers[0].VolumeMounts)
		}
	})

	t.Run("errors", func(t *testing.T) {
		testCases := []struct {
			name        string
			container   types.Container
			wantErrText string
		}{
			{name: "invalid cpu", container: types.Container{Image: "i", Resources: &types.ContainerResources{Limits: &types.ResourcesLimits{Cpu: new("lots")}}}, wantErrText: "containers.main: converting resources.requests.cpu"},
			{name: "unsupported probe", container: types.Container{Image: "i", LivenessProbe: &types.ContainerProbe{}}, wantErrText: `containers.main.livenessProbe: only "httpGet" and "exec" probes supported`},
		}
		for _, testCase := range testCases {
			t.Run(testCase.name, func(t *testing.T) {
				_, err := NewServiceBuilder(types.Workload{
					Metadata:   map[string]any{"name": "hello-world"},
					Containers: types.WorkloadContainers{"main": testCase.container},
				}, substitutions, newFakeSecretSaver(), "hello-world").Pod(t.Context())
				require.ErrorContains(t, err, testCase.wantErrText)
			})
		}
	})
}
