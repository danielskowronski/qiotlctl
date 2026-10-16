# Doctor and operational health

Use `doctor` to separate configuration, MQTT authorization, retained HA discovery
and VM endpoint/queue problems. Diagnostics use the same YAML includes, ordered
`-c` overlays, profiles and cleanenv overrides as other commands.

```sh
qiotlctl doctor
qiotlctl -c config.yaml -c site.yaml doctor --device bedroom --format json
qiotlctl doctor --daemon-url http://127.0.0.1:9090
kubectl -n app-qiotld exec deployment/qiotld -- \
  qiotlctl doctor --daemon-url http://127.0.0.1:9090 --format json
```

The default overall deadline is 30 seconds (`--timeout`); each network inspection
uses `health.check_timeout`. Device selection limits MQTT/HA inspection; VM queue
evidence still describes the shared destination. `--daemon-url` reads `/healthz`
from the running process, including its actual config, mounted secrets and journal.
It cannot be combined with `-c`, `--device` or `--probe-publish`. Prefer this mode
for Kubernetes and when the daemon journal is unavailable on the workstation.

## What checks establish

| Check | Evidence and limits |
| --- | --- |
| Configuration | Effective configuration, identity/topic collisions, output settings and credential references validate. Disabled outputs are reported as disabled. |
| MQTT | Separate diagnostic client IDs avoid disconnecting the daemon. Reports connection/TLS/authentication failures and exact denied SUBACK topics. An accepted subscription with no message is not proof of a working data path or a failed device. |
| HA subscription/discovery | Checks the configured birth topic and exact retained discovery topics/payloads. Detects missing, nonretained or mismatched discovery, including silently discarded publishes. Generic models need recorded supported readings to enumerate their sensors. |
| HA availability/freshness | Inspects bridge/sensor availability and states observed during the check. Missing birth/state messages are warnings: nonretained messages and sleeping devices may not appear within the deadline. Stale/future timestamps and bad sensor quality are flagged. |
| HA application | Broker readback does not prove HA accepted/registered entities. Check HA's own MQTT login/ACLs, discovery prefix and logs for duplicate IDs or invalid payloads. |
| VM endpoint | A zero-byte POST to the configured import endpoint checks HTTP routing/auth/TLS without inserting a measurement. It is endpoint acceptance, not proof of sensor ingestion or query visibility. An endpoint that rejects empty requests is reported as a failed/unsupported probe; no synthetic metric is substituted. |
| VM delivery/queue | Reads existing journal evidence, pending count/age, last real-batch success/failure and rejection count. Missing local journals leave delivery/backlog unverified; use the daemon snapshot. Historical success alone does not establish current remote query results. |

Default `doctor` publishes no MQTT messages, commands, device ACKs or last will.
It does not create/migrate a journal, drain queues, start an exporter, or trigger
backfill. Existing SQLite data is opened read-only. VM endpoint checks do use the
zero-sample HTTP POST described above. Device-downlink publish permissions remain
explicitly unverified.

An optional scoped publish test is available:

```sh
qiotlctl doctor --probe-publish
```

This publishes a unique **nonretained** nonce only at
`<state_prefix>/diagnostics/<uuid>` and requires the same nonce to be received.
It proves access only to that diagnostic topic. It cannot certify publication
rights on device commands, HA discovery or sensor state. No retained cleanup is
needed. Its subscribe/publish ACL is covered by the example `qiotlctl/#` rule.

Doctor exit codes: **0** = required checks verified (warnings/unverified optional
checks may remain), **10** = failed required check, **11** = required evidence
missing. Invalid doctor flags use 2; failure to retrieve a daemon snapshot uses
1. A config-loading/validation failure reported inside a doctor result uses 10.
Read the check statuses, not just the overall result. `--format json` emits the
versioned snapshot on stdout; command failure summaries go to stderr.

## Continuous checks and Kubernetes probes

