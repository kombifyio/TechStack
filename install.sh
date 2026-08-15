#!/bin/sh

# kombify Techstack Installer Script
# Usage: curl -fsSL https://raw.githubusercontent.com/kombifyio/TechStack/main/install.sh | sh

set -e

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m' # No Color

# Configuration
REPO="kombifyio/TechStack"
BINARY_NAME="techstack"
INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"
OPERATIONS_DIR="${TECHSTACK_OPERATIONS_DIR:-/usr/local/libexec}"
OPERATIONS_BINARY_NAME="techstack-stackkit-operations"

# MIN_SUPPORTED_VERSION is the oldest release whose install path this installer
# supports. If the public mirror's "latest" release is below it, the mirror is
# dormant/behind and we refuse to silently install a stale binary. Bump this in
# lockstep with releases that change the install/agent contract.
MIN_SUPPORTED_VERSION="${MIN_SUPPORTED_VERSION:-0.6.10}"

NO_INSTALL="${TECHSTACK_NO_INSTALL:-0}"
AS_SERVICE="${TECHSTACK_AS_SERVICE:-}"
if [ "${TECHSTACK_REGISTRATION_ONLY:-0}" = "1" ]; then
    AS_SERVICE="0"
elif [ -z "$AS_SERVICE" ]; then
    if [ -n "${KOMBI_SERVER:-}" ] && [ -n "${KOMBI_TOKEN:-}" ]; then
        AS_SERVICE="1"
    else
        AS_SERVICE="0"
    fi
fi
WORKER_REGISTERED="0"
ENROLLMENT_RESPONSE=""
EXISTING_RUNTIME_ENROLLMENT="0"
BOOTSTRAP_PHASE="initializing"

bootstrap_log() {
    _level=$1
    _message=$2
    if [ -z "${KOMBI_SERVER:-}" ] || [ -z "${KOMBI_TOKEN:-}" ] || ! command -v curl >/dev/null 2>&1; then
        return 0
    fi
    _bootstrap_server=${KOMBI_SERVER%/}
    if ! validate_control_plane_url "$_bootstrap_server" >/dev/null 2>&1; then
        return 0
    fi
    curl -sS --max-time 5 --connect-timeout 2 \
        -X POST "${_bootstrap_server}/api/v1/workers/bootstrap/logs" \
        -H "Authorization: Bearer ${KOMBI_TOKEN}" \
        -H "Content-Type: text/plain; charset=utf-8" \
        -H "X-Kombify-Log-Level: ${_level}" \
        -H "X-Kombify-Log-Phase: ${BOOTSTRAP_PHASE}" \
        --data-binary "${_message}" >/dev/null 2>&1 || true
}

bootstrap_failed() {
    _status=$?
    trap - 0
    if [ "$_status" -ne 0 ]; then
        bootstrap_log error "Installer failed during ${BOOTSTRAP_PHASE}. Inspect ${BOOTSTRAP_LOG_PATH:-/var/log/kombify-bootstrap.log} on the server for the full local transcript."
    fi
    exit "$_status"
}

bootstrap_phase() {
    BOOTSTRAP_PHASE=$1
    bootstrap_log info "$2"
}

validate_control_plane_url() {
	_server=$1
	case "$_server" in
		https://*) return 0 ;;
		http://localhost|http://localhost:*|http://localhost/*|http://127.0.0.1|http://127.0.0.1:*|http://127.0.0.1/*|http://\[::1\]|http://\[::1\]:*|http://\[::1\]/*) return 0 ;;
	esac
	_private_origin=${KOMBI_PRIVATE_LAN_HTTP_ORIGIN%/}
	if [ -n "$_private_origin" ] && [ "$_server" = "$_private_origin" ] && is_private_lan_origin "$_private_origin"; then
		return 0
	fi
	echo "${RED}The kombify control plane requires HTTPS, loopback HTTP, or the exact private LAN :5264 origin supplied by Techstack.${NC}" >&2
	return 1
}

is_private_lan_origin() {
	_origin=${1%/}
	case "$_origin" in
		http://*:5264) ;;
		*) return 1 ;;
	esac
	_host=${_origin#http://}
	_host=${_host%:5264}
	_old_ifs=$IFS
	IFS=.
	set -- $_host
	IFS=$_old_ifs
	[ "$#" -eq 4 ] || return 1
	for _octet in "$@"; do
		case "$_octet" in
			0|[1-9]|[1-9][0-9]|[12][0-9][0-9]) ;;
			*) return 1 ;;
		esac
		[ "$_octet" -le 255 ] || return 1
	done
	case "$1.$2" in
		10.*|192.168|172.1[6-9]|172.2[0-9]|172.3[01]) return 0 ;;
		*) return 1 ;;
	esac
}

usage() {
    echo "Usage: install.sh [--no-install] [--service]"
    echo ""
    echo "Options:"
    echo "  --no-install      Skip binary installation (registration only)"
    echo "  --service         Install and enable SystemD service (Linux only)"
    echo ""
    echo "A KOMBI_SERVER/KOMBI_TOKEN worker one-liner installs the persistent Guard by default."
    echo "For an already enrolled worker, also set KOMBI_RUNTIME_AGENT_ID and KOMBI_TENANT_ID."
    echo "Set TECHSTACK_REGISTRATION_ONLY=1 for the legacy one-shot registration behavior."
}

parse_args() {
    while [ $# -gt 0 ]; do
        case "$1" in
            --no-install)
                NO_INSTALL="1"; shift 1 ;;
            --service)
                AS_SERVICE="1"; shift 1 ;;
            -h|--help)
                usage; exit 0 ;;
            *)
                echo "${YELLOW}Unknown argument: $1${NC}" >&2
                usage
                exit 1
                ;;
        esac
    done
}

