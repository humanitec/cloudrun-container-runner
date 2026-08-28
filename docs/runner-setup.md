# Preparing the runner cluster and GCP project

The Runner is a short-lived Kubernetes Job that the Humanitec Operator creates
on your cluster. That Job authenticates to Google Cloud and deploys the Score
workload to Cloud Run.

Two identities are involved, and they are easy to confuse:

| Identity | Where it lives | What it is for |
| --- | --- | --- |
| Runner Kubernetes service account | runner cluster | the identity the Job Pod runs as |
| Deployer Google service account | GCP project | the identity that calls the Cloud Run API |
| Cloud Run runtime service account | GCP project | the identity your *workload* runs as, once deployed |

Steps 1 and 2 are always required. Step 3 links the first two, and you pick
either the Workload Identity route (recommended) or a service account key.

Throughout, these are the values used by `examples/resource-definition.yaml`:

```bash
export NAMESPACE=humanitec-runner
export KSA_NAME=humanitec-runner
export GCP_PROJECT_ID=my-gcp-project
export CLUSTER_NAME=my-runner-cluster
export CLUSTER_LOCATION=europe-west1
export DEPLOYER_SA=cloudrun-deployer
```

## 1. Runner namespace, service account and RBAC

Every Driver whose name contains *Container* or *Runner* needs a dedicated
namespace and service account for the Job, plus permission to write the outputs
the Driver reads back.

```bash
kubectl create namespace "${NAMESPACE}"

kubectl create serviceaccount "${KSA_NAME}" \
  --namespace "${NAMESPACE}"

kubectl create role humanitec-runner \
  --namespace "${NAMESPACE}" \
  --verb=create \
  --resource=secrets,configmaps

kubectl create rolebinding humanitec-runner \
  --namespace "${NAMESPACE}" \
  --role=humanitec-runner \
  --serviceaccount="${NAMESPACE}:${KSA_NAME}"
```

The same thing as a manifest, if you keep cluster state in Git:

```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: humanitec-runner
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: humanitec-runner
  namespace: humanitec-runner
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  namespace: humanitec-runner
  name: humanitec-runner
rules:
  - apiGroups: [""]
    resources: ["secrets", "configmaps"]
    verbs: ["create"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: humanitec-runner
  namespace: humanitec-runner
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: humanitec-runner
subjects:
  - kind: ServiceAccount
    name: humanitec-runner
    namespace: humanitec-runner
```

The namespace and service account names must match `job.namespace` and
`job.service_account` in the Resource Definition. `job.namespace` defaults to
`humanitec-runner` if omitted.

**No further RBAC is needed** when the Operator and the runner Job share a
cluster, which is the default for `humanitec/container-builtin`: the Operator
installation already carries the permissions to manage runner Jobs. Only if you
place the Job on a *separate* cluster (via `secret_refs.cluster.kubeconfig`) do
you also need a Role granting `batch/jobs`, `configmaps`, `secrets`, `pods`,
`events` and `pods/log` access there, bound to the identity the Operator uses to
reach that cluster.

## 2. Deployer Google service account

Create the account that will call the Cloud Run API:

```bash
gcloud iam service-accounts create "${DEPLOYER_SA}" \
  --project="${GCP_PROJECT_ID}" \
  --display-name="Humanitec Cloud Run runner"

export DEPLOYER_SA_EMAIL="${DEPLOYER_SA}@${GCP_PROJECT_ID}.iam.gserviceaccount.com"
```

Grant it what deploying a Cloud Run service requires:

```bash
# Create and update Cloud Run services.
gcloud projects add-iam-policy-binding "${GCP_PROJECT_ID}" \
  --member="serviceAccount:${DEPLOYER_SA_EMAIL}" \
  --role="roles/run.developer"
```

Secret Manager needs two more grants. The Runner writes a workload's secret
values itself, before it deploys, so the deployer has to be able to create
secrets, add versions, and read back the newest version to tell whether anything
actually changed. It also lists and deletes them, for the cleanup that follows a
deployment — which happens on every deployment and every destroy, so these
grants are not conditional on a particular workload holding secrets.

Those are two separate concerns, so they are two roles. Grants are additive, and
the pair covers writing and reading without the wider reach of
`roles/secretmanager.admin`:

