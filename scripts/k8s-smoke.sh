#!/usr/bin/env bash
# Local integration test. Never uses the caller's kubeconfig or live devices.
set -euo pipefail
cd "$(dirname "$0")/.."
VERSION=${VERSION:-dev}
chart_version=$(awk '/^version:/ {print $2}' charts/qiotlctl/Chart.yaml)
IMAGE=${IMAGE:-ghcr.io/danielskowronski/qiotlctl}
cluster="qiotlctl-smoke-$$"
registry="qiotlctl-registry-$$"
work="$(pwd)/.local/$cluster"
mkdir -p "$work"
export KUBECONFIG="$work/kubeconfig"
export KIND_EXPERIMENTAL_PROVIDER=docker
export HELM_REGISTRY_CONFIG="$work/registry.json"
created_cluster=0
created_registry=0
cleanup() {
  code=$?
  if [[ "$created_cluster" == 1 ]]; then
    kubectl -n qiotlctl-test get pods -o wide > "$work/pods.txt" 2>&1 || true
    kubectl -n qiotlctl-test logs deployment/qiotlctl > "$work/daemon.log" 2>&1 || true
    kubectl -n qiotlctl-test get events --sort-by=.lastTimestamp > "$work/events.txt" 2>&1 || true
  fi
  if [[ "${KEEP_TEST_CLUSTER:-0}" != 1 ]]; then
    if [[ "$created_cluster" == 1 ]]; then kind delete cluster --name "$cluster" >/dev/null 2>&1 || true; fi
    if [[ "$created_registry" == 1 ]]; then docker rm -f "$registry" >/dev/null 2>&1 || true; fi
  fi
  echo "Test evidence: $work (exit=$code)"
  exit "$code"
}
trap cleanup EXIT
for tool in docker kind kubectl helm curl; do command -v "$tool" >/dev/null; done
docker image inspect "$IMAGE:$VERSION" >/dev/null
if kind get clusters | grep -Fxq "$cluster"; then
  echo "Refusing to reuse existing cluster $cluster" >&2; exit 1
fi
# OCI registry binds only to loopback and is used solely for chart testing.
docker run -d --name "$registry" -p 127.0.0.1::5000 registry:2.8.3 > "$work/registry-id"
created_registry=1
port=$(docker port "$registry" 5000/tcp | awk -F: '{print $NF}')
for i in {1..30}; do
  if curl -fsS "http://127.0.0.1:$port/v2/" >/dev/null; then break; fi
  sleep 1
done
helm package charts/qiotlctl --destination "$work" --app-version "$VERSION" > "$work/package.log"
helm push "$work/qiotlctl-$chart_version.tgz" "oci://127.0.0.1:$port/charts" --plain-http > "$work/oci-push.log" 2>&1
mkdir "$work/pulled"
helm pull "oci://127.0.0.1:$port/charts/qiotlctl" --version "$chart_version" --plain-http --destination "$work/pulled" > "$work/oci-pull.log" 2>&1
cmp "$work/qiotlctl-$chart_version.tgz" "$work/pulled/qiotlctl-$chart_version.tgz"
created_cluster=1
kind create cluster --name "$cluster" --kubeconfig "$KUBECONFIG" --wait 120s
kind load docker-image "$IMAGE:$VERSION" --name "$cluster"
k() { kubectl "$@"; }
k create namespace qiotlctl-test
k label namespace qiotlctl-test pod-security.kubernetes.io/enforce=restricted
k apply -f - <<'YAML'
---
apiVersion: v1
kind: ConfigMap
metadata: {name: broker}
data:
  mosquitto.conf: |
    listener 1883
    allow_anonymous true
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: broker}
spec:
  replicas: 1
  selector: {matchLabels: {app: broker}}
  template:
    metadata: {labels: {app: broker}}
    spec:
      containers:
        - name: broker
          image: eclipse-mosquitto:2.0.22
          ports: [{containerPort: 1883}]
          volumeMounts: [{name: config, mountPath: /mosquitto/config}]
      volumes:
        - name: config
          configMap: {name: broker}