prepare_existing_runtime_enrollment() {
    KOMBI_SERVER=${KOMBI_SERVER:-${TECHSTACK_SERVER_URL:-}}
    KOMBI_TOKEN=${KOMBI_TOKEN:-${TECHSTACK_AGENT_TOKEN:-}}
    KOMBI_RUNTIME_AGENT_ID=${KOMBI_RUNTIME_AGENT_ID:-${TECHSTACK_RUNTIME_AGENT_ID:-}}
    KOMBI_TENANT_ID=${KOMBI_TENANT_ID:-${TECHSTACK_TENANT_ID:-}}
    KOMBI_OWNER_ID=${KOMBI_OWNER_ID:-${TECHSTACK_OWNER_ID:-}}
    KOMBI_STACK_ID=${KOMBI_STACK_ID:-${TECHSTACK_STACK_ID:-}}
    KOMBI_LEASE_ID=${KOMBI_LEASE_ID:-${TECHSTACK_LEASE_ID:-}}
    KOMBI_SERVER_ID=${KOMBI_SERVER_ID:-${TECHSTACK_SERVER_ID:-}}
    if [ -z "${KOMBI_RUNTIME_AGENT_ID:-}" ] && [ -z "${KOMBI_TENANT_ID:-}" ]; then
        return 0
    fi
    if [ -z "${KOMBI_SERVER:-}" ] || [ -z "${KOMBI_TOKEN:-}" ] ||
       [ -z "${KOMBI_RUNTIME_AGENT_ID:-}" ] || [ -z "${KOMBI_TENANT_ID:-}" ] ||
       [ -z "${KOMBI_STACK_ID:-}" ]; then
        echo "${RED}Existing runtime enrollment requires control-plane URL, token, runtime-agent, tenant, and stack identity.${NC}" >&2
        exit 1
    fi
    case "$KOMBI_RUNTIME_AGENT_ID" in
        *[!A-Za-z0-9._:-]*|'')
            echo "${RED}KOMBI_RUNTIME_AGENT_ID contains unsupported characters.${NC}" >&2
            exit 1
            ;;
    esac
    case "${KOMBI_TENANT_ID}${KOMBI_OWNER_ID}${KOMBI_STACK_ID}${KOMBI_LEASE_ID}${KOMBI_SERVER_ID}" in
        *[!A-Za-z0-9._:|-]*|'')
            echo "${RED}Existing runtime identity contains unsupported characters.${NC}" >&2
            exit 1
            ;;
    esac
    SERVER=${KOMBI_SERVER%/}
    if ! validate_control_plane_url "$SERVER"; then
        exit 1
    fi
    EXISTING_RUNTIME_ENROLLMENT="1"
    WORKER_REGISTERED="1"
}

