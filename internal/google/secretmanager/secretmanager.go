package secretmanager

import (
	"context"
	"fmt"
	"path"

	gsm "cloud.google.com/go/secretmanager/apiv1"
	"cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
	gax "github.com/googleapis/gax-go/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/humanitec/cloudrun-container-runner/internal/google"
)

// managedByLabel marks the secrets this runner creates.
const managedByLabel = "score2cloudrun"

type api interface {
	CreateSecret(context.Context, *secretmanagerpb.CreateSecretRequest, ...gax.CallOption) (*secretmanagerpb.Secret, error)
	AddSecretVersion(context.Context, *secretmanagerpb.AddSecretVersionRequest, ...gax.CallOption) (*secretmanagerpb.SecretVersion, error)
	AccessSecretVersion(context.Context, *secretmanagerpb.AccessSecretVersionRequest, ...gax.CallOption) (*secretmanagerpb.AccessSecretVersionResponse, error)
	Close() error
}

// Client reads and writes secrets in Google Secret Manager.
//
// The underlying client is built on first use, so that converting a workload
// which holds no secrets needs neither Google credentials nor a complete
// target.
type Client struct {
	target google.Target
	api    api
}

// New returns a Client that works in the target's project.
// Holds a lazy client - the client is created when the first secret is saved.
func New(target google.Target) *Client {
	return &Client{target: target}
}

// ensureClient builds the Secret Manager client on first use.
//
// Credentials are whatever Application Default Credentials finds, which is what
// the entrypoint has already arranged: GOOGLE_APPLICATION_CREDENTIALS when the
// Resource Definition supplies a key file, the metadata server otherwise.
func (c *Client) ensureClient(ctx context.Context) error {
	if c.api != nil {
		return nil
	}
	if err := c.target.Validate(); err != nil {
		return fmt.Errorf("no target to store the secret in: %w", err)
	}

	client, err := gsm.NewClient(ctx)
	if err != nil {
		return fmt.Errorf("connecting to google secret manager: %w", err)
	}
	c.api = client
	return nil
}

// Close releases the Secret Manager client. It is safe to call when no client
// was ever built.
func (c *Client) Close() error {
	if c.api == nil {
		return nil
	}
	return c.api.Close()
}

// SaveSecret creates the secret if it does not exist yet and returns its version.
func (c *Client) SaveSecret(ctx context.Context, name, value string) (string, error) {
	if err := c.ensureClient(ctx); err != nil {
		return "", err
	}
	secretPath := fmt.Sprintf("projects/%s/secrets/%s", c.target.Project, name)

	// Cloud Run only accepts a user-managed secret if it is replicated in the
	// region the service runs in, so the two regions are necessarily the same.
	_, err := c.api.CreateSecret(ctx, &secretmanagerpb.CreateSecretRequest{
		Parent:   "projects/" + c.target.Project,
		SecretId: name,
		Secret: &secretmanagerpb.Secret{
			Replication: &secretmanagerpb.Replication{
				Replication: &secretmanagerpb.Replication_UserManaged_{
					UserManaged: &secretmanagerpb.Replication_UserManaged{
						Replicas: []*secretmanagerpb.Replication_UserManaged_Replica{
							{Location: c.target.Region},
						},
					},
				},
			},
			Labels: map[string]string{"managed-by": managedByLabel},
		},
	})
	if err != nil && status.Code(err) != codes.AlreadyExists {
		return "", fmt.Errorf("creating secret %s: %w", name, err)
	}

	// Add a new version only if the value changed. A new version would force a new Cloud Run
	// revision even when nothing changed.
	version, stored, err := c.currentVersion(ctx, secretPath)
	if err != nil {
		return "", err
	}
	if version != "" && stored == value {
		return version, nil
	}

	added, err := c.api.AddSecretVersion(ctx, &secretmanagerpb.AddSecretVersionRequest{
		Parent:  secretPath,
		Payload: &secretmanagerpb.SecretPayload{Data: []byte(value)},
	})
	if err != nil {
		return "", fmt.Errorf("adding a version to secret %s: %w", name, err)
	}
	return path.Base(added.GetName()), nil
}

// currentVersion returns the newest version of the secret at secretPath and the value that version holds.
func (c *Client) currentVersion(ctx context.Context, secretPath string) (string, string, error) {
	current, err := c.api.AccessSecretVersion(ctx, &secretmanagerpb.AccessSecretVersionRequest{
		Name: secretPath + "/versions/latest",
	})
	switch status.Code(err) {
	case codes.OK:
	case codes.NotFound, codes.FailedPrecondition:
		// The secret may have just been created and have no versions, or its newest
		// version may have been disabled or destroyed.
		return "", "", nil
	default:
		return "", "", fmt.Errorf("reading the newest version of secret %s: %w", path.Base(secretPath), err)
	}
	return path.Base(current.GetName()), string(current.GetPayload().GetData()), nil
}
