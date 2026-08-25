package secretmanager

import (
	"context"
	"testing"

	"cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
	gax "github.com/googleapis/gax-go/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/humanitec/cloudrun-container-runner/internal/google"
)

// fakeAPI is a scripted stand-in for the Secret Manager API, so that
// what Saver does with each response can be checked without a real project.
type fakeAPI struct {
	createErr error

	accessResponse *secretmanagerpb.AccessSecretVersionResponse
	accessErr      error

	addedVersionName string

	created []*secretmanagerpb.CreateSecretRequest
	added   []string
	closed  bool
}

func (f *fakeAPI) CreateSecret(_ context.Context, req *secretmanagerpb.CreateSecretRequest, _ ...gax.CallOption) (*secretmanagerpb.Secret, error) {
	f.created = append(f.created, req)
	if f.createErr != nil {
		return nil, f.createErr
	}
	return &secretmanagerpb.Secret{Name: req.GetParent() + "/secrets/" + req.GetSecretId()}, nil
}

func (f *fakeAPI) AccessSecretVersion(_ context.Context, _ *secretmanagerpb.AccessSecretVersionRequest, _ ...gax.CallOption) (*secretmanagerpb.AccessSecretVersionResponse, error) {
	if f.accessErr != nil {
		return nil, f.accessErr
	}
	return f.accessResponse, nil
}

func (f *fakeAPI) AddSecretVersion(_ context.Context, req *secretmanagerpb.AddSecretVersionRequest, _ ...gax.CallOption) (*secretmanagerpb.SecretVersion, error) {
	f.added = append(f.added, string(req.GetPayload().GetData()))
	return &secretmanagerpb.SecretVersion{Name: f.addedVersionName}, nil
}

func (f *fakeAPI) Close() error {
	f.closed = true
	return nil
}

// accessed builds the response the API gives for a readable version.
func accessed(name, payload string) *secretmanagerpb.AccessSecretVersionResponse {
	return &secretmanagerpb.AccessSecretVersionResponse{
		Name:    name,
		Payload: &secretmanagerpb.SecretPayload{Data: []byte(payload)},
	}
}

func clientWith(a api) *Client {
	return &Client{
		target: google.Target{Project: "my-gcp-project", Region: "europe-west1"},
		api:    a,
	}
}

func TestSaveAddsAVersion(t *testing.T) {
	testCases := []struct {
		name string
		fake *fakeAPI
	}{
		{
			// A secret that has just been created has no versions to access.
			name: "the secret is new",
			fake: &fakeAPI{
				accessErr:        status.Error(codes.NotFound, "no versions"),
				addedVersionName: "projects/my-gcp-project/secrets/app_main_env_TOKEN_abc123/versions/1",
			},
		},
		{
			name: "the secret exists and holds a different value",
			fake: &fakeAPI{
				createErr:        status.Error(codes.AlreadyExists, "already exists"),
				accessResponse:   accessed("projects/my-gcp-project/secrets/app_main_env_TOKEN_abc123/versions/3", "old"),
				addedVersionName: "projects/my-gcp-project/secrets/app_main_env_TOKEN_abc123/versions/1",
			},
		},
		{
			// Nothing can be read back, so the value has to be written again.
			name: "the newest version was disabled or destroyed",
			fake: &fakeAPI{
				createErr:        status.Error(codes.AlreadyExists, "already exists"),
				accessErr:        status.Error(codes.FailedPrecondition, "version is disabled"),
				addedVersionName: "projects/my-gcp-project/secrets/app_main_env_TOKEN_abc123/versions/1",
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			version, err := clientWith(tc.fake).SaveSecret(t.Context(), "app_main_env_TOKEN_abc123", "s3cr3t")
			require.NoError(t, err)

			assert.Equal(t, "1", version, "the version number is taken off the resource name")
			assert.Equal(t, []string{"s3cr3t"}, tc.fake.added)
		})
	}
}

