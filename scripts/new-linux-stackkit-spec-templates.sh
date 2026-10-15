#!/usr/bin/env bash
# Generates the three platform-independent StackKits spec templates on Linux
# from the pinned StackKits release. Shared by Delivery Windows client and the
# hosted installed-client smoke so both package identical templates.
set -euo pipefail
umask 077
work_root="$(mktemp -d /tmp/techstack-spec-templates.XXXXXXXX)"
trap 'rm -rf -- "$work_root"' EXIT
release_dir="$work_root/release"
# The private Dockerfile supplies the pin in Delivery. The curated public
# Dockerfile omits that stage, so its exported Windows bundle supplies the pin.
STACKKIT_RELEASE_TAG="$(sed -n 's/^ARG STACKKIT_RELEASE_TAG="\(v[0-9][0-9.]*\)"$/\1/p' Dockerfile)"
STACKKIT_RELEASE_ARCHIVE_SHA256="$(sed -n 's/^ARG STACKKIT_RELEASE_ARCHIVE_SHA256="\([0-9a-f]\{64\}\)"$/\1/p' Dockerfile)"
# The curated public Dockerfile has no StackKits build stage. Its Windows
# bundle script carries the same pinned public archive and digest instead.
if [[ -z "$STACKKIT_RELEASE_TAG" || -z "$STACKKIT_RELEASE_ARCHIVE_SHA256" ]]; then
  STACKKIT_RELEASE_TAG="$(sed -n 's/^[[:space:]]*\[string\]\$ReleaseTag = "\(v[0-9][0-9.]*\)",$/\1/p' scripts/new-windows-stackkit-bundle.ps1)"
  STACKKIT_RELEASE_ARCHIVE_SHA256="$(sed -n 's/^[[:space:]]*\[string\]\$LinuxArchiveSHA256 = "\([0-9a-f]\{64\}\)",$/\1/p' scripts/new-windows-stackkit-bundle.ps1)"
fi
test -n "$STACKKIT_RELEASE_TAG" && test -n "$STACKKIT_RELEASE_ARCHIVE_SHA256"
STACKKIT_RELEASE_VERSION="${STACKKIT_RELEASE_TAG#v}"
echo "pinned StackKits release: ${STACKKIT_RELEASE_TAG}"
archive="stackkits-basement-kit_${STACKKIT_RELEASE_VERSION}_linux_amd64.tar.gz"
url="https://github.com/kombifyio/StackKits/releases/download/${STACKKIT_RELEASE_TAG}/${archive}"
curl --fail --silent --show-error --location "$url" --output "$work_root/release.tar.gz"
echo "${STACKKIT_RELEASE_ARCHIVE_SHA256}  $work_root/release.tar.gz" | sha256sum -c -
mkdir -p "$release_dir"
tar --extract --gzip --file "$work_root/release.tar.gz" --directory "$release_dir"
test -x "$release_dir/stackkit"
# The release-authored seeds carry the recorded per-kit API-version decision.
# Reuse that decision; native resource/module defaults remain CLI-owned.
for kit in basement-kit cloud-kit modern-homelab; do
  api_version="$(jq -er '.apiVersion' "docs/examples/seeds/$kit/stack-spec.yaml")"
  case "$api_version" in
    stackkit/v2alpha2) authoring_args=(--api-version "$api_version" --catalog-defaults) ;;
    stackkit/v2alpha1)
      compute_tier="$(jq -er '.install.computeTier' "docs/examples/seeds/$kit/stack-spec.yaml")"
      authoring_args=(--api-version "$api_version" --compute-tier "$compute_tier") ;;
    *) echo "Unsupported recorded StackKits template API: $api_version" >&2; exit 1 ;;
  esac
  template_dir="$work_root/tpl/$kit"
  custody_dir="$work_root/custody/$kit"
  mkdir -p "$template_dir" "$custody_dir"
  ( cd "$template_dir" && STACKKIT_CUSTODY_DIR="$custody_dir" "$release_dir/stackkit" --no-log init "$kit" \
      --non-interactive \
      --name techstack-spec-template \
      --owner-source=local \
      --owner-email owner@smoke.stackkit.cc \
      --owner-username owner \
      "${authoring_args[@]}" \
      --domain template.invalid )
  spec="$template_dir/stack-spec.yaml"
  test -s "$spec"
  jq -e --arg kit "$kit" --arg api "$api_version" \
    '.apiVersion == $api and .kind == "StackSpec" and .kit.slug == $kit' \
    "$spec" >/dev/null
  STACKKIT_CUSTODY_DIR="$custody_dir" "$release_dir/stackkit" --no-log --chdir "$template_dir" --spec stack-spec.yaml validate
  mkdir -p "dist/spec-templates/$kit"
  cp "$spec" "dist/spec-templates/$kit/stack-spec.yaml"
  echo "spec template: $kit"
done
