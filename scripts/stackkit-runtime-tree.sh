#!/bin/sh
# Assembles the StackKits runtime tree (.stackkit/) that ships to Guard hosts
# from an already digest-verified, extracted StackKits release. The control plane
# image build and the Guard release workflow both call this script, so the tree
# the workflow signs (guard-release-tool tree-digest) is byte for byte the tree
# the image bundles and the privileged Guard executor verifies.
#
# usage: stackkit-runtime-tree.sh <amd64|arm64> <release-dir> <compatibility.json> <use-case-catalog.json> <out-root>
# env:   STACKKIT_RELEASE_KIT, STACKKIT_RELEASE_TAG, STACKKIT_RELEASE_ARCHIVE_SHA256,
#        STACKKIT_RELEASE_INDEX_SHA256
set -eu

if [ "$#" -ne 5 ]; then
  echo "usage: stackkit-runtime-tree.sh <amd64|arm64> <release-dir> <compatibility.json> <use-case-catalog.json> <out-root>" >&2
  exit 64
fi
arch=$1
release=$2
compatibility=$3
catalog=$4
out=$5
case "$arch" in amd64|arm64) ;; *) echo "unsupported architecture: $arch" >&2; exit 64 ;; esac
: "${STACKKIT_RELEASE_KIT:?}" "${STACKKIT_RELEASE_TAG:?}" "${STACKKIT_RELEASE_ARCHIVE_SHA256:?}" "${STACKKIT_RELEASE_INDEX_SHA256:?}"

root="$out/.stackkit"
mkdir -p "$root/bin"
# StackKits resolves its packaged executors and offline provider closure beside
# stackkit. Preserve that layout from the verified archive in both the image
# bundle and the tree authenticated by the privileged Guard executor.
for tool in stackkit stackkit-server stackkit-mcp tofu terramate; do
  if [ ! -f "$release/$tool" ] || [ ! -x "$release/$tool" ]; then
    echo "StackKits release is missing executable $tool" >&2
    exit 1
  fi
  cp "$release/$tool" "$root/bin/$tool"
  chmod 0755 "$root/bin/$tool"
done
test -s "$release/providers/stackkit-provider-manifest.json"
test -s "$release/providers/stackkit-provider-lock.hcl"
mkdir -p "$root/bin/providers"
cp -R "$release/providers/." "$root/bin/providers/"
cp "$compatibility" "$root/stackkits-compatibility-v1.json"
cp "$catalog" "$root/stackkits-use-case-catalog-v1.json"
# Every Techstack-managed deployment runs Advanced Mode, and Core and Agent
# render Advanced argv only from the pinned release's own catalog. A release
# without it cannot serve Techstack; there is no embedded fallback.
advanced_catalog="$release/docs/data/advanced-operations/latest.json"
if [ ! -s "$advanced_catalog" ]; then
  echo "StackKits release is missing its Advanced operations catalog (docs/data/advanced-operations/latest.json)" >&2
  exit 1
fi
jq -e '.schemaVersion == "stackkit.advanced-operations/v1"' "$advanced_catalog" >/dev/null
cp "$advanced_catalog" "$root/stackkits-advanced-operations-v1.json"
chmod 0644 "$root/stackkits-compatibility-v1.json" "$root/stackkits-use-case-catalog-v1.json" "$root/stackkits-advanced-operations-v1.json"

binary_sha256="$(sha256sum "$root/bin/stackkit" | cut -d' ' -f1)"
jq -n \
  --arg kit "$STACKKIT_RELEASE_KIT" \
  --arg version "$STACKKIT_RELEASE_TAG" \
  --arg arch "$arch" \
  --arg archiveSha256 "$STACKKIT_RELEASE_ARCHIVE_SHA256" \
  --arg indexSha256 "$STACKKIT_RELEASE_INDEX_SHA256" \
  --arg binarySha256 "$binary_sha256" \
  '{schemaVersion:"techstack.stackkit-release-pin/v2",kit:$kit,version:$version,platform:{os:"linux",arch:$arch},archiveSha256:$archiveSha256,indexSha256:$indexSha256,binarySha256:$binarySha256,binaryPath:"/app/.stackkit/bin/stackkit"}' \
  > "$root/stackkits-release-pin.json"
chmod 0644 "$root/stackkits-release-pin.json"
