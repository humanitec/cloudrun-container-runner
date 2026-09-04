package cloudrun

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
	run "google.golang.org/api/run/v1"

	"github.com/humanitec/cloudrun-container-runner/internal/google"
)

// adminServer stands in for the Cloud Run Admin API, so that a deployment can
// be exercised without Google credentials while still going over the wire the
// generated client actually builds.
//
// Reads are scripted call by call, the last entry answering every call past the
// end of the script, which is what lets a test spell out how a reconciliation
// unfolds. Writes are recorded for the test to assert on.
type adminServer struct {
	gets     []reply
	getCalls int

	created  *run.Service
	replaced *run.Service
	deleted  string

	deleteStatus int
}

// reply is one scripted answer. A zero status means 200 and the service is the
// body; anything else is rendered as the API's error envelope. A delay holds
// the response back, which is how a read is made to outlive its context.
type reply struct {
	status  int
	service *run.Service
	delay   time.Duration
}

func (a *adminServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		next := a.gets[min(a.getCalls, len(a.gets)-1)]
		a.getCalls++
		if next.delay > 0 {
			select {
			case <-time.After(next.delay):
			case <-r.Context().Done():
				return
			}
		}
		respond(w, next.status, next.service)
	case http.MethodPost:
		a.created = decodeService(r)
		respond(w, 0, withGeneration(a.created, 1))
	case http.MethodPut:
		a.replaced = decodeService(r)
		respond(w, 0, withGeneration(a.replaced, 4))
	case http.MethodDelete:
		a.deleted = r.URL.Path
		respond(w, a.deleteStatus, &run.Status{})
	default:
		respond(w, http.StatusMethodNotAllowed, nil)
	}
}

