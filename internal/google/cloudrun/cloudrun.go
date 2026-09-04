package cloudrun

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path"
	"time"

	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
	run "google.golang.org/api/run/v1"

	"github.com/humanitec/cloudrun-container-runner/internal/google"
)

const (
	readyCondition = "Ready"

	conditionTrue  = "True"
	conditionFalse = "False"

	// defaultPollInterval is how often a deployment in flight is re-read.
	defaultPollInterval = 2 * time.Second
)

// Result describes a deployed service. It is what the Container Driver reads
// back from OUTPUTS_FILE.
type Result struct {
	Project  string `json:"project"`
	Region   string `json:"region"`
	Service  string `json:"service"`
	URL      string `json:"url"`
	Revision string `json:"revision"`
}

// Client creates, replaces and deletes Cloud Run services in the target's project and region.
//
// The underlying client is built on first use, so that a run which never
// reaches Cloud Run needs neither Google credentials nor a complete target.
type Client struct {
	target       google.Target
	services     *run.NamespacesServicesService
	pollInterval time.Duration
}

// New returns a Client that works in the target's project and region.
func New(target google.Target) *Client {
	return &Client{target: target, pollInterval: defaultPollInterval}
}

// endpoint is the regional Admin API host. The v1 API is only served regionally.
func endpoint(region string) string {
	return "https://" + region + "-run.googleapis.com/"
}

// ensureClient builds the Admin API client on first use.
//
// Credentials are whatever Application Default Credentials finds: the file
// GOOGLE_APPLICATION_CREDENTIALS names when the Resource Definition supplies a
// key, the metadata server otherwise.
func (c *Client) ensureClient(ctx context.Context) error {
	if c.services != nil {
		return nil
	}
	if err := c.target.Validate(); err != nil {
		return fmt.Errorf("no target to deploy to: %w", err)
	}

	admin, err := run.NewService(ctx, option.WithEndpoint(endpoint(c.target.Region)))
	if err != nil {
		return fmt.Errorf("connecting to the cloud run admin api: %w", err)
	}
	c.services = admin.Namespaces.Services
	return nil
}

// parent is the collection a service of this project lives in.
func (c *Client) parent() string {
	return "namespaces/" + c.target.Project
}

// resourceName is the fully qualified name of one service.
func (c *Client) resourceName(service string) string {
	return c.parent() + "/services/" + service
}

// Deploy applies manifest to Cloud Run, creating the service when it is not
// there yet and replacing it otherwise, and waits for the result to settle.
//
// Cancel ctx to give up waiting; the deployment itself carries on server side.
func (c *Client) Deploy(ctx context.Context, manifest *run.Service) (Result, error) {
	if err := c.ensureClient(ctx); err != nil {
		return Result{}, err
	}

	desired, err := c.forDeployment(manifest)
	if err != nil {
		return Result{}, err
	}
	id := desired.Metadata.Name
	name := c.resourceName(id)

	_, err = c.services.Get(name).Context(ctx).Do()
	switch {
	case isNotFound(err):
		desired, err = c.services.Create(c.parent(), desired).Context(ctx).Do()
		if err != nil {
			return Result{}, fmt.Errorf("creating cloud run service %s: %w", id, err)
		}
	case err != nil:
		return Result{}, fmt.Errorf("reading cloud run service %s: %w", id, err)
	default:
		// No metadata.resourceVersion, so this is a last-write-wins replace, the same as `gcloud run services replace`.
		desired, err = c.services.ReplaceService(name, desired).Context(ctx).Do()
		if err != nil {
			return Result{}, fmt.Errorf("replacing cloud run service %s: %w", id, err)
		}
	}

	deployed, err := c.waitReady(ctx, name, generationOf(desired))
	if err != nil {
		return Result{}, err
	}
	return c.result(deployed), nil
}

// Delete removes the service and waits for it to go. Deleting a service that is not there is not an error.
func (c *Client) Delete(ctx context.Context, service string) error {
	if err := c.ensureClient(ctx); err != nil {
		return err
	}
	name := c.resourceName(service)

	if _, err := c.services.Delete(name).Context(ctx).Do(); err != nil {
		if isNotFound(err) {
			return nil
		}
		return fmt.Errorf("deleting cloud run service %s: %w", service, err)
	}
	return c.waitGone(ctx, name)
}

