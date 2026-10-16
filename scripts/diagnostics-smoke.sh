#!/usr/bin/env bash
# Disposable loopback EMQX and VM with synthetic broker configuration.
set -euo pipefail
cd "$(dirname "$0")/.."
work="$(pwd)/.local/diagnostics-smoke-$$"
mkdir -p "$work"
broker="qiotlctl-diagnostics-emqx-$$"
vm="qiotlctl-diagnostics-vm-$$"
cleanup() {
  code=$?
  docker logs "$broker" > "$work/emqx.log" 2>&1 || true
  docker logs "$vm" > "$work/vm.log" 2>&1 || true
  docker rm -f "$broker" "$vm" >/dev/null 2>&1 || true
  echo "Diagnostics evidence: $work (exit=$code)"
  exit "$code"
}
trap cleanup EXIT
cat > "$work/acl.conf" <<'ACL'
{deny, {username, "publish-denied"}, publish, ["homeassistant/sensor/#"]}.
{deny, {username, "readback-denied"}, subscribe, ["homeassistant/sensor/#"]}.
{deny, {username, "birth-denied"}, subscribe, ["homeassistant/status"]}.
{allow, all, all, ["#"]}.
ACL
docker run -d --name "$broker" -p 127.0.0.1::1883 \
  -e EMQX_AUTHORIZATION__DENY_ACTION=ignore \
  -e EMQX_AUTHORIZATION__CACHE__ENABLE=false \
  -v "$work/acl.conf:/opt/emqx/etc/acl.conf:ro" emqx:5.8.8 > "$work/emqx-id"
docker run -d --name "$vm" -p 127.0.0.1::8428 \
  victoriametrics/victoria-metrics:v1.154.0 -storageDataPath=/storage -memory.allowedBytes=128MiB > "$work/vm-id"
port=$(docker port "$broker" 1883/tcp | awk -F: '{print $NF}')
vm_port=$(docker port "$vm" 8428/tcp | awk -F: '{print $NF}')
for i in {1..45}; do
  if docker exec "$broker" emqx ctl status > "$work/emqx-status" 2>&1; then break; fi
  sleep 1
done
QIOTLCTL_TEST_EMQX="tcp://127.0.0.1:$port" go test -race ./tools/diagnostictest -count=1 -v | tee "$work/tests.log"
cat > "$work/doctor.yaml" <<YAML
---
mqtt: {url: 'tcp://127.0.0.1:$port', username: good}
health: {check_timeout: 1s}
devices:
  fixture:
    model: CGDN1
    topics: {telemetry: fixture/up, commands: fixture/down}
YAML
go run ./cmd/qiotlctl -c "$work/doctor.yaml" doctor --format json > "$work/doctor.json"
grep -q '"ready":true' "$work/doctor.json"
go run ./cmd/qiotlctl -c "$work/doctor.yaml" doctor --probe-publish --format json > "$work/doctor-probe.json"
grep -q '"id":"mqtt.probe_publish","status":"ok"' "$work/doctor-probe.json"
# An empty import proves route acceptance while inserting no sensor series.
curl -fsS "http://127.0.0.1:$vm_port/api/v1/series?match%5B%5D=%7B__name__%21%3D%22%22%7D" > "$work/series-before.json"
code=$(curl -sS -o "$work/import-body" -w '%{http_code}' -X POST -H 'Content-Type: application/json' --data-binary '' "http://127.0.0.1:$vm_port/api/v1/import")
[[ "$code" == 204 ]]
curl -fsS "http://127.0.0.1:$vm_port/api/v1/series?match%5B%5D=%7B__name__%21%3D%22%22%7D" > "$work/series-after.json"
cmp "$work/series-before.json" "$work/series-after.json"
grep -q '"data":\[\]' "$work/series-after.json"
echo 'EMQX denial/readback/recovery and empty VM import passed.'
