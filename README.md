# cloudrun-container-runner

Deploy [Score](https://score.dev) workloads to Google Cloud Run from the
Humanitec Platform Orchestrator.

## What it does

You describe your app in a Score file: its container image, environment
variables, ports, and the resources it needs, like a database. This runner
takes that description and runs it on Cloud Run for you.

On each deployment it:

1. Reads the Score workload and the resource values the Orchestrator filled in.
2. Turns them into a Cloud Run service.
3. Saves any secret values in Google Secret Manager, so they never sit in plain
   text in the service.
4. Deploys the service and waits until it is running.
5. Reports the service URL and revision back to the Orchestrator.

When you remove the workload, the runner deletes the Cloud Run service and all
its secrets.

## How it runs

The runner is a small container image:

```
ghcr.io/humanitec/cloudrun-container-runner
```

You don't run it yourself. The Humanitec Operator starts it as a short-lived Kubernetes
Job in your cluster each time a workload is deployed or deleted. You connect it
to the Orchestrator with a Resource Definition that uses the
[`humanitec/container-builtin` driver](https://developer.humanitec.com/platform-orchestrator/docs/integration-and-extensions/drivers/container-builtin/).

## Getting started

1. Prepare your cluster and Google Cloud project. Follow
   [docs/runner-setup.md](docs/runner-setup.md). It covers the Kubernetes
   service account, the Google service account and its permissions, and how to
   log in to Google Cloud.
2. Apply the Resource Definition from
   [examples/resource-definition.yaml](examples/resource-definition.yaml), after
   changing the project, region and names to your own:

   ```bash
   humctl apply -f examples/resource-definition.yaml
   ```

3. Deploy a Score workload that matches it. A small one is in
   [examples/score.yaml](examples/score.yaml).

   ```bash
   humctl score deploy --class=google-cloud-run
   ```

## Settings

The runner reads these environment variables. Set them under `job.variables` in
the Resource Definition.

| Variable | What it is |
| --- | --- |
| `CLOUDSDK_CORE_PROJECT` | The Google Cloud project to deploy into. |
| `CLOUDSDK_RUN_REGION` | The Cloud Run region to deploy into. |
| `CLOUDRUN_RUNTIME_SERVICE_ACCOUNT` | The Google service account your app runs as. Optional. |
| `CLOUDRUN_SERVICE_NAME_PREFIX` | Added in front of the service name, for example `myapp-dev`. Optional. |
| `CLOUDRUN_SERVICE_NAME` | Use this exact service name instead. Optional. |
| `GOOGLE_APPLICATION_CREDENTIALS` | Path to a service account key. Leave it out if you use Workload Identity. |

## Changing the Cloud Run service

Score does not cover everything Cloud Run can do. To set something it doesn't
cover, add a `google-cloud-run` Score extension next to the workload. It has two
parts:

- `pod` changes the container setup, for example adding a volume.
- `service` changes the Cloud Run service itself, for example its annotations.

Both are patches. The runner builds the service from Score first, then merges
your patch on top.

See [examples/score.humanitec.yaml](examples/score.humanitec.yaml) for an
example. It sets scaling limits, adds an environment variable, a label.

## Limits

- One port per service. Cloud Run only exposes one.
- Secrets can only come from Google Secret Manager.

## Try it locally

You can build the Cloud Run service manifest without deploying the service. You need
Go installed.

```bash
export CLOUDSDK_CORE_PROJECT=my-gcp-project
export CLOUDSDK_RUN_REGION=europe-west1
go run ./cmd/score2cloudrun examples/resource-inputs.json
```

This writes the result to `service.yaml`. Add `-deploy` to send it to Cloud Run
as well, and `-help` to see every option.

Note: if the workload has secrets, like the example does, they are still saved to
Secret Manager in that project. So you need to be logged in to Google Cloud
(`gcloud auth application-default login`) even without `-deploy`.

## Development

```bash
go test ./...
docker build -t cloudrun-container-runner .
```

A new image is published to `ghcr.io` for every Git tag. Only stable versions,
like `1.2.0`, also update the `latest` tag.