---
apiVersion: v1
kind: Service
metadata: {name: broker}
spec:
  selector: {app: broker}
  ports: [{port: 1883}]
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: vm}
spec:
  replicas: 1
  selector: {matchLabels: {app: vm}}
  template:
    metadata: {labels: {app: vm}}
    spec:
      containers:
        - name: vm
          image: victoriametrics/victoria-metrics:v1.154.0
          args: ['-storageDataPath=/storage', '-memory.allowedBytes=128MiB', '-dedup.minScrapeInterval=1ms']
          ports: [{containerPort: 8428}]
          volumeMounts: [{name: data, mountPath: /storage}]
      volumes: [{name: data, emptyDir: {}}]
---
apiVersion: v1
kind: Service
metadata: {name: vm}
spec:
  selector: {app: vm}
  ports: [{port: 8428}]
YAML
k rollout status deployment/broker --timeout=120s
k rollout status deployment/vm --timeout=120s
cat > "$work/values.yaml" <<YAML
---
fullnameOverride: qiotlctl
image:
  repository: $IMAGE
  tag: $VERSION
  pullPolicy: Never
persistence:
  size: 1Gi
networkPolicy:
  enabled: false # kind's default CNI does not enforce NetworkPolicy.
config:
  data:
    mqtt: {url: 'mqtt://broker.default.svc:1883', keepalive: 2s, reconnect_max: 2s}
    storage: {max_bytes: 536870912}
    health: {check_interval: 1s, check_timeout: 500ms, failure_grace: 0s}
    devices:
      fixture:
        mac: AABBCCDDEEFF
        model: CGDN1
        topics: {telemetry: fixture/up, commands: fixture/down}
    outputs:
      victoriametrics:
        enabled: true
        import_endpoint: http://vm.default.svc:8428/api/v1/import
        retry_max: 2s
YAML
helm upgrade --install qiotlctl "$work/pulled/qiotlctl-$chart_version.tgz" -n qiotlctl-test -f "$work/values.yaml" --wait --timeout 120s > "$work/install.log"
proxy=/api/v1/namespaces/qiotlctl-test/services/qiotlctl:http/proxy
metrics() { k get --raw "$proxy/metrics"; }
status() { k -n qiotlctl-test exec deployment/qiotlctl -- /usr/local/bin/qiotlctl -c /etc/qiotld/config.yaml outputs status; }
publish() {
  stamp=$(date +%s)
  k exec deployment/broker -- mosquitto_pub -t fixture/up -q 1 -m "{\"type\":\"17\",\"id\":$1,\"need_ack\":1,\"sensorData\":[{\"timestamp\":{\"value\":$stamp},\"co2\":{\"value\":$2}}]}"
}
wait_metric() {
  for i in {1..30}; do
    if metrics | awk -v key="$1" -v wanted="$2" '$1==key && $2==wanted {found=1} END {exit !found}'; then return; fi
    sleep 1
  done
  echo "Metric $1 did not reach $2" >&2; exit 1
}
# A report requesting an ACK must remain unacknowledged in default observe mode.
k exec deployment/broker -- mosquitto_sub -t fixture/down -C 1 -W 8 > "$work/down.txt" 2> "$work/down-error.txt" &
sub_pid=$!
sleep 1
publish 1 600
if wait "$sub_pid"; then echo 'unexpected device command/ACK' >&2; exit 1; else sub_code=$?; fi
[[ "$sub_code" == 27 ]] # mosquitto timeout; connection errors must not pass.
[[ ! -s "$work/down.txt" ]]
wait_metric qiotl_vm_samples_delivered_total 1
metrics > "$work/initial-metrics.txt"
# Persist a sample while VM is unavailable, then replace the daemon pod.
k scale deployment/vm --replicas=0
k wait --for=delete pod -l app=vm --timeout=60s
publish 2 617
wait_metric qiotl_vm_pending_samples 1
status > "$work/outage-status.json"
wait_metric qiotl_ready 0
k get --raw "$proxy/livez" > "$work/vm-outage-livez.txt"
# Endpoint probes may fail before the pending batch's HTTP timeout completes.
# Wait for delivery evidence rather than assuming every worker changes at once.
for i in {1..30}; do
  k get --raw "$proxy/healthz" > "$work/vm-outage-health.json"
  if grep -q '"id":"vm.delivery","status":"failed"' "$work/vm-outage-health.json"; then break; fi
  sleep 1