// Adding an identical version would change the version number the manifest
// pins, which forces a new Cloud Run revision for no reason.
func TestSaveReusesTheVersionWhenTheValueIsUnchanged(t *testing.T) {
	fake := &fakeAPI{
		createErr:      status.Error(codes.AlreadyExists, "already exists"),
		accessResponse: accessed("projects/my-gcp-project/secrets/app_main_env_TOKEN_abc123/versions/4", "s3cr3t"),
	}

	version, err := clientWith(fake).SaveSecret(t.Context(), "app_main_env_TOKEN_abc123", "s3cr3t")
	require.NoError(t, err)

	assert.Equal(t, "4", version)
	assert.Empty(t, fake.added, "an identical version must not be added")
}

// Cloud Run only accepts a user-managed secret replicated in the region the
// service runs in, so this has to follow CLOUDSDK_RUN_REGION.
func TestSaveReplicatesToTheCloudRunRegion(t *testing.T) {
	fake := &fakeAPI{
		accessErr:        status.Error(codes.NotFound, "no versions"),
		addedVersionName: "projects/my-gcp-project/secrets/app_main_env_TOKEN_abc123/versions/1",
	}

	_, err := clientWith(fake).SaveSecret(t.Context(), "app_main_env_TOKEN_abc123", "s3cr3t")
	require.NoError(t, err)

	require.Len(t, fake.created, 1)
	assert.Equal(t, "projects/my-gcp-project", fake.created[0].GetParent())
	assert.Equal(t, "app_main_env_TOKEN_abc123", fake.created[0].GetSecretId())

	replicas := fake.created[0].GetSecret().GetReplication().GetUserManaged().GetReplicas()
	require.Len(t, replicas, 1)
	assert.Equal(t, "europe-west1", replicas[0].GetLocation())

	assert.Equal(t, map[string]string{"managed-by": managedByLabel}, fake.created[0].GetSecret().GetLabels())
}

func TestSaveReportsAPIFailures(t *testing.T) {
	t.Run("the secret cannot be created", func(t *testing.T) {
		fake := &fakeAPI{createErr: status.Error(codes.PermissionDenied, "denied")}

		_, err := clientWith(fake).SaveSecret(t.Context(), "app_main_env_TOKEN_abc123", "s3cr3t")
		require.ErrorContains(t, err, "creating secret app_main_env_TOKEN_abc123")
		assert.Empty(t, fake.added, "nothing is written once the secret could not be created")
	})

	t.Run("the newest version cannot be read", func(t *testing.T) {
		fake := &fakeAPI{accessErr: status.Error(codes.PermissionDenied, "denied")}

		_, err := clientWith(fake).SaveSecret(t.Context(), "app_main_env_TOKEN_abc123", "s3cr3t")
		require.ErrorContains(t, err, "reading the newest version of secret app_main_env_TOKEN_abc123")
		assert.Empty(t, fake.added, "a permission failure is not treated as an absent version")
	})
}

func TestCloseReleasesTheClient(t *testing.T) {
	fake := &fakeAPI{}
	require.NoError(t, clientWith(fake).Close())
	assert.True(t, fake.closed)
}

// The target is only needed once a secret is actually written: a workload
// without secrets must convert with neither environment variable set.
func TestSaveReportsAnIncompleteTarget(t *testing.T) {
	testCases := []struct {
		name        string
		target      google.Target
		wantErrText string
	}{
		{
			name:        "no project",
			target:      google.Target{Region: "europe-west1"},
			wantErrText: google.EnvProject,
		},
		{
			name:        "no region",
			target:      google.Target{Project: "my-gcp-project"},
			wantErrText: google.EnvRegion,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(tc.target).SaveSecret(t.Context(), "app_main_env_TOKEN_abc123", "s3cr3t")
			require.ErrorContains(t, err, tc.wantErrText)
		})
	}
}

func TestCloseBeforeAnyClientWasBuilt(t *testing.T) {
	require.NoError(t, New(google.Target{}).Close())
}