worker_register_if_env_set() {
    # Back-compat: The UI already generates commands like:
    #   curl -fsSL <server>/install.sh | KOMBI_SERVER="<server>" KOMBI_TOKEN="<token>" bash
    # If these env vars are present, redeem the pairing token. The complete
    # response is kept in memory so --service can persist the signed HTTPS
    # Guard enrollment with root-only permissions.
    if [ -z "$KOMBI_SERVER" ] || [ -z "$KOMBI_TOKEN" ]; then
        return 0
    fi
    if [ "$EXISTING_RUNTIME_ENROLLMENT" = "1" ]; then
        echo "${GREEN}✓ Existing runtime enrollment retained; pairing redemption is not required.${NC}"
        return 0
    fi

    if ! command -v curl >/dev/null 2>&1; then
        echo "${RED}curl is required for worker registration.${NC}" >&2
        exit 1
    fi

    HOSTNAME_VAL=""
    if command -v hostname >/dev/null 2>&1; then
        HOSTNAME_VAL=$(hostname 2>/dev/null || true)
    fi
    if [ -z "$HOSTNAME_VAL" ]; then
        HOSTNAME_VAL="worker"
    fi

    OS_VAL=$(uname -s | tr '[:upper:]' '[:lower:]')
    ARCH_VAL=$(uname -m)

    # Basic JSON escaping
    H_ESC=$(printf '%s' "$HOSTNAME_VAL" | sed 's/\\/\\\\/g; s/"/\\"/g')
    OS_ESC=$(printf '%s' "$OS_VAL" | sed 's/\\/\\\\/g; s/"/\\"/g')
    ARCH_ESC=$(printf '%s' "$ARCH_VAL" | sed 's/\\/\\\\/g; s/"/\\"/g')
    TOK_ESC=$(printf '%s' "$KOMBI_TOKEN" | sed 's/\\/\\\\/g; s/"/\\"/g')

    SERVER=${KOMBI_SERVER%/}
    if ! validate_control_plane_url "$SERVER"; then
        exit 1
    fi

    is_localhost_url() {
        case "$1" in
            http://localhost*|https://localhost*|http://127.*|https://127.*)
                return 0
                ;;
        esac
        return 1
    }

    is_likely_vm() {
        # Best-effort: detect common QEMU/Docker guest environments.
        if grep -qi qemu /sys/class/dmi/id/product_name 2>/dev/null; then
            return 0
        fi
        if grep -qi qemu /sys/devices/virtual/dmi/id/product_name 2>/dev/null; then
            return 0
        fi
        return 1
    }

    print_localhost_hint() {
        echo "${YELLOW}Hint:${NC} You are using a localhost URL (${SERVER})." >&2
        echo "On a worker/VM, localhost refers to that machine (not your kombify Techstack host)." >&2
        echo "Use the kombify Techstack host's reachable IP/hostname instead (e.g. http://<host-ip>:5260)." >&2
        if is_likely_vm; then
            echo "${YELLOW}Note:${NC} You appear to be running inside a VM. Replace localhost accordingly." >&2
        fi
    }

    echo "${CYAN}Registering worker with kombify Techstack...${NC}"
    echo "${CYAN}Server:${NC} ${SERVER}" 

    REGISTER_TMP_DIR=$(mktemp -d)
    TMP_BODY="${REGISTER_TMP_DIR}/response"
    TMP_ERR="${REGISTER_TMP_DIR}/curl-error"
    TMP_PAYLOAD="${REGISTER_TMP_DIR}/request"
    printf '%s' "{\"token\":\"$TOK_ESC\",\"hostname\":\"$H_ESC\",\"os\":\"$OS_ESC\",\"arch\":\"$ARCH_ESC\"}" > "$TMP_PAYLOAD"
    : > "$TMP_BODY"
    : > "$TMP_ERR"
    chmod 0600 "$TMP_BODY" "$TMP_ERR" "$TMP_PAYLOAD"
    cleanup_worker_registration() {
        rm -f "$TMP_BODY" "$TMP_ERR" "$TMP_PAYLOAD"
        rmdir "$REGISTER_TMP_DIR" 2>/dev/null || true
    }
    trap 'cleanup_worker_registration' 0
    trap 'cleanup_worker_registration; exit 1' 1 2 15

    HTTP_CODE=$(curl -s -S --max-time 10 --connect-timeout 3 \
        -o "$TMP_BODY" -w "%{http_code}" \
        "$SERVER/api/v1/workers/register" \
        -H "Content-Type: application/json" \
        --data-binary "@${TMP_PAYLOAD}" \
        2>"$TMP_ERR" \
        || echo "curl_failed")
    case "$HTTP_CODE" in
        *curl_failed*) HTTP_CODE="curl_failed" ;;
    esac

    if [ "$HTTP_CODE" = "curl_failed" ]; then
        bootstrap_log error "Worker registration could not reach the control plane."
        echo "${RED}Worker registration failed: could not reach ${SERVER}/api/v1/workers/register${NC}" >&2
        if [ -s "$TMP_ERR" ]; then
            echo "--- curl error ---" >&2
            sed 's/^/  /' "$TMP_ERR" >&2
        fi
        if is_localhost_url "$SERVER"; then
            print_localhost_hint
        fi
        cleanup_worker_registration
        trap - 0 1 2 15
        exit 1
    fi

    RESP=$(cat "$TMP_BODY" 2>/dev/null || true)
    cleanup_worker_registration
    trap - 0 1 2 15

    # Treat non-2xx as a hard failure with diagnostics.
    if [ "$HTTP_CODE" -lt 200 ] || [ "$HTTP_CODE" -ge 300 ]; then
        bootstrap_log error "Worker registration was rejected with HTTP ${HTTP_CODE}."
        echo "${RED}Worker registration failed (HTTP ${HTTP_CODE}).${NC}" >&2
        echo "The response was withheld because enrollment responses can contain credentials." >&2
        if is_localhost_url "$SERVER"; then
            print_localhost_hint
        fi
        exit 1
    fi

    if [ -z "$RESP" ]; then
        bootstrap_log error "Worker registration returned an empty response."
        echo "${RED}Worker registration failed: empty response from server.${NC}" >&2
        if is_localhost_url "$SERVER"; then
            print_localhost_hint
        fi
        exit 1
    fi

    if echo "$RESP" | grep -q '"accepted"[[:space:]]*:[[:space:]]*true'; then
        echo "${GREEN}✓ Worker registered and accepted.${NC}"
    elif echo "$RESP" | grep -q '"accepted"[[:space:]]*:[[:space:]]*false'; then
        echo "${YELLOW}Worker registered but pending approval in the UI.${NC}"
        echo "Open the kombify Techstack dashboard and approve the worker to continue."
    else
        bootstrap_log error "Worker registration returned an invalid enrollment response."
        echo "${RED}Worker registration response was not recognized.${NC}" >&2
        echo "The response was withheld because an enrollment response can contain a bearer credential." >&2
        if is_localhost_url "$SERVER"; then
            print_localhost_hint
        fi
        exit 1
    fi
    WORKER_REGISTERED="1"
    ENROLLMENT_RESPONSE="$RESP"
}

# Detect OS and Architecture
detect_platform() {
    OS=$(uname -s | tr '[:upper:]' '[:lower:]')
    ARCH=$(uname -m)

    case "$OS" in
        linux)
            OS="linux"
            ;;
        darwin)
            OS="darwin"
            ;;
        mingw*|msys*|cygwin*)
            OS="windows"
            ;;
        *)
            echo "${RED}Unsupported operating system: $OS${NC}"
            exit 1
            ;;
    esac

    case "$ARCH" in
        x86_64|amd64)
            ARCH="x86_64"
            ;;
        aarch64|arm64)
            ARCH="arm64"
            ;;
        armv7l)
            ARCH="armv7"
            ;;
        *)
            echo "${RED}Unsupported architecture: $ARCH${NC}"
            exit 1
            ;;
    esac

    echo "${CYAN}Detected platform: ${OS}_${ARCH}${NC}"
}

