#!/bin/bash
#
# Runner entrypoint for the Humanitec Container Driver.
#
# The driver runs this container as the `runner` container of a Kubernetes Job
# and talks to it purely through environment variables:
#
#   ACTION                create | destroy
#   SCRIPTS_DIRECTORY     working directory shared with the driver sidecars
#   RESOURCE_INPUTS_FILE  JSON resource inputs, carrying the Score workload
#   OUTPUTS_FILE          where to write non-sensitive outputs, as JSON
#   SECRET_OUTPUTS_FILE   where to write sensitive outputs, as JSON
#   ERROR_FILE            where to write a human-readable failure reason
#
# A non-zero exit marks the deployment as failed; the Orchestrator then shows
# the contents of ERROR_FILE in the deployment error message.
#
# The deployment target is taken from gcloud's own environment variables, set
# through `job.variables` in the Resource Definition:
#
#   CLOUDSDK_CORE_PROJECT   the GCP project
#   CLOUDSDK_RUN_REGION     the Cloud Run region
#
# Credentials are optional. On a GKE runner cluster with Workload Identity the
# pod already has an identity and nothing needs to be supplied. Otherwise the
# Resource Definition carries the service account key as a secret file and its
# path as an ordinary variable:
#
#   values:
#     job:
#       variables:
#         GOOGLE_APPLICATION_CREDENTIALS: credentials.json
#   secret_refs:
#     files:
#       credentials.json:
#         store: my-secret-store
#         ref: gcp/cloudrun-deployer-key
#
# The path is relative, so it only resolves once this script has entered
# SCRIPTS_DIRECTORY. See authenticate() below.

set -euo pipefail

log() { printf '[runner] %s\n' "$*" >&2; }

# die records the reason in ERROR_FILE, where the Orchestrator picks it up, and fails the Job.
die() {
  log "ERROR: $*"
  printf '%s\n' "$*" >>"${ERROR_FILE:-/dev/null}"
  exit 1
}

require_env() {
  local name
  for name in "$@"; do
    [[ -n "${!name:-}" ]] || die "required environment variable ${name} is not set"
  done
}

# run executes a command, echoes its output to the Job log, and on failure forwards that output to ERROR_FILE.
run() {
  local output rc=0

  # `|| rc=` keeps set -e from aborting before the failure can be reported.
  output="$("$@" 2>&1)" || rc=$?
  printf '%s\n' "${output}" >&2

  ((rc == 0)) || die "$(printf '%s failed with exit code %d:\n%s' "$*" "${rc}" "${output}")"
}

write_json() {
  [[ -n "$1" ]] || return 0
  printf '%s\n' "$2" >"$1"
}

# Without a credentials file, gcloud falls back to the metadata server. That's how Workload Identity works
# on a GKE runner cluster: the pod's K8s service account is already bound to a Google service account.
#
# With a credentials file, --cred-file covers both a service account key and an `external_account` config.
authenticate() {
  if [[ -z "${GOOGLE_APPLICATION_CREDENTIALS:-}" ]]; then
    log "no credentials file supplied, relying on the pod's metadata server (workload identity)"
    return 0
  fi

  [[ -r "${GOOGLE_APPLICATION_CREDENTIALS}" ]] ||
    die "GOOGLE_APPLICATION_CREDENTIALS is ${GOOGLE_APPLICATION_CREDENTIALS}, which is not readable"

  log "authenticating with ${GOOGLE_APPLICATION_CREDENTIALS}"
  run gcloud auth login --cred-file="${GOOGLE_APPLICATION_CREDENTIALS}" --quiet
}

create() {
  local service="$1"

  log "deploying ${service} to Cloud Run"
  # Synchronous call, waits for service's Ready condition
  run gcloud run services replace service.yaml --quiet

  local described
  described="$(gcloud run services describe "${service}" --format=json)" ||
    die "deployed ${service} but could not read its state back"

  write_json "${OUTPUTS_FILE:-}" "$(jq \
    --arg project "${CLOUDSDK_CORE_PROJECT}" \
    --arg region "${CLOUDSDK_RUN_REGION}" \
    --arg service "${service}" \
    '{
       project:  $project,
       region:   $region,
       service:  $service,
       url:      (.status.url // ""),
       revision: (.status.latestReadyRevisionName // "")
     }' <<<"${described}")"
  write_json "${SECRET_OUTPUTS_FILE:-}" '{}'

  log "deployed ${service}"
}

destroy() {
  local service="$1"

  # Deletion has to be idempotent: if already destroyed doesn't throw an error.
  if gcloud run services describe "${service}" --format='value(metadata.name)' >/dev/null 2>&1; then
    log "deleting ${service} from Cloud Run"
    run gcloud run services delete "${service}" --quiet
    log "deleted ${service}"
  else
    log "${service} does not exist, nothing to delete"
  fi

  write_json "${OUTPUTS_FILE:-}" '{}'
  write_json "${SECRET_OUTPUTS_FILE:-}" '{}'
}

main() {
  require_env ACTION RESOURCE_INPUTS_FILE CLOUDSDK_CORE_PROJECT CLOUDSDK_RUN_REGION

  [[ -r "${RESOURCE_INPUTS_FILE}" ]] ||
    die "RESOURCE_INPUTS_FILE is ${RESOURCE_INPUTS_FILE}, which is not readable"

  # The driver expects the runner to work inside the shared directory, and any
  # relative path it hands over resolves against it.
  cd "${SCRIPTS_DIRECTORY:-/tmp}" ||
    die "cannot enter working directory ${SCRIPTS_DIRECTORY:-/tmp}"

  authenticate

  log "converting the Score workload into a Cloud Run service manifest"
  score2cloudrun "${RESOURCE_INPUTS_FILE}" >service.yaml ||
    die "score2cloudrun could not convert the Score workload into a Cloud Run service"

  local service
  service="$(yq '.metadata.name // ""' service.yaml)"
  [[ -n "${service}" ]] || die "the generated manifest has no metadata.name"

  case "${ACTION}" in
    create) create "${service}" ;;
    destroy) destroy "${service}" ;;
    *) die "unsupported ACTION '${ACTION}', expected 'create' or 'destroy'" ;;
  esac
}

main "$@"