done
grep -q '"id":"vm.delivery","status":"failed"' "$work/vm-outage-health.json"
if k get --raw "$proxy/readyz" > "$work/vm-outage-readyz.txt" 2>&1; then echo 'unexpected readiness during VM outage' >&2; exit 1; fi
# The Kubernetes Service proxy replaces non-2xx bodies with its own error.
# /healthz preserves component reasons through that proxy.
uid_before=$(k -n qiotlctl-test get pods -l app.kubernetes.io/instance=qiotlctl -o jsonpath='{.items[0].metadata.uid}')
k -n qiotlctl-test logs deployment/qiotlctl --follow > "$work/shutdown.log" 2>&1 &
logs_pid=$!
sleep 1
k -n qiotlctl-test rollout restart deployment/qiotlctl
# VM is still down: the replacement must run but cannot become ready yet.
for i in {1..60}; do
  current_uid=$(k -n qiotlctl-test get pods -l app.kubernetes.io/instance=qiotlctl -o jsonpath='{.items[0].metadata.uid}' 2>/dev/null || true)
  if [[ "$current_uid" != "$uid_before" && -n "$current_uid" ]] && k get --raw "$proxy/livez" > /dev/null 2>&1; then break; fi
  sleep 1
done
uid_after=$(k -n qiotlctl-test get pods -l app.kubernetes.io/instance=qiotlctl -o jsonpath='{.items[0].metadata.uid}')
[[ "$uid_before" != "$uid_after" ]]
wait "$logs_pid"
grep -q 'observer stopped:' "$work/shutdown.log"
wait_metric qiotl_vm_pending_samples 1
status > "$work/restarted-status.json"
# Broker outage: service still exposes metrics/liveness, but readiness fails.
k scale deployment/broker --replicas=0
k wait --for=delete pod -l app=broker --timeout=60s
wait_metric qiotl_mqtt_connected 0
k get --raw "$proxy/livez" > "$work/outage-livez.txt"
if k get --raw "$proxy/readyz" > "$work/outage-readyz.txt" 2>&1; then echo 'unexpected readiness during broker outage' >&2; exit 1; fi
# Hold the outage beyond the liveness failure window to catch restart loops.
for i in {1..7}; do k get --raw "$proxy/livez" >/dev/null; sleep 5; done
restarts=$(k -n qiotlctl-test get pods -l app.kubernetes.io/instance=qiotlctl -o jsonpath='{.items[0].status.containerStatuses[0].restartCount}')
[[ "$restarts" == 0 ]]
k scale deployment/broker --replicas=1
k rollout status deployment/broker --timeout=120s
wait_metric qiotl_mqtt_subscribed 1
k scale deployment/vm --replicas=1
k rollout status deployment/vm --timeout=120s
wait_metric qiotl_vm_pending_samples 0
status > "$work/recovered-status.json"
wait_metric qiotl_ready 1
k -n qiotlctl-test rollout status deployment/qiotlctl --timeout=60s
k -n qiotlctl-test exec deployment/qiotlctl -- /usr/local/bin/qiotlctl doctor --daemon-url http://127.0.0.1:9090 --format json > "$work/doctor.json"
# VM restarted with a fresh test volume: the queued sample must arrive there.
for i in {1..30}; do
  k exec deployment/broker -- wget -qO- 'http://vm:8428/api/v1/query?query=max_over_time(qingping_iot_co2%5B1h%5D)&nocache=1' > "$work/vm-query.json"
  if grep -q '"617"' "$work/vm-query.json"; then break; fi
  sleep 1