# version_lt A B — return 0 (true) if semver A is strictly less than B.
# Strips a leading "v" and compares major.minor.patch numerically; non-numeric
# or missing fields default to 0. Pure POSIX sh (no sort -V dependency).
version_lt() {
    _a=$(echo "${1#v}" | cut -d- -f1)
    _b=$(echo "${2#v}" | cut -d- -f1)
    for _i in 1 2 3; do
        _fa=$(echo "$_a" | cut -d. -f"$_i"); _fa=${_fa:-0}
        _fb=$(echo "$_b" | cut -d. -f"$_i"); _fb=${_fb:-0}
        # Guard against non-numeric segments.
        case "$_fa" in ''|*[!0-9]*) _fa=0 ;; esac
        case "$_fb" in ''|*[!0-9]*) _fb=0 ;; esac
        if [ "$_fa" -lt "$_fb" ]; then return 0; fi
        if [ "$_fa" -gt "$_fb" ]; then return 1; fi
    done
    return 1
}

# Get the release version to install. Precedence:
#   1. TECHSTACK_VERSION env  -> explicit pin, no lookup (escape hatch).
#   2. GitHub "latest" release -> validated against MIN_SUPPORTED_VERSION so a
#      dormant mirror can never silently ship an ancient binary.
get_latest_version() {
    if [ -n "${TECHSTACK_VERSION:-}" ]; then
        LATEST_VERSION="$TECHSTACK_VERSION"
        echo "${CYAN}Using pinned version: ${LATEST_VERSION}${NC}"
        return 0
    fi

    echo "${CYAN}Fetching latest version...${NC}"
    LATEST_VERSION=$(curl -s "https://api.github.com/repos/${REPO}/releases/latest" | grep '"tag_name"' | sed -E 's/.*"([^"]+)".*/\1/')

    if [ -z "$LATEST_VERSION" ]; then
        echo "${RED}Failed to fetch the latest release from ${REPO}.${NC}" >&2
        echo "${YELLOW}The public release mirror may be unreachable or rate-limited.${NC}" >&2
        echo "${YELLOW}Pin a known version and retry:${NC}" >&2
        echo "    TECHSTACK_VERSION=v${MIN_SUPPORTED_VERSION} curl -fsSL https://install.techstack.kombify.io | sh" >&2
        exit 1
    fi

    # Refuse to install a binary older than the supported baseline unless the
    # operator explicitly opts in — a dormant mirror serving an old "latest"
    # tag is the exact failure this guards against.
    if version_lt "$LATEST_VERSION" "$MIN_SUPPORTED_VERSION"; then
        if [ "${TECHSTACK_ALLOW_STALE:-0}" = "1" ]; then
            echo "${YELLOW}Warning: ${LATEST_VERSION} is below the supported baseline ${MIN_SUPPORTED_VERSION}; proceeding because TECHSTACK_ALLOW_STALE=1.${NC}" >&2
        else
            echo "${RED}Refusing to install stale release ${LATEST_VERSION} (below supported baseline ${MIN_SUPPORTED_VERSION}).${NC}" >&2
            echo "${YELLOW}The public release mirror (${REPO}) appears to be behind. Options:${NC}" >&2
            echo "    - Pin a specific version:   TECHSTACK_VERSION=v<x.y.z> <installer>" >&2
            echo "    - Override intentionally:   TECHSTACK_ALLOW_STALE=1 <installer>" >&2
            exit 1
        fi
    fi

    echo "${GREEN}Latest version: ${LATEST_VERSION}${NC}"
}

