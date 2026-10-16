#!/usr/bin/env bash
# Build local release artifacts. No login, push, tagging or commit side effects.
set -euo pipefail
cd "$(dirname "$0")/.."
version=${VERSION:-dev}
if [[ ! "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[a-zA-Z0-9.-]+)?$ ]]; then
  echo 'Set VERSION to a release version, e.g. 0.2.0' >&2; exit 1
fi
out="$(pwd)/dist/$version"
mkdir -p "$out"
revision=$(git rev-parse HEAD 2>/dev/null || printf unknown)
if [[ -n "$(git status --porcelain --untracked-files=normal 2>/dev/null)" ]]; then revision="$revision-dirty"; fi
python3 scripts/source-archive.py "$out/qiotlctl-$version-source.tar.gz" "$version" > "$out/SOURCE.sha256"
{
  printf 'version=%s\nrevision=%s\n' "$version" "$revision"
  go version
  cat "$out/SOURCE.sha256"
} > "$out/BUILD-INFO.txt"
go_license="$(go env GOROOT)/LICENSE"
# Homebrew keeps the compiler license next to libexec rather than inside it.
if [[ ! -f "$go_license" ]]; then go_license="$(go env GOROOT)/../LICENSE"; fi
if [[ ! -f "$go_license" ]]; then echo 'Go compiler LICENSE not found' >&2; exit 1; fi
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do
  os=${target%/*}; arch=${target#*/}
  bundle="qiotlctl-$version-$os-$arch"
  mkdir "$work/$bundle"
  for binary in qiotlctl qiotld; do
    CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -buildvcs=false -trimpath \
      -ldflags="-s -w -X main.version=$version" -o "$work/$bundle/$binary" "./cmd/$binary"
  done
  cp LICENSE "$work/$bundle/"
  cp "$out/BUILD-INFO.txt" "$work/$bundle/"
  cp "$go_license" "$work/$bundle/Go-LICENSE"
  cp -R third_party "$work/$bundle/third_party"
  COPYFILE_DISABLE=1 tar -czf "$out/$bundle.tar.gz" -C "$work" "$bundle"
  printf 'Built %s\n' "$bundle"
done
(cd "$out" && shasum -a 256 ./*.tar.gz BUILD-INFO.txt > SHA256SUMS)
printf 'Local artifacts: %s\n' "$out"