```bash
# Create secrets, add versions, and list and delete them during cleanup.
gcloud projects add-iam-policy-binding "${GCP_PROJECT_ID}" \
  --member="serviceAccount:${DEPLOYER_SA_EMAIL}" \
  --role="roles/secretmanager.editor"

# Read back the newest version to compare it with the incoming value.
gcloud projects add-iam-policy-binding "${GCP_PROJECT_ID}" \
  --member="serviceAccount:${DEPLOYER_SA_EMAIL}" \
  --role="roles/secretmanager.secretAccessor"
```

Both are needed. `roles/secretmanager.editor` alone grants `versions.add` but
not `versions.access`, so the Runner would create the secret and then fail on
the comparison read. `versions.get` is not a substitute: it returns a version's
metadata, not its payload.

If the workload image lives in a private Artifact Registry repository, the
deployer also needs to read it. Grant this on the repository rather than
project-wide, and skip it entirely for public images such as the
`us-docker.pkg.dev/cloudrun/container/hello` used in the examples:

```bash
gcloud artifacts repositories add-iam-policy-binding REPOSITORY \
  --location=REPO_LOCATION \
  --project="${GCP_PROJECT_ID}" \
  --member="serviceAccount:${DEPLOYER_SA_EMAIL}" \
  --role="roles/artifactregistry.reader"
```

Note that the deployer is not what fetches the image: Cloud Run imports it using
its own service agent, `service-PROJECT_NUMBER@serverless-robot-prod.iam.gserviceaccount.com`,
which already has access to repositories in the same project. If the image lives
in a *different* project, that service agent needs the same
`roles/artifactregistry.reader` grant on the repository too, or the deployment
succeeds and then fails to pull.

Deploying a service also means assigning it a runtime service account, which
requires acting as that account. Grant this on the *runtime* account, not
project-wide:

```bash
# RUNTIME_SA_EMAIL is whatever the deployed service runs as. If the workload
# does not name one, Cloud Run uses the default compute service account, which
# is keyed by project *number* rather than project ID.
export GCP_PROJECT_NUMBER=$(gcloud projects describe "${GCP_PROJECT_ID}" \
  --format="value(projectNumber)")
export RUNTIME_SA_EMAIL="${GCP_PROJECT_NUMBER}-compute@developer.gserviceaccount.com"

gcloud iam service-accounts add-iam-policy-binding "${RUNTIME_SA_EMAIL}" \
  --project="${GCP_PROJECT_ID}" \
  --member="serviceAccount:${DEPLOYER_SA_EMAIL}" \
  --role="roles/iam.serviceAccountUser"
```

Skipping this yields `Permission 'iam.serviceaccounts.actAs' denied on service
account ...-compute@developer.gserviceaccount.com` at deploy time.

If the workload uses secrets, the **runtime** account needs to read them. Cloud
Run checks this when the service is deployed, so a missing binding fails the
deployment rather than the running service.

Grant this on the project rather than per secret. The Runner derives each secret
name from the workload, the container, and the variable or file it belongs to,
so the names are not known ahead of time, and a secret the Runner has just
created carries no bindings of its own:

```bash
gcloud projects add-iam-policy-binding "${GCP_PROJECT_ID}" \
  --member="serviceAccount:${RUNTIME_SA_EMAIL}" \
  --role="roles/secretmanager.secretAccessor"
```

Secret Manager is the only secret store this Runner supports; the converter
rejects references to any other store.

Secrets the Runner creates are labelled `managed-by=score2cloudrun` and
`service=<the Cloud Run service name>`, are replicated to the Cloud Run region in
`CLOUDSDK_RUN_REGION`, and gain a new version only when their value actually
changes.

Those two labels are also how the Runner finds them again. Once a deployment is
live it deletes the secrets it holds for that service which the new revision no
longer references, and destroying the workload deletes all of them. The cleanup
waits for Cloud Run to accept the revision first, so a deployment that fails
leaves the secrets the running revision reads exactly where they are.

```bash
# Everything the Runner holds, then one service's share of it.
gcloud secrets list --filter="labels.managed-by=score2cloudrun"
gcloud secrets list --filter="labels.managed-by=score2cloudrun AND labels.service=hello-world-dev"
```

## 3a. Link the two with Workload Identity (recommended)

No keys to store or rotate. The Job Pod gets Google credentials from the GKE
metadata server, and the Runner picks them up through Application Default
Credentials without any explicit login step.

Enable Workload Identity on the cluster, and on the node pool for Standard
clusters (Autopilot has it on already):