# Prefer the exact Linux executable currently serving KOMBI_SERVER. This keeps
# the Guard in lockstep with the deployed HTTPS enrollment contract while the
# public release mirror is dormant. The download is authorized by the active
# pairing capability in an HTTPS Authorization header without consuming it;
# integrity metadata is verified before install and one-time redemption.
install_binary_from_control_plane() {
    if [ -z "${KOMBI_SERVER:-}" ] || [ "$OS" != "linux" ] || [ -n "${TECHSTACK_VERSION:-}" ]; then
        return 1
    fi
    if ! command -v sha256sum >/dev/null 2>&1; then
        echo "${RED}sha256sum is required to verify the control-plane Guard binary.${NC}" >&2
        exit 1
    fi

    SERVER=${KOMBI_SERVER%/}
    if ! validate_control_plane_url "$SERVER"; then
        exit 1
    fi

    DOWNLOAD_TIMEOUT=${TECHSTACK_BINARY_DOWNLOAD_TIMEOUT:-300}
    case "$DOWNLOAD_TIMEOUT" in
        ''|*[!0-9]*)
            echo "${RED}TECHSTACK_BINARY_DOWNLOAD_TIMEOUT must be a positive number of seconds.${NC}" >&2
            exit 1
            ;;
    esac
    if [ "$DOWNLOAD_TIMEOUT" -le 0 ]; then
        echo "${RED}TECHSTACK_BINARY_DOWNLOAD_TIMEOUT must be greater than zero.${NC}" >&2
        exit 1
    fi

    CONTROL_PLANE_BINARY_URL="${SERVER}/api/v1/agent/binary/${OS}/${ARCH}"
    TMP_DIR=$(mktemp -d)
    BINARY_TMP="${TMP_DIR}/techstack"
    HEADERS_TMP="${TMP_DIR}/headers"
    AUTH_HEADER_TMP="${TMP_DIR}/authorization"
    printf 'Authorization: Bearer %s\n' "$KOMBI_TOKEN" > "$AUTH_HEADER_TMP"
    if [ "$EXISTING_RUNTIME_ENROLLMENT" = "1" ]; then
        printf 'X-Kombify-Runtime-Agent-ID: %s\n' "$KOMBI_RUNTIME_AGENT_ID" >> "$AUTH_HEADER_TMP"
        printf 'X-Kombify-Tenant-ID: %s\n' "$KOMBI_TENANT_ID" >> "$AUTH_HEADER_TMP"
    fi
    chmod 0600 "$AUTH_HEADER_TMP"
    cleanup_control_plane_download() {
        rm -f "$BINARY_TMP" "$HEADERS_TMP" "$AUTH_HEADER_TMP"
        rmdir "$TMP_DIR" 2>/dev/null || true
    }
    trap 'cleanup_control_plane_download' 0
    trap 'cleanup_control_plane_download; exit 1' 1 2 15

    echo "${CYAN}Requesting the deployed Guard binary from ${SERVER}...${NC}"
    if HTTP_CODE=$(curl -sS --request POST --connect-timeout 5 --max-time "$DOWNLOAD_TIMEOUT" \
        -H "@${AUTH_HEADER_TMP}" \
        -H "Content-Type: application/octet-stream" \
        -D "$HEADERS_TMP" -o "$BINARY_TMP" -w "%{http_code}" \
        "$CONTROL_PLANE_BINARY_URL"); then
        :
    else
        echo "${YELLOW}The control-plane binary endpoint is unreachable; checking the release fallback.${NC}" >&2
        cleanup_control_plane_download
        trap - 0 1 2 15
        return 1
    fi
    rm -f "$AUTH_HEADER_TMP"

    case "$HTTP_CODE" in
        200) ;;
        404|405|501)
            echo "${YELLOW}This control plane does not publish a compatible Guard binary; checking the release fallback.${NC}" >&2
            cleanup_control_plane_download
            trap - 0 1 2 15
            return 1
            ;;
        409)
            AVAILABLE_ARCH=$(awk 'tolower($1) == "x-kombify-artifact-arch:" { gsub("\\r", "", $2); print tolower($2); exit }' "$HEADERS_TMP")
            echo "${RED}The deployed control plane has no Guard binary for ${OS}/${ARCH}.${NC}" >&2
            if [ -n "$AVAILABLE_ARCH" ]; then
                echo "${YELLOW}Available deployed architecture: ${OS}/${AVAILABLE_ARCH}.${NC}" >&2
            fi
            cleanup_control_plane_download
            trap - 0 1 2 15
            exit 1
            ;;
        401|403)
            echo "${RED}The control plane rejected the agent download capability.${NC}" >&2
            cleanup_control_plane_download
            trap - 0 1 2 15
            exit 1
            ;;
        429)
            echo "${RED}The Guard binary download is rate-limited.${NC}" >&2
            echo "${YELLOW}Retry after the bounded download window; do not rotate a working runtime credential.${NC}" >&2
            cleanup_control_plane_download
            trap - 0 1 2 15
            exit 1
            ;;
        *)
            echo "${RED}Control-plane binary download failed (HTTP ${HTTP_CODE}).${NC}" >&2
            cleanup_control_plane_download
            trap - 0 1 2 15
            exit 1
            ;;
    esac

    EXPECTED_SHA256=$(awk 'tolower($1) == "x-kombify-artifact-sha256:" { gsub("\\r", "", $2); print tolower($2); exit }' "$HEADERS_TMP")
    CONTENT_TYPE=$(awk 'tolower($1) == "content-type:" { gsub("\\r", "", $2); print tolower($2); exit }' "$HEADERS_TMP")
    CONTENT_DISPOSITION=$(awk 'tolower($1) == "content-disposition:" { sub(/^[^:]*:[[:space:]]*/, ""); sub(/\\r$/, ""); print; exit }' "$HEADERS_TMP")

    case "$EXPECTED_SHA256" in
        *[!0-9a-f]*|'') EXPECTED_SHA256="" ;;
    esac
    if [ "${#EXPECTED_SHA256}" -ne 64 ]; then
        echo "${RED}Control-plane binary response has no valid SHA-256 checksum.${NC}" >&2
        cleanup_control_plane_download
        trap - 0 1 2 15
        exit 1
    fi
    case "$CONTENT_TYPE" in
        application/octet-stream*) ;;
        *)
            echo "${RED}Control-plane binary response has an unexpected content type.${NC}" >&2
            cleanup_control_plane_download
            trap - 0 1 2 15
            exit 1
            ;;
    esac
    case "$CONTENT_DISPOSITION" in
        attachment\;*) ;;
        *)
            echo "${RED}Control-plane binary response is missing the attachment disposition.${NC}" >&2
            cleanup_control_plane_download
            trap - 0 1 2 15
            exit 1
            ;;
    esac

    ACTUAL_SHA256=$(sha256sum "$BINARY_TMP" | awk '{print tolower($1)}')
    if [ "$ACTUAL_SHA256" != "$EXPECTED_SHA256" ]; then
        echo "${RED}Control-plane binary checksum verification failed.${NC}" >&2
        cleanup_control_plane_download
        trap - 0 1 2 15
        exit 1
    fi

    chmod 0755 "$BINARY_TMP"
    if ! "$BINARY_TMP" version >/dev/null 2>&1; then
        echo "${RED}The verified control-plane binary cannot run on this Linux host.${NC}" >&2
        cleanup_control_plane_download
        trap - 0 1 2 15
        exit 1
    fi

    echo "${CYAN}Installing the verified deployed binary to ${INSTALL_DIR}...${NC}"
    if [ -w "$INSTALL_DIR" ]; then
        mv "$BINARY_TMP" "$INSTALL_DIR/$BINARY_NAME"
    else
        echo "${YELLOW}Elevated permissions required for installation${NC}"
        sudo mv "$BINARY_TMP" "$INSTALL_DIR/$BINARY_NAME"
    fi
    chmod 0755 "$INSTALL_DIR/$BINARY_NAME" 2>/dev/null || sudo chmod 0755 "$INSTALL_DIR/$BINARY_NAME"
    cleanup_control_plane_download
    trap - 0 1 2 15

    echo "${GREEN}✓ Installed the checksum-verified binary from the deployed control plane.${NC}"
    return 0
}

