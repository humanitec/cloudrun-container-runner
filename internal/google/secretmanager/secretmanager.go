package secretmanager

import (
	"context"
	"errors"
	"fmt"
	"path"
	"slices"

	gsm "cloud.google.com/go/secretmanager/apiv1"
	"cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
	gax "github.com/googleapis/gax-go/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/humanitec/cloudrun-container-runner/internal/google"
)

const (
	managedByLabel = "managed-by"
	serviceLabel   = "service"
	managedByValue = "score2cloudrun"
)

type api interface {
	CreateSecret(context.Context, *secretmanagerpb.CreateSecretRequest, ...gax.CallOption) (*secretmanagerpb.Secret, error)
	AddSecretVersion(context.Context, *secretmanagerpb.AddSecretVersionRequest, ...gax.CallOption) (*secretmanagerpb.SecretVersion, error)
	AccessSecretVersion(context.Context, *secretmanagerpb.AccessSecretVersionRequest, ...gax.CallOption) (*secretmanagerpb.AccessSecretVersionResponse, error)
	ListSecrets(context.Context, *secretmanagerpb.ListSecretsRequest, ...gax.CallOption) ([]*secretmanagerpb.Secret, error)
	DeleteSecret(context.Context, *secretmanagerpb.DeleteSecretRequest, ...gax.CallOption) error
	Close() error
}

// gsmAPI is an adapter to api to enable testing - ListSecrets return an iterator, which is not an interface and
// can't be mocked.
type gsmAPI struct {
	*gsm.Client
}

func (g gsmAPI) ListSecrets(ctx context.Context, req *secretmanagerpb.ListSecretsRequest, opts ...gax.CallOption) ([]*secretmanagerpb.Secret, error) {
	var secrets []*secretmanagerpb.Secret
	for secret, err := range g.Client.ListSecrets(ctx, req, opts...).All() {
		if err != nil {
			return nil, err
		}
		secrets = append(secrets, secret)
	}
	return secrets, nil
}

// Client reads and writes the secrets of one Cloud Run service in Google Secret Manager.
//
// The underlying client is built on first use, so that converting a workload
// which holds no secrets needs neither Google credentials nor a complete
// target.
type Client struct {
	target  google.Target
	service string
	api     api

	// saved lists the secrets the current deployment holds for the Cloud Run Service.
	saved []string
}

// New returns a Client that works in the target's project.
// Holds a lazy client - the client is created when the first secret is saved.
func New(target google.Target, service string) *Client {
	return &Client{target: target, service: service}
}

// ensureClient builds the Secret Manager client on first use.
//
// Credentials are whatever Application Default Credentials finds:
// GOOGLE_APPLICATION_CREDENTIALS when the Resource Definition supplies a key
// file, the metadata server otherwise.
func (c *Client) ensureClient(ctx context.Context) error {
	if c.api != nil {
		return nil
	}
	if err := c.target.Validate(); err != nil {
		return fmt.Errorf("no target to store the secrets in: %w", err)
	}

	client, err := gsm.NewClient(ctx)
	if err != nil {
		return fmt.Errorf("connecting to google secret manager: %w", err)
	}
	c.api = gsmAPI{client}
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

// parent is the collection the project's secrets live in.
func (c *Client) parent() string {
	return "projects/" + c.target.Project
}

// labels marks a secret as this runner's, for this service.
func (c *Client) labels() map[string]string {
	return map[string]string{
		managedByLabel: managedByValue,
		serviceLabel:   c.service,
	}
}

// filter selects the secrets this runner holds for the service, in the syntax
// of https://cloud.google.com/secret-manager/docs/filtering.
func (c *Client) filter() string {
	return fmt.Sprintf("labels.%s=%q AND labels.%s=%q", managedByLabel, managedByValue, serviceLabel, c.service)
}

// SaveSecret creates the secret if it does not exist yet and returns its version.
func (c *Client) SaveSecret(ctx context.Context, name, value string) (string, error) {
	if err := c.ensureClient(ctx); err != nil {
		return "", err
	}
	secretPath := c.parent() + "/secrets/" + name

	// Cloud Run only accepts a user-managed secret if it is replicated in the
	// region the service runs in, so the two regions are necessarily the same.
	_, err := c.api.CreateSecret(ctx, &secretmanagerpb.CreateSecretRequest{
		Parent:   c.parent(),
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
			Labels: c.labels(),
		},
	})
	if err != nil && status.Code(err) != codes.AlreadyExists {
		return "", fmt.Errorf("creating secret %s: %w", name, err)
	}
	c.saved = append(c.saved, name)

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

// DeleteUnused deletes the service's secrets that SaveSecret did not write during this run.
// In the destroy case all secrets are deleted.
// Failures are joined - one secret that won't delete does not stop the rest.
func (c *Client) DeleteUnused(ctx context.Context) error {
	if err := c.ensureClient(ctx); err != nil {
		return err
	}

	secrets, err := c.api.ListSecrets(ctx, &secretmanagerpb.ListSecretsRequest{
		Parent: c.parent(),
		Filter: c.filter(),
	})
	if err != nil {
		return fmt.Errorf("listing the secrets of service %s: %w", c.service, err)
	}

	var errs []error
	for _, secret := range secrets {
		name := path.Base(secret.GetName())
		if slices.Contains(c.saved, name) {
			continue
		}
		err := c.api.DeleteSecret(ctx, &secretmanagerpb.DeleteSecretRequest{Name: secret.GetName()})
		if err != nil && status.Code(err) != codes.NotFound {
			errs = append(errs, fmt.Errorf("deleting secret %s: %w", name, err))
		}
	}
	return errors.Join(errs...)
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