func respond(w http.ResponseWriter, status int, body any) {
	if status == 0 {
		status = http.StatusOK
	}
	if status != http.StatusOK {
		body = map[string]any{"error": map[string]any{
			"code":    status,
			"message": http.StatusText(status),
		}}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func decodeService(r *http.Request) *run.Service {
	var service run.Service
	if err := json.NewDecoder(r.Body).Decode(&service); err != nil {
		return nil
	}
	return &service
}

// clientWith returns a Client talking to admin over a real HTTP round trip,
// polling fast enough not to slow the tests down.
func clientWith(t *testing.T, admin *adminServer) *Client {
	t.Helper()

	server := httptest.NewServer(admin)
	t.Cleanup(server.Close)

	api, err := run.NewService(t.Context(),
		option.WithEndpoint(server.URL+"/"),
		option.WithoutAuthentication(),
	)
	require.NoError(t, err)

	return &Client{
		target:       google.Target{Project: "my-project", Region: "europe-west1"},
		services:     api.Namespaces.Services,
		pollInterval: time.Millisecond,
	}
}

func withGeneration(service *run.Service, generation int64) *run.Service {
	if service == nil {
		service = &run.Service{}
	}
	metadata := run.ObjectMeta{}
	if service.Metadata != nil {
		metadata = *service.Metadata
	}
	metadata.Generation = generation

	copied := *service
	copied.Metadata = &metadata
	return &copied
}

// serviceAt is a service the server has reconciled up to generation, with the
// Ready condition in the given state.
func serviceAt(generation int64, readyStatus string, condition *run.GoogleCloudRunV1Condition) *run.Service {
	if condition == nil {
		condition = &run.GoogleCloudRunV1Condition{Type: readyCondition, Status: readyStatus}
	}
	return &run.Service{
		Metadata: &run.ObjectMeta{Name: "hello-world-dev", Generation: generation},
		Status: &run.ServiceStatus{
			ObservedGeneration:      generation,
			Conditions:              []*run.GoogleCloudRunV1Condition{condition},
			Url:                     "https://hello-world-dev-abc.a.run.app",
			LatestReadyRevisionName: "hello-world-dev-00001-abc",
		},
	}
}

// manifest is what the converter hands over: a service with no namespace, since
// nothing upstream of the client knows the deployment target, and an empty
// status left behind by the Knative types it was translated from.
func manifest() *run.Service {
	return &run.Service{
		ApiVersion: "serving.knative.dev/v1",
		Kind:       "Service",
		Metadata:   &run.ObjectMeta{Name: "hello-world-dev"},
		Spec: &run.ServiceSpec{
			Template: &run.RevisionTemplate{
				Spec: &run.RevisionSpec{
					Containers: []*run.Container{{Image: "busybox:latest"}},
				},
			},
		},
		Status: &run.ServiceStatus{},
	}
}

func TestDeployCreatesAServiceThatIsNotThereYet(t *testing.T) {
	admin := &adminServer{gets: []reply{
		{status: http.StatusNotFound},               // the existence check
		{service: serviceAt(1, conditionTrue, nil)}, // the first poll
	}}

	result, err := clientWith(t, admin).Deploy(t.Context(), manifest())
	require.NoError(t, err)

	require.NotNil(t, admin.created, "the service should have been created")
	assert.Nil(t, admin.replaced, "an absent service cannot be replaced")
	// The namespace is the project, and it has to survive onto the wire.
	assert.Equal(t, "my-project", admin.created.Metadata.Namespace)
	assert.Equal(t, Result{
		Project:  "my-project",
		Region:   "europe-west1",
		Service:  "hello-world-dev",
		URL:      "https://hello-world-dev-abc.a.run.app",
		Revision: "hello-world-dev-00001-abc",
	}, result)
}

func TestDeployReplacesAServiceThatAlreadyExists(t *testing.T) {
	admin := &adminServer{gets: []reply{
		{service: serviceAt(3, conditionTrue, nil)}, // the existence check
		{service: serviceAt(4, conditionTrue, nil)}, // the first poll
	}}

	_, err := clientWith(t, admin).Deploy(t.Context(), manifest())
	require.NoError(t, err)

	require.NotNil(t, admin.replaced, "the service should have been replaced")
	assert.Nil(t, admin.created, "an existing service should not be created again")
	// Status is the server's to report, so it has no business in the request.
	assert.Nil(t, admin.replaced.Status)
}

// The Ready condition left over from the revision this deployment replaces says
// nothing about whether this one worked, and taking it at face value would
// report a failed deployment as a success.
func TestDeployIgnoresAStatusFromAnEarlierGeneration(t *testing.T) {
	admin := &adminServer{gets: []reply{
		{service: serviceAt(3, conditionTrue, nil)},  // the existence check
		{service: serviceAt(3, conditionTrue, nil)},  // stale: generation 4 is what we wrote
		{service: serviceAt(4, conditionFalse, nil)}, // and it turns out it failed
	}}

	_, err := clientWith(t, admin).Deploy(t.Context(), manifest())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "could not bring up hello-world-dev")
}

func TestDeployWaitsWhileReconciliationIsStillRunning(t *testing.T) {
	admin := &adminServer{gets: []reply{
		{status: http.StatusNotFound},
		{service: serviceAt(1, "Unknown", nil)},
		{service: serviceAt(1, "Unknown", nil)},
		{service: serviceAt(1, conditionTrue, nil)},
	}}

	_, err := clientWith(t, admin).Deploy(t.Context(), manifest())
	require.NoError(t, err)
	assert.Equal(t, 4, admin.getCalls)
}

func TestDeployReportsWhyCloudRunRefusedTheService(t *testing.T) {
	admin := &adminServer{gets: []reply{
		{status: http.StatusNotFound},
		{service: serviceAt(1, conditionFalse, &run.GoogleCloudRunV1Condition{
			Type:    readyCondition,
			Status:  conditionFalse,
			Reason:  "ContainerMissing",
			Message: "The user-provided container failed to start and listen on the port",
		})},
	}}

	_, err := clientWith(t, admin).Deploy(t.Context(), manifest())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to start and listen on the port")
	assert.Contains(t, err.Error(), "ContainerMissing")
}

func TestDeployGivesUpBetweenPolls(t *testing.T) {
	admin := &adminServer{gets: []reply{
		{status: http.StatusNotFound},
		{service: serviceAt(1, "Unknown", nil)},
	}}

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()

	_, err := clientWith(t, admin).Deploy(ctx, manifest())

	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Contains(t, err.Error(), "to become ready")
}

// Running out of time aborts the read that is in flight at the moment, and that
// transport failure has to be reported as the timeout it is rather than as a
// service that could not be read.
func TestDeployGivesUpWhenAReadOutlivesTheContext(t *testing.T) {
	admin := &adminServer{gets: []reply{
		{status: http.StatusNotFound},
		{service: serviceAt(1, conditionTrue, nil), delay: time.Minute},
	}}

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	_, err := clientWith(t, admin).Deploy(ctx, manifest())

	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Contains(t, err.Error(), "to become ready")
	assert.NotContains(t, err.Error(), "reading back")
}

func TestDeployReportsAFailureToRead(t *testing.T) {
	admin := &adminServer{gets: []reply{{status: http.StatusForbidden}}}

	_, err := clientWith(t, admin).Deploy(t.Context(), manifest())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading cloud run service hello-world-dev")
}

func TestDeleteWaitsForTheServiceToGo(t *testing.T) {
	admin := &adminServer{gets: []reply{
		{service: serviceAt(1, conditionTrue, nil)}, // still there
		{status: http.StatusNotFound},               // gone
	}}

	require.NoError(t, clientWith(t, admin).Delete(t.Context(), "hello-world-dev"))
	assert.Equal(t, "/apis/serving.knative.dev/v1/namespaces/my-project/services/hello-world-dev", admin.deleted)
}

// The Driver runs destroy for workloads whose deployment never got as far as
// creating a service, so a missing service is the expected end state, not a
// failure.
func TestDeleteIsIdempotent(t *testing.T) {
	admin := &adminServer{
		deleteStatus: http.StatusNotFound,
		gets:         []reply{{status: http.StatusNotFound}},
	}

	assert.NoError(t, clientWith(t, admin).Delete(t.Context(), "hello-world-dev"))
}

func TestDeleteReportsAnythingOtherThanAMissingService(t *testing.T) {
	admin := &adminServer{deleteStatus: http.StatusForbidden}

	err := clientWith(t, admin).Delete(t.Context(), "hello-world-dev")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "deleting cloud run service hello-world-dev")
}