done
grep -q '"617"' "$work/vm-query.json"
# Config checksum must roll the deployment, preserving the PVC.
uid_before=$(k -n qiotlctl-test get pods -l app.kubernetes.io/instance=qiotlctl -o jsonpath='{.items[0].metadata.uid}')
helm upgrade qiotlctl "$work/pulled/qiotlctl-$chart_version.tgz" -n qiotlctl-test -f "$work/values.yaml" --set config.data.runtime.queue_size=2048 --wait --timeout 120s > "$work/upgrade.log"
k -n qiotlctl-test exec deployment/qiotlctl -- /usr/local/bin/qiotlctl -c /etc/qiotld/config.yaml config validate
status > "$work/upgraded-status.json"
uid_after=$(k -n qiotlctl-test get pods -l app.kubernetes.io/instance=qiotlctl -o jsonpath='{.items[0].metadata.uid}')
[[ "$uid_before" != "$uid_after" ]]
# HA publication is enabled only against this disposable broker.
helm upgrade qiotlctl "$work/pulled/qiotlctl-$chart_version.tgz" -n qiotlctl-test -f "$work/values.yaml" --set config.data.outputs.home_assistant.enabled=true --wait --timeout 120s > "$work/ha-upgrade.log"
k exec deployment/broker -- mosquitto_sub -t homeassistant/sensor/qingping_aabbccddeeff/co2/config -C 1 -W 15 > "$work/ha-discovery.json"
grep -q 'qingping_aabbccddeeff_co2' "$work/ha-discovery.json"
# States are nonretained: subscribe first, then publish fresh telemetry.
k exec deployment/broker -- mosquitto_sub -t qiotlctl/AABBCCDDEEFF/co2/state -C 1 -W 15 > "$work/ha-state.json" &
ha_pid=$!
sleep 1
publish 3 650
wait "$ha_pid"
grep -q '"value":650' "$work/ha-state.json"
# Removing retained discovery must fail readiness, without breaking liveness.
k exec deployment/broker -- mosquitto_pub -t homeassistant/sensor/qingping_aabbccddeeff/co2/config -q 1 -r -n
wait_metric qiotl_ready 0
k get --raw "$proxy/healthz" > "$work/ha-missing-health.json"
grep -q 'retained discovery not observed' "$work/ha-missing-health.json"
k get --raw "$proxy/livez" > "$work/ha-missing-livez.txt"
# HA's birth prompts rediscovery and readiness recovery.
k exec deployment/broker -- mosquitto_pub -t homeassistant/status -q 1 -m online
wait_metric qiotl_ready 1
# VM failure must not mark the capture bridge offline.
k scale deployment/vm --replicas=0
k wait --for=delete pod -l app=vm --timeout=60s
wait_metric qiotl_ready 0
k exec deployment/broker -- mosquitto_sub -t qiotlctl/bridge/qiotlctl/availability -C 1 -W 5 > "$work/vm-outage-ha-bridge.txt"
grep -qx online "$work/vm-outage-ha-bridge.txt"
k scale deployment/vm --replicas=1
k rollout status deployment/vm --timeout=120s
wait_metric qiotl_ready 1
# Broker restart loses retained discovery; the HA connection must restore it.
k rollout restart deployment/broker
k rollout status deployment/broker --timeout=120s
k exec deployment/broker -- mosquitto_sub -t homeassistant/sensor/qingping_aabbccddeeff/co2/config -C 1 -W 30 > "$work/ha-rediscovery.json"
grep -q 'qingping_aabbccddeeff_co2' "$work/ha-rediscovery.json"
wait_metric qiotl_mqtt_subscribed 1
helm uninstall qiotlctl -n qiotlctl-test > "$work/uninstall.log"
k -n qiotlctl-test get pvc qiotlctl > "$work/retained-pvc.txt"
echo 'OCI round-trip, doctor, readiness reasons, independent liveness, ACK-off, VM/HA outage recovery, PVC restart, upgrade and HA reconnect passed.'
