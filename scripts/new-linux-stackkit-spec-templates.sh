#!/usr/bin/env bash
# Generates the three platform-independent StackKits spec templates on Linux
# from the pinned StackKits release. Shared by Delivery Windows client and the
# hosted installed-client smoke so both package identical templates.
set -euo pipefail
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
curl --fail --silent --show-error --location "$url" --output /tmp/release.tar.gz
echo "${STACKKIT_RELEASE_ARCHIVE_SHA256}  /tmp/release.tar.gz" | sha256sum -c -
mkdir -p /tmp/release
tar --extract --gzip --file /tmp/release.tar.gz --directory /tmp/release
test -x /tmp/release/stackkit
# Same flags as the container spec-template stage: since v0.24.27
# native v2alpha2 is the CLI default and v2alpha2 refuses to choose
# for you, so the api version and compute tier are named explicitly.
for kit in basement-kit cloud-kit modern-homelab; do
  rm -rf "/tmp/tpl/$kit"
  mkdir -p "/tmp/tpl/$kit"
  ( cd "/tmp/tpl/$kit" && /tmp/release/stackkit --no-log init "$kit" \
      --non-interactive \
      --name techstack-spec-template \
      --owner-source=local \
      --owner-email owner@smoke.stackkit.cc \
      --owner-username owner \
      --api-version stackkit/v2alpha1 \
      --compute-tier standard \
      --domain template.invalid )
  spec="/tmp/tpl/$kit/stack-spec.yaml"
  test -s "$spec"
  jq -e --arg kit "$kit" \
    '.apiVersion == "stackkit/v2alpha1" and .kind == "StackSpec" and .kit.slug == $kit' \
    "$spec" >/dev/null
  /tmp/release/stackkit --no-log --chdir "/tmp/tpl/$kit" --spec stack-spec.yaml validate
  mkdir -p "dist/spec-templates/$kit"
  cp "$spec" "dist/spec-templates/$kit/stack-spec.yaml"
  echo "spec template: $kit"
done