// waitReady polls the service until Cloud Run has reconciled the generation
// just written and settled its Ready condition either way.
func (c *Client) waitReady(ctx context.Context, name string, generation int64) (*run.Service, error) {
	ticker := time.NewTicker(c.pollInterval)
	defer ticker.Stop()

	id := path.Base(name)
	for {
		service, err := c.services.Get(name).Context(ctx).Do()
		switch {
		case isNotFound(err):
			// A service that was only just created may not be readable yet.
		case err != nil && ctx.Err() == nil:
			return nil, fmt.Errorf("reading back cloud run service %s: %w", id, err)
		case err != nil:
			// This means context is dead, will be handled in select.
		default:
			// Do not consider earlier generations.
			if status := service.Status; status != nil && status.ObservedGeneration >= generation {
				switch condition := readyOf(status.Conditions); {
				case condition == nil:
					// Reconciliation has not reported anything yet.
				case condition.Status == conditionTrue:
					return service, nil
				case condition.Status == conditionFalse:
					return nil, fmt.Errorf("cloud run could not bring up %s: %s", id, describe(condition))
				}
			}
		}

		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("waiting for cloud run service %s to become ready: %w", id, ctx.Err())
		case <-ticker.C:
		}
	}
}

// waitGone polls the service until it is no longer there. Deletion is
// asynchronous, and the Driver reports the workload as destroyed once we
// return.
func (c *Client) waitGone(ctx context.Context, name string) error {
	ticker := time.NewTicker(c.pollInterval)
	defer ticker.Stop()

	id := path.Base(name)
	for {
		_, err := c.services.Get(name).Context(ctx).Do()
		switch {
		case isNotFound(err):
			return nil
		case err != nil && ctx.Err() == nil:
			return fmt.Errorf("reading back cloud run service %s: %w", id, err)
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for cloud run service %s to be deleted: %w", id, ctx.Err())
		case <-ticker.C:
		}
	}
}

// result reports what the Driver publishes as the workload's outputs.
func (c *Client) result(service *run.Service) Result {
	out := Result{
		Project: c.target.Project,
		Region:  c.target.Region,
		Service: service.Metadata.Name,
	}
	if service.Status != nil {
		out.URL = service.Status.Url
		out.Revision = service.Status.LatestReadyRevisionName
	}
	return out
}

// forDeployment returns the manifest as a request body: in the project's
// namespace, and without the status, which is the server's to report. The
// caller keeps its own copy untouched, since that is what -output shows.
func (c *Client) forDeployment(manifest *run.Service) (*run.Service, error) {
	if manifest == nil || manifest.Metadata == nil || manifest.Metadata.Name == "" {
		return nil, errors.New("the service manifest has no metadata.name")
	}
	metadata := *manifest.Metadata
	metadata.Namespace = c.target.Project

	desired := *manifest
	desired.Metadata = &metadata
	desired.Status = nil
	return &desired, nil
}

// generationOf reports the sequence number the server gave the write we just
// made, which is what tells a fresh status apart from a leftover one.
func generationOf(service *run.Service) int64 {
	if service == nil || service.Metadata == nil {
		return 0
	}
	return service.Metadata.Generation
}

// readyOf picks the Ready condition out of a service's status.
func readyOf(conditions []*run.GoogleCloudRunV1Condition) *run.GoogleCloudRunV1Condition {
	for _, condition := range conditions {
		if condition != nil && condition.Type == readyCondition {
			return condition
		}
	}
	return nil
}

// describe renders a failed condition as something worth putting in front of
// whoever triggered the deployment.
func describe(condition *run.GoogleCloudRunV1Condition) string {
	switch {
	case condition.Message != "" && condition.Reason != "":
		return fmt.Sprintf("%s (%s)", condition.Message, condition.Reason)
	case condition.Message != "":
		return condition.Message
	case condition.Reason != "":
		return condition.Reason
	default:
		return "no reason given"
	}
}

// isNotFound reports whether err is the Admin API saying the service is not
// there.
func isNotFound(err error) bool {
	var apiErr *googleapi.Error
	return errors.As(err, &apiErr) && apiErr.Code == http.StatusNotFound
}
