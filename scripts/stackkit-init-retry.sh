#!/bin/sh

set -eu

if [ "$#" -lt 4 ]; then
  echo "usage: stackkit-init-retry.sh <workspace> <log-path> <stackkit-binary> <init-args...>" >&2
  exit 64
fi

workspace=$1
log_path=$2
shift 2

max_attempts=${STACKKIT_INIT_MAX_ATTEMPTS:-3}
retry_delay_seconds=${STACKKIT_INIT_RETRY_DELAY_SECONDS:-1}

case "$max_attempts" in
  1|2|3|4|5) ;;
  *)
    echo "STACKKIT_INIT_MAX_ATTEMPTS must be an integer from 1 through 5" >&2
    exit 64
    ;;
esac

case "$retry_delay_seconds" in
  ''|*[!0-9]*)
    echo "STACKKIT_INIT_RETRY_DELAY_SECONDS must be a non-negative integer" >&2
    exit 64
    ;;
esac

mkdir -p "$workspace" "$(dirname "$log_path")"

attempt=1
while :; do
  if (cd "$workspace" && "$@") >"$log_path" 2>&1; then
    exit 0
  else
    status=$?
  fi

  # Trust and integrity failures are never transport retries, even if a nested
  # diagnostic happens to mention a network symptom as additional context.
  if grep -Eiq \
    'digest mismatch|sha256 mismatch|attestation|trusted root|release receipt|receipt .* mismatch|current StackKit executable differs|executable identity|signature verification' \
    "$log_path"; then
    exit "$status"
  fi

  if ! grep -Eiq \
    'context deadline exceeded|Client\.Timeout|TLS handshake timeout|connection reset by peer|unexpected EOF|temporary failure in name resolution|connection timed out' \
    "$log_path"; then
    exit "$status"
  fi

  if [ "$attempt" -ge "$max_attempts" ]; then
    exit "$status"
  fi

  echo "StackKits init transport attempt ${attempt}/${max_attempts} failed; retrying the immutable verified release fetch" >&2
  if [ "$retry_delay_seconds" -gt 0 ]; then
    sleep "$retry_delay_seconds"
  fi
  attempt=$((attempt + 1))
done
