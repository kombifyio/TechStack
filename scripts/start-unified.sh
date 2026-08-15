#!/usr/bin/env bash

set -euo pipefail

public_port="${PORT:-5260}"
stackkits_port="${TECHSTACK_STACKKITS_INTERNAL_PORT:-8082}"
data_dir="${TECHSTACK_DATA_DIR:-/data}"
pb_data_dir="${TECHSTACK_PB_DATA_DIR:-${data_dir}/pb_data}"
stackkits_log_dir="${STACKKITS_LOG_DIR:-${pb_data_dir}/stackkits/logs}"

if [[ "$(id -u)" == "0" && "${TECHSTACK_RUN_AS_ROOT:-}" != "1" ]]; then
  mkdir -p "${data_dir}" "${pb_data_dir}" "${stackkits_log_dir}"
  chown techstack:techstack "${data_dir}" "${pb_data_dir}" "$(dirname "${stackkits_log_dir}")" "${stackkits_log_dir}"
  if ! find "${data_dir}" -maxdepth "${TECHSTACK_DATA_CHOWN_MAX_DEPTH:-3}" -exec chown techstack:techstack {} +; then
    echo "[unified] warning: data ownership refresh was incomplete; continuing startup" >&2
  fi
  exec su-exec techstack "$0" "$@"
fi

# Render pre-deploy commands are executed inside the same Docker image. Accept
# only the explicit database-authority command here; normal runtime startup
# remains the no-argument path below. Shell wrappers are passed through so the
# command also works when the platform preserves Docker ENTRYPOINT semantics.
case "${1:-}" in
  provider-control-bootstrap|version)
    exec /app/techstack "$@"
    ;;
  /app/techstack|/bin/sh|/bin/bash|sh|bash)
    exec "$@"
    ;;
esac

# Control-plane state is Postgres-first (PocketBase retirement). The backend
# fails closed without DATABASE_URL; require it before launching anything.
if [[ -z "${DATABASE_URL:-}" ]]; then
  echo "[unified] ERROR: DATABASE_URL is required (control-plane Postgres). PocketBase control-plane fallback removed." >&2
  exit 1
fi

# The Go runtime sizes its heap against what it believes the machine has and
# knows nothing about a cgroup limit, so in a small container it grows straight
# past the limit instead of collecting and the kernel kills it. The StackKit
# rollout allocates roughly 1.4GB across a three-second generate while keeping
# almost nothing alive (measured live heap after GC: ~1.4MB), which is exactly
# the shape GOMEMLIMIT exists for. Derive it from the cgroup so the value stays
# correct on any plan and when self-hosting, and leave an explicitly configured
# GOMEMLIMIT untouched.
if [[ -z "${GOMEMLIMIT:-}" ]]; then
  cgroup_v2_path="${TECHSTACK_CGROUP_MEMORY_MAX:-/sys/fs/cgroup/memory.max}"
  cgroup_v1_path="${TECHSTACK_CGROUP_MEMORY_LIMIT:-/sys/fs/cgroup/memory/memory.limit_in_bytes}"
  container_memory_bytes=""
  if [[ -r "${cgroup_v2_path}" ]]; then
    container_memory_bytes="$(cat "${cgroup_v2_path}" 2>/dev/null || true)"
  elif [[ -r "${cgroup_v1_path}" ]]; then
    container_memory_bytes="$(cat "${cgroup_v1_path}" 2>/dev/null || true)"
  fi
  # "max" means unlimited, and an absurdly large limit is the kernel's way of
  # saying the same thing; in both cases the default heap sizing is fine.
  if [[ "${container_memory_bytes}" =~ ^[0-9]+$ ]] && (( container_memory_bytes > 0 )) \
     && (( container_memory_bytes < 137438953472 )); then
    # Leave headroom for the Go runtime's own off-heap memory and everything
    # else in the container. A soft limit above the hard limit protects nothing.
    soft_limit=$(( container_memory_bytes * ${TECHSTACK_GOMEMLIMIT_PERCENT:-75} / 100 ))
    if (( soft_limit > 0 )); then
      export GOMEMLIMIT="${soft_limit}"
      echo "[unified] GOMEMLIMIT=${soft_limit} derived from cgroup limit ${container_memory_bytes}"
    fi
  fi
