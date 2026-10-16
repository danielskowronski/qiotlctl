#!/usr/bin/env bash
# Read-only preflight used only for explicit publication.
set -euo pipefail
cd "$(dirname "$0")/.."
version=$(awk '/^appVersion:/ {gsub(/"/, "", $2); print $2}' charts/qiotlctl/Chart.yaml)
chart_version=$(awk '/^version:/ {print $2}' charts/qiotlctl/Chart.yaml)
if [[ "${VERSION:-dev}" != "$version" || "${APP_VERSION:-$version}" != "$version" || "${CHART_VERSION:-$chart_version}" != "$chart_version" ]]; then
  echo "Publication versions must match Chart.yaml (app=$version chart=$chart_version)" >&2; exit 1
fi
git rev-parse --verify HEAD >/dev/null
if [[ -n "$(git status --porcelain --untracked-files=normal)" ]]; then
  echo 'Commit the reviewed source and rebuild before publication; checkout has uncommitted files' >&2; exit 1
fi
# A tag is deliberately a separate, explicit maintainer action.
if [[ "$(git rev-parse "refs/tags/v$version^{commit}" 2>/dev/null || true)" != "$(git rev-parse HEAD)" ]]; then
  echo "Create/review local tag v$version at HEAD before publication" >&2; exit 1
fi
cmp LICENSE charts/qiotlctl/LICENSE
printf 'Release source checks passed: %s\n' "$version"
