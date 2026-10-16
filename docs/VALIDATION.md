# Testing and validation

Tests distinguish wire encoding, decoded meaning, MQTT delivery and device
behavior. A passing encoder test does not establish firmware support, and a
broker acknowledgement does not establish device execution.

## Run the checks

From the module root:

```sh
make check
make race
go mod verify
make chart-check
```

Go, Python 3 and Git are required for the complete check target. Helm is required
for chart rendering tests; those tests skip when Helm is unavailable. Tests use
temporary files and local loopback listeners, so a sandbox must permit local test
networking. The release tests create temporary Git repositories and never publish.

For a Go vulnerability scan:

```sh
GOBIN="$PWD/.local/tools" go install golang.org/x/vuln/cmd/govulncheck@v1.9.0
.local/tools/govulncheck ./...
```

Rerun the scanner after dependency or compiler changes. Its results are
point-in-time evidence, not a guarantee against unknown vulnerabilities or an
exhaustive audit of container operating-system packages.

## Test coverage

| Area | Coverage |
| --- | --- |
| Protocols | Independent HEX vectors, lengths/checksums, malformed JSON, duplicate keys, round trips, unknown fields and fuzz targets |
| Device interpretation | Sanitized captured examples, Python-reference parity, timestamps, status/quality and model-dependent fields |
| Settings | Signed/scaled values, bounds, alarms, raw values, typed/raw collisions, limit overrides and human-readable readback |
| Configuration | cleanenv conversion, layered files/documents, includes, profiles, null/false/zero behavior, strict keys and credential redaction |
| Commands | Subscribe-before-publish, matching/stale/duplicate responses, timeouts, disconnects, local serialization, journal limits and exporter isolation |
| Storage | Read-only replay, missing-source handling, transactional capture/output work, credential exclusion, capacity limits and restart recovery |
| VictoriaMetrics | Labels/units/timestamps, deduplication, invalid-value rejection, durable retry, HTTP/TLS diagnostics and backlog health |
| Home Assistant | Discovery identity, freshness, expiry, retained evidence, model changes, reconnect/birth republishing and scoped cleanup |
| Health | Readiness reasons/grace/expiry, independent liveness, recovery reporting and concurrent snapshots |
| Helm | Defaults, Secret/config overlays, PVCs, pod security, optional monitoring/VMUser resources and invalid-value rejection |
| Packaging | Source allowlist, deterministic source snapshot, symlink rejection, checksum format and clean/tagged publication checks |

`testdata/history.json` contains 24 sanitized cases derived from a larger recorded
corpus. Expected measurements are compared with the pinned Python implementation.
The source database is not required to run the committed tests. See
[fixture provenance and regeneration](PROTOCOLS.md#history-derived-fixtures).

## Local broker and exporter integration

```sh
bash scripts/diagnostics-smoke.sh
make image VERSION=dev
make k8s-smoke VERSION=dev
```

Docker is required for both integration scripts. The Kubernetes script also needs
Buildx, kind, kubectl, Helm and curl. Each script creates its own test resources;
the Kubernetes script uses a dedicated kubeconfig and loopback OCI chart registry.
Neither script loads an operator's broker configuration or historical database.

The diagnostics script uses disposable EMQX and VictoriaMetrics instances. It
checks denied subscriptions, a silently rejected publication despite PUBACK,
retained discovery readback, recovery, the optional nonce probe and zero-sample
VM imports that leave the series inventory unchanged.

The Kubernetes script checks installation under Restricted pod admission, OCI
chart round trips, default ACK-off behavior, independent liveness during broker
and VM outages, readiness reasons, buffered delivery across pod replacement,
configuration upgrades, HA discovery recovery and retained PVCs on uninstall.

Logs and snapshots are written under `.local/diagnostics-smoke-*` and
`.local/qiotlctl-smoke-*`. The scripts print the exact directories. Their own
containers/cluster/registry are removed on exit unless the Kubernetes test is run
with `KEEP_TEST_CLUSTER=1` for debugging.

## Coverage limits

- Firmware observations currently cover CGP22C and CGDN1 Lite v1. CGP23W,
  extended HEX history and typed-v2 layouts have specification/synthetic coverage;
  that is not equivalent to hardware validation.
- HA broker readback does not verify application entity registration, the HA
  client's ACLs or duplicate entity handling in a real HA installation.
- Empty VM imports check endpoint acceptance, not ingestion/query visibility.
  Server-side parsing, storage durability and query-cache behavior need separate
  checks for the chosen deployment.
- Optional VMAuth/VMUser and monitoring-controller resources are render-tested;
  controller reconciliation is not covered by the disposable cluster test.
- kind's default CNI does not enforce NetworkPolicy. Verify enforcement using the
  cluster's actual CNI and operator placement.
- Buildx cross-compilation and smoke execution do not cover every supported OS,
  CPU or Kubernetes version. Revalidate the target environment before rollout.

Model-specific live observations and their limits are recorded in
[FIRMWARE_NOTES.md](FIRMWARE_NOTES.md). Release verification is described in
[RELEASING.md](RELEASING.md).