fi

echo "[unified] ports: public=${public_port} stackkits=${stackkits_port} data=${pb_data_dir}"

mkdir -p "${pb_data_dir}"

export PORT="${public_port}"
export HOST="0.0.0.0"
export TECHSTACK_PORT="${public_port}"
# The Go backend serves the embedded static SPA itself (ADR-033 OQ2 web
# convergence); there is no Node SSR process anymore. The httpx backend binds
# cfg.Server.ListenAddr (TECHSTACK_LISTEN_ADDR), so pin it to the public port.
export TECHSTACK_LISTEN_ADDR="0.0.0.0:${public_port}"

stackkits_pid=""
if command -v stackkit-server >/dev/null 2>&1; then
  embedded_stackkits_url="http://127.0.0.1:${stackkits_port}"
  if [[ "${TECHSTACK_STACKKITS_ACTIONS_EXTERNAL:-}" == "1" || "${TECHSTACK_STACKKITS_ACTIONS_EXTERNAL:-}" == "true" ]]; then
    export TECHSTACK_STACKKITS_ACTIONS_URL="${TECHSTACK_STACKKITS_ACTIONS_URL:-${embedded_stackkits_url}}"
  else
    if [[ -n "${TECHSTACK_STACKKITS_ACTIONS_URL:-}" && "${TECHSTACK_STACKKITS_ACTIONS_URL}" != "${embedded_stackkits_url}" ]]; then
      echo "[unified] overriding TECHSTACK_STACKKITS_ACTIONS_URL for embedded StackKits runtime actions"
    fi
    export TECHSTACK_STACKKITS_ACTIONS_URL="${embedded_stackkits_url}"
  fi
  export STACKKITS_RUNTIME_PROFILE="${STACKKITS_RUNTIME_PROFILE:-local}"
  export STACKKITS_ALLOW_UNAUTHENTICATED="${STACKKITS_ALLOW_UNAUTHENTICATED:-true}"
  export STACKKITS_RUNTIME_ACTION_MODE="${STACKKITS_RUNTIME_ACTION_MODE:-apply}"
  export STACKKITS_BASE_DIR="${STACKKITS_BASE_DIR:-/app/stackkits}"
  export STACKKITS_LOG_DIR="${stackkits_log_dir}"
  mkdir -p "${STACKKITS_LOG_DIR}"
  stackkit-server "--port=${stackkits_port}" "--base-dir=${STACKKITS_BASE_DIR}" --allow-unauthenticated &
  stackkits_pid=$!
fi

/app/techstack serve "--http=0.0.0.0:${public_port}" "--dir=${pb_data_dir}" &
backend_pid=$!

pids=("${backend_pid}")
if [[ -n "${stackkits_pid}" ]]; then
  pids+=("${stackkits_pid}")
fi

echo "[unified] backend=${backend_pid} stackkits=${stackkits_pid:-disabled}"

cleanup() {
  kill "${pids[@]}" 2>/dev/null || true
}

trap cleanup INT TERM EXIT

wait -n "${pids[@]}"
status=$?

# Log which process exited for debugging
if ! kill -0 "${backend_pid}" 2>/dev/null; then
  echo "[unified] backend exited (status=${status})"
fi
if [[ -n "${stackkits_pid}" ]] && ! kill -0 "${stackkits_pid}" 2>/dev/null; then
  echo "[unified] stackkits exited (status=${status})"
fi

cleanup
for pid in "${pids[@]}"; do
  wait "${pid}" 2>/dev/null || true
done

exit "${status}"