| Endpoint | Behavior |
| --- | --- |
| `/livez` | HTTP 200 while the HTTP server is serving. Existing startup/liveness probes use this. Broker/exporter/device outages do not fail it. |
| `/readyz` | HTTP 200 when capture and required enabled outputs are ready; otherwise 503 with each blocking reason and remedy. |
| `/healthz` | HTTP 200 JSON snapshot, including `ready`, check status, required/blocking flags, last-check/success/failure times and counts. Inspect the JSON to determine readiness. |
| `/metrics` | Existing connection/exporter/queue metrics plus `qiotl_health_check_blocking{check="..."}`; fixed component IDs avoid device/topic label cardinality. |

The Kubernetes Service proxy may replace a 503 response body with a generic
error; use `/healthz` or `doctor --daemon-url` to see the component reasons
through that proxy.

HTTP probes only read cached evidence; requesting them never triggers MQTT/VM
traffic or database scans. Workers update checks in the background. Startup stays
unready until required first checks complete. Previously successful exporter
checks have a failure grace; an initial failure has none. Idle VM is allowed
after an endpoint check even before a real sample arrives. Successful empty
probes **never clear a failed real batch**. Queue age is enqueue age, not device
measurement age. Oldest-pending limits detect stalled delivery even if the HTTP
endpoint accepts probes. Bounded evidence expiry detects stalled check workers.

```yaml
---
health:
  require_vm: true       # Only applies when VM export is enabled.
  require_ha: true       # Only applies when HA export is enabled.
  check_interval: 30s
  check_timeout: 5s      # Positive and no longer than check_interval.
  failure_grace: 30s     # 0s makes exporter failures block immediately.
  max_pending_age: 5m
  log_interval: 5m       # Reminder interval for unchanged failures.
```

Each key has a `QIOTLCTL_HEALTH_<UPPERCASE_KEY>` cleanenv override. Interval/log
interval must be at least 1s, timeout/maximum pending age positive and grace
nonnegative. These fields merge normally across files; explicit false/zero
values are preserved. To make a particular enabled output advisory, set its
`require_*` to false. It still reports failures and retries.

Overall unready status does not pause capture, queueing, retries or replies.
HA bridge availability follows capture MQTT/storage health, so a VM failure
does not mark the HA bridge offline. The metrics Service includes unready pods.
`helm --wait` may time out during an exporter outage even though capture is
working. Liveness stays independent, preventing dependency-driven restart loops.

Health transitions and recovery are logged to stderr as JSON or text according
to `--format`; unchanged failures get rate-limited reminders. Messages include
the check, reason, remedy and available queue counts. Credentials, endpoint
queries and arbitrary remote response bodies are omitted. Readings remain on
stdout. Connection/authentication/subscription errors retry without terminating
the daemon; invalid config and unusable storage still fail explicitly.

## Troubleshooting

Readiness includes enabled VM/HA by default. **Grant qiotld subscribe permission
on its HA discovery topics**, as well as publish permission. The [EMQX example](../charts/qiotlctl/examples/emqx-acl.conf)
and [deployment ACL table](DEPLOYMENT.md#mqtt-permissions-and-emqx-acls) include
the extra permission. HA itself still needs its own discovery/state subscriptions
and birth publication rights. A broker PUBACK alone cannot prove delivery.

For missing discovery, start with `doctor --daemon-url` and inspect `ha.connection`,
`ha.subscription`, `ha.publication` and `ha.discovery`. Correct any denied topics.
If a broker silently filtered previously attempted discovery, trigger rediscovery
through HA's normal online birth or restart qiotld after fixing its ACLs. Do not
publish dummy messages to device downlinks. If matching retained discovery is
present but entities remain absent, inspect HA's separate permissions and logs.

For VM failures, inspect `vm.endpoint`, `vm.delivery`, `vm.backlog`: 401/403 point
to credentials/authorization; 404 to routing; 429 to capacity/rate limits; 5xx to
the service. TLS, DNS and connection/timeouts have distinct messages. Fix the
cause and let durable pending work retry. Do not delete the journal to make
readiness pass.

For repeatable local validation, use `go test -race ./...`,
`bash scripts/diagnostics-smoke.sh` (disposable EMQX/VM), and
`VERSION=dev make image k8s-smoke` (isolated kind cluster). See
[testing guide](VALIDATION.md) for coverage and limitations.