# Download and install
install_binary() {
    # Capitalize OS name (portable approach)
    OS_CAPITALIZED=$(echo "$OS" | awk '{print toupper(substr($0,1,1)) tolower(substr($0,2))}')
    
    DOWNLOAD_URL="https://github.com/${REPO}/releases/download/${LATEST_VERSION}/${BINARY_NAME}_${OS_CAPITALIZED}_${ARCH}.tar.gz"
    
    if [ "$OS" = "windows" ]; then
        DOWNLOAD_URL="https://github.com/${REPO}/releases/download/${LATEST_VERSION}/${BINARY_NAME}_${OS_CAPITALIZED}_${ARCH}.zip"
    fi

    echo "${CYAN}Downloading from: ${DOWNLOAD_URL}${NC}"
    
    TMP_DIR=$(mktemp -d)
    INSTALL_CALLER_DIR=$(pwd)
    cd "$TMP_DIR"

    if ! curl -fsSL "$DOWNLOAD_URL" -o archive; then
        echo "${RED}Failed to download binary${NC}"
        cd "$INSTALL_CALLER_DIR"
        rm -rf "$TMP_DIR"
        exit 1
    fi

    echo "${CYAN}Extracting...${NC}"
    if [ "$OS" = "windows" ]; then
        unzip -q archive
    else
        tar -xzf archive
    fi

    echo "${CYAN}Installing to ${INSTALL_DIR}...${NC}"
    
    # Check if we need sudo
    if [ -w "$INSTALL_DIR" ]; then
        mv "$BINARY_NAME" "$INSTALL_DIR/"
        chmod +x "$INSTALL_DIR/$BINARY_NAME"
    else
        echo "${YELLOW}Elevated permissions required for installation${NC}"
        sudo mv "$BINARY_NAME" "$INSTALL_DIR/"
        sudo chmod +x "$INSTALL_DIR/$BINARY_NAME"
    fi

    cd "$INSTALL_CALLER_DIR"
    rm -rf "$TMP_DIR"
    
    echo "${GREEN}✓ kombify Techstack installed successfully!${NC}"
}

# Verify installation
verify_installation() {
    BINARY_PATH="${INSTALL_DIR%/}/${BINARY_NAME}"
    if [ ! -x "$BINARY_PATH" ]; then
        echo "${RED}Verification failed: ${BINARY_PATH} is not executable.${NC}" >&2
        exit 1
    fi
    if ! VERSION=$("$BINARY_PATH" version 2>&1); then
        echo "${RED}Verification failed: the installed binary did not start.${NC}" >&2
        exit 1
    fi
    echo "${GREEN}✓ Verification successful${NC}"
    echo "$VERSION"
}

install_stackkit_operations_process() {
    if [ "$OS" != "linux" ]; then
        return 0
    fi
    BINARY_PATH="${INSTALL_DIR%/}/${BINARY_NAME}"
    OPERATIONS_PATH="${OPERATIONS_DIR%/}/${OPERATIONS_BINARY_NAME}"
    if [ -w "$OPERATIONS_DIR" ]; then
        install -m 0755 "$BINARY_PATH" "$OPERATIONS_PATH"
    else
        sudo install -d -m 0755 "$OPERATIONS_DIR"
        sudo install -m 0755 "$BINARY_PATH" "$OPERATIONS_PATH"
    fi
    BINARY_SHA256=$(sha256sum "$BINARY_PATH" | awk '{print tolower($1)}')
    OPERATIONS_SHA256=$(sha256sum "$OPERATIONS_PATH" | awk '{print tolower($1)}')
    if [ "$BINARY_SHA256" != "$OPERATIONS_SHA256" ]; then
        echo "${RED}StackKits operations process checksum verification failed.${NC}" >&2
        exit 1
    fi
    echo "${GREEN}✓ Installed the digest-identical StackKits operations process.${NC}"
}

# Install SystemD service (Linux only)
install_systemd_service() {
    if [ "$OS" != "linux" ]; then
        echo "${YELLOW}SystemD service installation is only supported on Linux${NC}"
        return 0
    fi

    if ! command -v systemctl >/dev/null 2>&1; then
        echo "${YELLOW}SystemD not found, skipping service installation${NC}"
        return 0
    fi

    echo "${CYAN}Installing SystemD service...${NC}"

    if [ "$WORKER_REGISTERED" = "1" ]; then
        install_systemd_agent_service
        return $?
    fi

    # Create techstack user if it doesn't exist
    if ! getent passwd techstack >/dev/null 2>&1; then
        echo "${CYAN}Creating techstack system user...${NC}"
        if [ -w /etc/passwd ] || command -v sudo >/dev/null 2>&1; then
            sudo useradd --system --home /var/lib/techstack --shell /usr/sbin/nologin techstack 2>/dev/null || true
        fi
    fi

    # Create directories
    for dir in /var/lib/techstack /var/log/techstack /etc/techstack; do
        if [ ! -d "$dir" ]; then
            sudo mkdir -p "$dir"
            sudo chown techstack:techstack "$dir" 2>/dev/null || true
        fi
    done

    # Download and install service file
    SERVICE_URL="https://raw.githubusercontent.com/${REPO}/main/packaging/techstack.service"
    SERVICE_FILE="/lib/systemd/system/techstack.service"

    echo "${CYAN}Downloading service file...${NC}"
    if sudo curl -fsSL "$SERVICE_URL" -o "$SERVICE_FILE"; then
        sudo chmod 644 "$SERVICE_FILE"
        sudo systemctl daemon-reload
        
        echo "${GREEN}✓ SystemD service installed${NC}"
        echo ""
        echo "To start kombify Techstack:"
        echo "  sudo systemctl start techstack"
        echo ""
        echo "To enable at boot:"
        echo "  sudo systemctl enable techstack"
        echo ""
        echo "To check status:"
        echo "  sudo systemctl status techstack"
    else
        echo "${RED}Failed to download service file${NC}"
        echo "You can manually create the service file at $SERVICE_FILE"
    fi
}