func TestForDeploymentFillsInTheNamespaceAndDropsTheStatus(t *testing.T) {
	client := clientWith(t, &adminServer{})
	original := manifest()

	desired, err := client.forDeployment(original)
	require.NoError(t, err)

	assert.Equal(t, "my-project", desired.Metadata.Namespace)
	assert.Equal(t, "hello-world-dev", desired.Metadata.Name)
	assert.Nil(t, desired.Status, "status is the server's to report")
	assert.Equal(t, "busybox:latest", desired.Spec.Template.Spec.Containers[0].Image)

	// -output shows the caller's manifest, so preparing a request must not
	// reach back into it.
	assert.Empty(t, original.Metadata.Namespace)
	assert.NotNil(t, original.Status)
}

func TestForDeploymentRejectsAManifestWithoutAName(t *testing.T) {
	client := clientWith(t, &adminServer{})

	_, err := client.forDeployment(&run.Service{Kind: "Service"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "metadata.name")
}

// The v1 API is only served regionally; the global host rejects namespaces/...
// resource names.
func TestEndpointIsRegional(t *testing.T) {
	assert.Equal(t, "https://europe-west1-run.googleapis.com/", endpoint("europe-west1"))
}

func TestIsNotFoundOnlyMatchesA404(t *testing.T) {
	assert.True(t, isNotFound(&googleapi.Error{Code: http.StatusNotFound}))
	assert.False(t, isNotFound(&googleapi.Error{Code: http.StatusForbidden}))
	assert.False(t, isNotFound(context.DeadlineExceeded))
	assert.False(t, isNotFound(nil))
}
