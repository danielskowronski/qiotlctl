#!/usr/bin/env bash
# Requires go-licenses v2.0.1 (build tool only, not an application dependency).
set -euo pipefail
cd "$(dirname "$0")/.."
scanner=${GO_LICENSES:-go-licenses}
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
for os in linux darwin; do
  CGO_ENABLED=0 GOOS="$os" GOARCH=amd64 "$scanner" save ./cmd/qiotlctl ./cmd/qiotld \
    --ignore github.com/danielskowronski/qiotlctl --save_path "$work/$os"
done
python3 - "$work" <<'PY'
import pathlib, shutil, sys
source = pathlib.Path(sys.argv[1])
dest = pathlib.Path('third_party/licenses')
# Include notices, never upstream examples, test keys or full source trees.
# Paho permits distribution under EDL-1.0; keep both upstream license texts.
for platform in ('linux', 'darwin'):
    for p in sorted((source / platform).rglob('*')):
        if p.is_file() and p.name.lower().startswith(('license', 'licence', 'notice', 'copying', 'copyright', 'edl-', 'epl-')):
            relative = p.relative_to(source / platform)
            if any(part in ('testdata', 'vendor', 'cmd') for part in relative.parts):
                continue
            out = dest / relative
            out.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(p, out)
PY
# go-licenses saves the EPL text but may omit the alternate EDL text.
paho=$(go list -m -f '{{.Dir}}' github.com/eclipse/paho.mqtt.golang)
cp "$paho/edl-v10" third_party/licenses/github.com/eclipse/paho.mqtt.golang/edl-v10
go list -m all > third_party/modules.txt