install_systemd_agent_service() {
    if [ "$EXISTING_RUNTIME_ENROLLMENT" = "1" ]; then
        install_existing_runtime_agent_service
        return $?
    fi
    if [ -z "$ENROLLMENT_RESPONSE" ] || ! echo "$ENROLLMENT_RESPONSE" | grep -q '"agent_token"[[:space:]]*:'; then
        echo "${RED}Cannot install the Guard service: registration did not return an agent token.${NC}" >&2
        return 1
    fi

    BINARY_PATH="${INSTALL_DIR%/}/${BINARY_NAME}"
    ENROLLMENT_TMP=$(mktemp)
    UNIT_TMP=$(mktemp)
    trap 'rm -f "$ENROLLMENT_TMP" "$UNIT_TMP"' 0 1 2 15
    umask 077
    printf '%s\n' "$ENROLLMENT_RESPONSE" > "$ENROLLMENT_TMP"
    cat > "$UNIT_TMP" <<EOF
[Unit]
Description=Kombify TechStack Guard (outbound HTTPS runtime channel)
Documentation=https://github.com/kombifyio/TechStack
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=root
ExecStart=${BINARY_PATH} agent --transport=https --enrollment-file=/etc/techstack/agent-enrollment.json
Restart=always
RestartSec=15
TimeoutStopSec=20
NoNewPrivileges=yes
PrivateTmp=yes
ProtectSystem=strict
ProtectHome=read-only
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes
ReadOnlyPaths=/etc/techstack -/root/my-homelab
# The Agent converges its own executable by staging a sibling temp file and
# renaming it into place, so it needs the CONTAINING DIRECTORY, not just the
# file. Granting only the executable path made every self-update fail with
# "read-only file system" while the download itself succeeded (incident
# 2026-08-12: fleet stranded on 0.7.90 against a 0.7.154 control plane).
ReadWritePaths=/app /opt/stackkit ${INSTALL_DIR%/} ${OPERATIONS_DIR}
Environment=TECHSTACK_ACCESS_MANIFEST=/opt/stackkit/.stackkit/access.json,/root/my-homelab/.stackkit/access.json
Environment=TECHSTACK_STACKKIT_RELEASE_PIN=/app/.stackkit/stackkits-release-pin.json
Environment=TECHSTACK_STACKKIT_RELEASE_CACHE=/app/.stackkit/releases
Environment=TECHSTACK_AGENT_EXECUTABLE_PATH=${BINARY_PATH}
Environment=TECHSTACK_OPERATIONS_EXECUTABLE_PATH=${OPERATIONS_DIR%/}/${OPERATIONS_BINARY_NAME}

[Install]
WantedBy=multi-user.target
EOF

    sudo install -d -m 0750 /etc/techstack
    sudo install -d -m 0755 /app /opt/stackkit
    sudo install -m 0600 "$ENROLLMENT_TMP" /etc/techstack/agent-enrollment.json
    sudo install -m 0644 "$UNIT_TMP" /etc/systemd/system/techstack-agent.service
    rm -f "$ENROLLMENT_TMP" "$UNIT_TMP"
    trap - 0 1 2 15
    sudo systemctl daemon-reload
    sudo systemctl enable techstack-agent.service
    sudo systemctl restart techstack-agent.service

    echo "${GREEN}✓ Outbound HTTPS Guard installed and started.${NC}"
    echo "  Status: sudo systemctl status techstack-agent"
    echo "  Logs:   sudo journalctl -u techstack-agent -f"
}