```bash
gcloud container clusters update "${CLUSTER_NAME}" \
  --location="${CLUSTER_LOCATION}" \
  --workload-pool="${GCP_PROJECT_ID}.svc.id.goog"

gcloud container node-pools update NODEPOOL_NAME \
  --cluster="${CLUSTER_NAME}" \
  --location="${CLUSTER_LOCATION}" \
  --workload-metadata=GKE_METADATA
```

Allow the Kubernetes service account from step 1 to impersonate the deployer
account, then annotate it. Both halves are required — the binding alone does
nothing:

```bash
gcloud iam service-accounts add-iam-policy-binding "${DEPLOYER_SA_EMAIL}" \
  --project="${GCP_PROJECT_ID}" \
  --role="roles/iam.workloadIdentityUser" \
  --member="serviceAccount:${GCP_PROJECT_ID}.svc.id.goog[${NAMESPACE}/${KSA_NAME}]"

kubectl annotate serviceaccount "${KSA_NAME}" \
  --namespace "${NAMESPACE}" \
  iam.gke.io/gcp-service-account="${DEPLOYER_SA_EMAIL}"
```

With this route the Resource Definition must **not** set
`GOOGLE_APPLICATION_CREDENTIALS` and must **not** carry a
`secret_refs.files.credentials.json` entry. Application Default Credentials
prefers that file whenever it is named, and only falls through to the metadata
server when it is not.

Verify before wiring up a deployment. This throwaway Pod uses the Cloud SDK
image rather than the Runner's, which carries no `gcloud`:

```bash
kubectl run wi-test -n "${NAMESPACE}" --rm -it --restart=Never \
  --overrides="{\"spec\":{\"serviceAccountName\":\"${KSA_NAME}\"}}" \
  --image=google/cloud-sdk:alpine -- gcloud auth list
```

It should print the deployer account as active. "No credentialed accounts" means
the annotation or the IAM binding is missing.

## 3b. Or: a service account key

Use this when the runner cluster is not GKE, or Workload Identity is not
available. It is a long-lived credential, so treat it accordingly.

```bash
gcloud iam service-accounts keys create key.json \
  --iam-account="${DEPLOYER_SA_EMAIL}"
```

Put the contents into your secret store, then delete the local copy. The
Operator reads it from there and writes it into the shared directory:

```yaml
driver_inputs:
  values:
    job:
      variables:
        GOOGLE_APPLICATION_CREDENTIALS: credentials.json
  secret_refs:
    files:
      credentials.json:
        store: my-secret-store
        ref: gcp/cloudrun-deployer-key
```

The path is relative, so it resolves inside `job.shared_directory`, which the
Runner enters before it reads anything. Application Default Credentials accepts
both a service account key and an `external_account` configuration here.

## 4. Point the Resource Definition at it

The pieces above correspond to these inputs:

```yaml
driver_inputs:
  values:
    job:
      namespace: humanitec-runner        # step 1
      service_account: humanitec-runner  # step 1
      image: ghcr.io/humanitec/cloudrun-container-runner:latest
      shared_directory: /humanitec
      variables:
        CLOUDSDK_CORE_PROJECT: my-gcp-project   # step 2
        CLOUDSDK_RUN_REGION: europe-west1
```

`CLOUDSDK_CORE_PROJECT` and `CLOUDSDK_RUN_REGION` keep `gcloud`'s names, even
though the Runner no longer shells out to it, so that Resource Definitions
written against earlier versions go on working.

See `examples/resource-definition.yaml` for the full file.

## Notes

The Runner image runs as UID/GID 1000, matching the Driver's default
`securityContext` (`runAsNonRoot`). Nothing to configure — but if you supply
your own `pod_template`, keep it non-root.

The image is `distroless/static`: the `score2cloudrun` binary, a certificate
bundle and nothing else. It calls the Cloud Run Admin and Secret Manager APIs
directly, so there is no `gcloud` and no shell in it. `kubectl exec` into a
running Job therefore gets you nowhere; `kubectl logs` is the way in.

Deployment failures surface in the Orchestrator with the reason the Runner wrote
to `ERROR_FILE`. For a service that deploys and then fails to come up, that is
Cloud Run's own `Ready` condition, message and reason — the same text
`gcloud run services describe` would show. For anything else, `kubectl logs` on
the Job Pod in the runner namespace has the full output.
