#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
mode=${1:-local}
VERSION=${VERSION:-dev}
IMAGE=${IMAGE:-ghcr.io/danielskowronski/qiotlctl}
BUILDER=${BUILDER:-qiotlctl-build}
revision=$(git rev-parse HEAD 2>/dev/null || printf unknown)
if [[ -n "$(git status --porcelain --untracked-files=normal 2>/dev/null)" ]]; then revision="$revision-dirty"; fi
REVISION=${REVISION:-$revision}
if [[ ! "$VERSION" =~ ^[a-zA-Z0-9_][a-zA-Z0-9_.-]{0,127}$ ]]; then
  echo 'VERSION must be a valid image tag' >&2; exit 1
fi
case "$mode" in local|archive|push) ;; *) echo 'expected local, archive or push' >&2; exit 1;; esac
if [[ "$mode" == push ]]; then
  bash scripts/release-check.sh
  if [[ "$REVISION" != "$revision" ]]; then
    echo 'REVISION must match the clean source commit for publication' >&2; exit 1
  fi
fi
# Select the requested builder without changing the default builder.
if ! docker buildx inspect "$BUILDER" >/dev/null 2>&1; then
  docker buildx create --name "$BUILDER" --driver docker-container >/dev/null
fi
mkdir -p dist
args=(--builder "$BUILDER" --build-arg "VERSION=$VERSION" --build-arg "REVISION=$REVISION" --tag "$IMAGE:$VERSION")
case "$mode" in
  local)
    platform=${PLATFORM:-$(docker version --format '{{.Server.Os}}/{{.Server.Arch}}')}
    args+=(--platform "$platform" --load --provenance=false)
    ;;
  archive)
    args+=(--platform "${PLATFORMS:-linux/amd64,linux/arm64}" --sbom=true --provenance=mode=max --output "type=oci,dest=dist/qiotlctl-$VERSION.oci.tar")
    ;;
  push)
    args+=(--platform "${PLATFORMS:-linux/amd64,linux/arm64}" --sbom=true --provenance=mode=max --push)
    ;;
esac
docker buildx build "${args[@]}" --metadata-file "dist/image-$VERSION-$mode.json" .
if [[ "$mode" == archive ]]; then
  (cd dist && shasum -a 256 "qiotlctl-$VERSION.oci.tar" > "qiotlctl-$VERSION.oci.tar.sha256")
fi