install_existing_runtime_agent_service() {
    BINARY_PATH="${INSTALL_DIR%/}/${BINARY_NAME}"
    ENROLLMENT_TMP=$(mktemp)
    UNIT_TMP=$(mktemp)
    trap 'rm -f "$ENROLLMENT_TMP" "$UNIT_TMP"' 0 1 2 15
    umask 077
    HEARTBEAT_URL="${SERVER}/api/v1/workers/${KOMBI_RUNTIME_AGENT_ID}/heartbeat"
    INVENTORY_URL="${SERVER}/api/v1/workers/${KOMBI_RUNTIME_AGENT_ID}/inventory"
    TOKEN_ESC=$(printf '%s' "$KOMBI_TOKEN" | sed 's/\\/\\\\/g; s/"/\\"/g')
    printf '%s\n' "{\"data\":{\"runtime_agent_id\":\"${KOMBI_RUNTIME_AGENT_ID}\",\"server_id\":\"${KOMBI_SERVER_ID}\",\"tenant_id\":\"${KOMBI_TENANT_ID}\",\"owner_id\":\"${KOMBI_OWNER_ID}\",\"stack_id\":\"${KOMBI_STACK_ID}\",\"lease_id\":\"${KOMBI_LEASE_ID}\",\"agent_token\":\"${TOKEN_ESC}\",\"heartbeat_url\":\"${HEARTBEAT_URL}\",\"inventory_url\":\"${INVENTORY_URL}\"}}" > "$ENROLLMENT_TMP"
    cat > "$UNIT_TMP" <<EOF
[Unit]
Description=Kombify TechStack Guard (outbound HTTPS runtime channel)
Documentation=https://github.com/kombifyio/TechStack
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=root
ExecStart=${BINARY_PATH} agent --transport=https --enrollment-file=/etc/techstack/agent-enrollment.json
Restart=always
RestartSec=15
TimeoutStopSec=20
NoNewPrivileges=yes
PrivateTmp=yes
ProtectSystem=strict
ProtectHome=read-only
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes
ReadOnlyPaths=/etc/techstack -/root/my-homelab
# The Agent converges its own executable by staging a sibling temp file and
# renaming it into place, so it needs the CONTAINING DIRECTORY, not just the
# file. Granting only the executable path made every self-update fail with
# "read-only file system" while the download itself succeeded (incident
# 2026-08-12: fleet stranded on 0.7.90 against a 0.7.154 control plane).
ReadWritePaths=/app /opt/stackkit ${INSTALL_DIR%/} ${OPERATIONS_DIR}
Environment=TECHSTACK_ACCESS_MANIFEST=/opt/stackkit/.stackkit/access.json,/root/my-homelab/.stackkit/access.json
Environment=TECHSTACK_STACKKIT_RELEASE_PIN=/app/.stackkit/stackkits-release-pin.json
Environment=TECHSTACK_STACKKIT_RELEASE_CACHE=/app/.stackkit/releases
Environment=TECHSTACK_AGENT_EXECUTABLE_PATH=${BINARY_PATH}
Environment=TECHSTACK_OPERATIONS_EXECUTABLE_PATH=${OPERATIONS_DIR%/}/${OPERATIONS_BINARY_NAME}

[Install]
WantedBy=multi-user.target
EOF

    sudo install -d -m 0750 /etc/techstack
    sudo install -d -m 0755 /app /opt/stackkit
    sudo install -m 0600 "$ENROLLMENT_TMP" /etc/techstack/agent-enrollment.json
    sudo install -m 0644 "$UNIT_TMP" /etc/systemd/system/techstack-agent.service
    rm -f "$ENROLLMENT_TMP" "$UNIT_TMP"
    trap - 0 1 2 15
    sudo systemctl daemon-reload
    sudo systemctl enable techstack-agent.service
    sudo systemctl restart techstack-agent.service

    echo "${GREEN}✓ Existing outbound HTTPS Guard enrollment installed and started.${NC}"
    echo "  Status: sudo systemctl status techstack-agent"
    echo "  Logs:   sudo journalctl -u techstack-agent -f"
}

# Main installation flow
main() {
    echo "${CYAN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo "${CYAN}   kombify Techstack Installer${NC}"
    echo "${CYAN}   The Hybrid Infrastructure Unifier${NC}"
    echo "${CYAN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo ""

    parse_args "$@"
    prepare_existing_runtime_enrollment
    trap bootstrap_failed 0
    bootstrap_phase starting "Techstack installer started."

    if [ "$NO_INSTALL" = "1" ]; then
        worker_register_if_env_set
        echo "${GREEN}Skipping install (--no-install).${NC}"
        return 0
    fi

    # The worker one-liner is durable by default. This branch is retained only
    # for an explicit TECHSTACK_REGISTRATION_ONLY=1 compatibility request.
    if [ -n "${KOMBI_SERVER:-}" ] && [ -n "${KOMBI_TOKEN:-}" ] && [ "$AS_SERVICE" != "1" ] && [ "${TECHSTACK_INSTALL_BINARY:-0}" != "1" ]; then
        worker_register_if_env_set
        echo "${YELLOW}Worker registration completed, but persistent health monitoring is not installed.${NC}"
        echo "Unset TECHSTACK_REGISTRATION_ONLY (or pass --service) to install and start the outbound HTTPS Guard."
        return 0
    fi

    bootstrap_phase platform "Detecting the server platform."
    detect_platform
    bootstrap_phase binary "Installing the pinned Techstack Guard binary."
    if install_binary_from_control_plane; then
        :
    else
        get_latest_version
        install_binary
    fi
    verify_installation
    bootstrap_phase stackkits-runtime "Installing the pinned StackKits operations runtime."
    install_stackkit_operations_process

    # Redeem the one-use token only after the binary is present and verified.
    bootstrap_phase enrollment "Guard binaries are installed; redeeming the managed server enrollment."
    worker_register_if_env_set

    # Install service if requested
    if [ "$AS_SERVICE" = "1" ]; then
        BOOTSTRAP_PHASE="service"
        install_systemd_service
    fi

    echo ""
    echo "${GREEN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo "${GREEN}Installation complete!${NC}"
    echo ""
    echo "Next steps:"
    echo "  1. Run '${BINARY_NAME} version' to verify installation"
    echo "  2. Run '${BINARY_NAME}' to see available commands"
    if [ "$AS_SERVICE" = "1" ]; then
        if [ "$WORKER_REGISTERED" = "1" ]; then
            echo "  3. Run 'sudo systemctl status techstack-agent' to verify continuous monitoring"
        else
            echo "  3. Run 'sudo systemctl start techstack' to start the service"
        fi
    fi
    echo "  4. Visit https://github.com/${REPO} for documentation"
    echo "${GREEN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    BOOTSTRAP_PHASE="complete"
    trap - 0
}

main "$@"
