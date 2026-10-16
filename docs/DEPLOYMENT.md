# Local builds and Kubernetes deployment

The project provides local container builds and an OCI Helm chart. The source is
`https://github.com/danielskowronski/qiotlctl`; image metadata links that repository
using `org.opencontainers.image.source`. Nothing in the normal build or test
workflow publishes to GitHub/GHCR or changes an existing Kubernetes cluster.

## Build artifacts

Run these commands from the Go module directory. Prerequisites: Go, Make, Docker
with Buildx, and Helm. The local cluster test additionally needs kind, kubectl and
curl. Source/CLI release bundles additionally require Python 3. Packaging adds no
runtime Go dependencies or Helm plugins.

```sh
make check
make release-local VERSION=0.2.0
make image VERSION=dev
make image-archive VERSION=0.2.0
make chart-package VERSION=0.2.0 CHART_VERSION=0.2.0
```

- `image` loads the Docker server's native platform into its image store. Override
  `PLATFORM=linux/amd64` for a specific runtime. The tag is
  `ghcr.io/danielskowronski/qiotlctl:<VERSION>`; a GHCR-shaped name does not upload it.
- `image-archive` builds `linux/amd64,linux/arm64` into
  `dist/qiotlctl-<VERSION>.oci.tar`, with SBOM/provenance attestations and SHA256 file.
  Override `PLATFORMS` if needed. No remote cache export is configured.
- `chart-package` runs chart tests, then creates `dist/qiotlctl-<CHART_VERSION>.tgz`
  and its SHA256 file. `APP_VERSION` defaults to `VERSION`. Chart and application
  versions are independent; change `Chart.yaml` defaults when preparing a release.
- `dist/image-*.json` contains Buildx result metadata. `REVISION` defaults to the
  local Git HEAD with `-dirty` when source files are uncommitted, or `unknown`
  outside a Git checkout. Files are named `image-<VERSION>-<mode>.json`.
  [Release preparation](RELEASING.md) describes source/CLI bundles and clean-source checks.

The dedicated `qiotlctl-build` builder is created if missing without changing your
selected builder. Override `BUILDER` to reuse another suitable builder. It retains
compilation caches; remove it later with `docker buildx rm qiotlctl-build` if no
longer needed. Container bases are digest-pinned; source paths are explicitly
allowlisted. Configuration, history, `.local`, tests and examples are excluded. The image
includes project, dependency and Go runtime license notices.

The image runs as UID/GID 65532 and contains both executables. It has no shell.
For operator commands, override the entry point:

```sh
docker run --rm --entrypoint /usr/local/bin/qiotlctl \
  ghcr.io/danielskowronski/qiotlctl:dev --version
```

Cross-compilation uses Buildx's build/target platform variables rather than
emulating the Go compiler. The runtime image contains system CA certificates.
Local `image` loading omits attestations for compatibility; the multi-platform
archive and explicit publication include them. SBOM/provenance describe a build;
they are not cryptographic signatures. See [Docker multi-platform builds](https://docs.docker.com/build/building/multi-platform/)
and [attestations](https://docs.docker.com/build/metadata/attestations/).

## Publishing

Only the following explicit targets upload to GHCR. Do not invoke them as part
of a local build. `image-push` builds from the current sources before publishing;
use the same reviewed checkout and version as the tested artifact. Publishing
requires a clean committed checkout, matching Chart.yaml versions, and a local
`v<VERSION>` tag at HEAD. `chart-push` repackages the reviewed chart before uploading.
Follow the [release checklist](RELEASING.md) first.
There is no automatic GitHub Actions release workflow.

```sh
# Authenticate separately using your credential manager or password-stdin.
# A local GHCR PAT (classic) needs write:packages; cluster pulls need read:packages.
docker login ghcr.io --username YOUR_GITHUB_USERNAME
helm registry login ghcr.io --username YOUR_GITHUB_USERNAME

make image-push VERSION=0.2.0
make chart-push VERSION=0.2.0 CHART_VERSION=0.2.0
```

The destinations are `ghcr.io/danielskowronski/qiotlctl:0.2.0` and
`oci://ghcr.io/danielskowronski/charts/qiotlctl`, chart version `0.2.0`.
`IMAGE` and `CHART_REGISTRY` can override these defaults. Keep image and chart
repository paths distinct. Record the returned digests and avoid reusing release
tags. `image.digest` takes precedence over `image.tag` when deploying.

Helm's push destination omits the chart basename and tag; Helm supplies both from
Chart.yaml. A downloaded chart can be installed by OCI digest. No chart repository
index or `helm repo add` is needed. See [Helm OCI registries](https://helm.sh/docs/topics/registries/)
and [GHCR authentication and repository linking](https://docs.github.com/en/packages/working-with-a-github-packages-registry/working-with-the-container-registry).
GitHub packages start private; changing visibility remains a separate action.
For private images, provide an existing pull Secret using
`imagePullSecrets: [{name: ghcr-pull}]`. Helm's registry login authorizes chart
retrieval on the operator's machine; it does not provide kubelet image credentials.

## Upgrading an existing installation

Use `ghcr.io/danielskowronski/qiotlctl` for the image and
`oci://ghcr.io/danielskowronski/charts/qiotlctl` for the chart. A GitHub repository
rename does not copy existing GHCR image/chart versions to these paths; publish
and verify the required artifacts before changing a running deployment.

For an in-place Helm upgrade, keep the existing release name and namespace.
Preserve the existing `app.kubernetes.io/name` selector with `nameOverride` and
the existing resource name with `fullnameOverride`; Deployment selectors are
immutable. Keep the same PVC, VMUser and credential Secret references. The
`app-qiotld` namespace in examples is for a new installation, not an instruction
to move an existing release. Render and compare the chart with the installed
resources before applying it. Renaming a release or namespace is a separate
migration and must not start a second writer against the same state.

The chart's default config mount is `/etc/qiotld`, and generated credential
environment names use `QIOTLCTL_*`. Update custom `extraEnv`, `extraEnvFrom`, paths
and overrides as needed. Explicit config paths and desired settings are still
supported. Preserve configured HA state-prefix/bridge IDs or update MQTT ACLs
and discovery deliberately; see [configuration migration](CONFIGURATION.md#existing-installations).

## Chart configuration

The [values reference](../charts/qiotlctl/values.yaml) is the supported chart
interface, checked by `values.schema.json`. The application validates its complete
merged configuration before connecting to MQTT. Default values intentionally have
no devices, no automatic responses and no enabled exporters. Supply an inventory
and matching network rules before expecting readiness.

```sh
helm lint charts/qiotlctl --strict
helm template qiotld charts/qiotlctl -n app-qiotld \
  -f charts/qiotlctl/examples/minimal.yaml --set fullnameOverride=qiotld > rendered.yaml

# Once an image is available in the selected cluster/registry:
helm upgrade --install qiotld charts/qiotlctl -n app-qiotld --create-namespace \
  -f my-values.yaml --set fullnameOverride=qiotld --wait --timeout 3m
```

The examples use release and resource name `qiotld` in namespace `app-qiotld`;
adapt the troubleshooting commands if choosing different names.

Rendering includes Secrets when values contain credentials: protect rendered
files and Helm release access accordingly. Prefer externally managed Secrets.
The minimal example contains a sample device identity; review topics and broker
address before deploying. Additional examples cover [HA only](../charts/qiotlctl/examples/home-assistant.yaml),
[VM and HA outputs](../charts/qiotlctl/examples/outputs.yaml),
[VMUser](../charts/qiotlctl/examples/vmuser.yaml), [TLS mounts](../charts/qiotlctl/examples/tls.yaml),
[existing config/PVC](../charts/qiotlctl/examples/existing-config.yaml) and
[multiple files/profiles](../charts/qiotlctl/examples/multiple-files.yaml).

`config.data` is native Go application YAML serialized into a generated Secret.
An existing `config.existingSecret` or `config.existingConfigMap` replaces that
base file completely; `config.data` is then unused. The default filename is
`config.yaml` and the directory is `/etc/qiotld`. Existing ConfigMaps should contain
only nonsensitive information. All referenced resources must be in the workload
namespace.

`config.files` maps additional filenames to literal YAML strings; those files are
stored in the generated Secret even with an external base configuration.
`config.extraSources` accepts Kubernetes projected Secret/ConfigMap sources,
including key-to-path mappings. Paths across all sources must be unique. Files
can use native `include` directives. `config.overlayFiles` explicitly orders the
additional files passed after the base file. A file is not automatically loaded
just because it is mounted.

Use literal file strings or external files when relying on application `null`
removal semantics: Helm merges structured values before the application's own
merge, and can consume nulls. Raw YAML strings preserve them. Profiles and settings
are interpreted only by the application. Starting/upgrading the daemon never runs
`apply-settings`; commands remain explicit operator actions.

`extraEnv` / `extraEnvFrom` support cleanenv overrides, including Secret references.
The names remain `QIOTLCTL_*` for daemon application fields. Chart-generated
`--config` arguments take precedence over `QIOTLD_CONFIG`; use chart configuration
paths and `overlayFiles` to change the file list. `extraArgs` can append CLI flags,
including `--ignore-official-limits` when your desired settings require it.

`mqttCredentials.existingSecret` and `vmCredentials.existingSecret` inject the
respective `QIOTLCTL_*_USERNAME` / `PASSWORD` variables using configurable keys.
An empty key omits that variable. With a chart-created VMUser, use the operator's
standard `username` and `password` keys. Do not duplicate those names in extraEnv.
For file-based credentials or TLS, use `extraVolumes` / `extraVolumeMounts` and
absolute paths in application config. Mounted files and environment credentials
are read at startup. Generated config changes update a checksum and roll the pod;
external file/credential changes require a rollout restart (or your own reloader).

## MQTT permissions and EMQX ACLs

MQTT authentication, topic authorization and Kubernetes NetworkPolicy are separate.
An allowed TCP connection and working device subscriptions do not grant permission
to publish HA discovery. HA uses a separate MQTT connection with the daemon's
configured broker credentials; Home Assistant itself normally has its own login.
Both must reach the same broker/topic space.

With the default prefixes, the broad Qingping ACL contract is:

| MQTT user | Action | Topic filter | Purpose |
| --- | --- | --- | --- |
| qiotld/operator | Publish + subscribe | `qingping/#` | Device telemetry, explicit commands and opt-in replies |
| qiotld/operator | Publish + subscribe | `qiotlctl/#` | Decoded state, sensor/bridge availability and operator inspection |
| qiotld/operator | Publish + subscribe | `homeassistant/sensor/#` | Retained discovery, explicit cleanup and readback verification |
| qiotld/operator | Subscribe | `homeassistant/status` | HA birth notification and rediscovery |
| Home Assistant | Subscribe | `homeassistant/#` | Discovery announcements |
| Home Assistant | Subscribe | `qiotlctl/#` | Sensor state and availability |
| Home Assistant | Publish | `homeassistant/status` | Online birth message and offline last will |

The [copyable EMQX ACL file](../charts/qiotlctl/examples/emqx-acl.conf) includes both
users. Replace `QIOTLD_MQTT_USERNAME` and `HA_MQTT_USERNAME` with their actual MQTT
login names, not Kubernetes Secret names. Add the rules to the existing file
authorizer before any matching deny; preserve unrelated rules and the existing
fallback policy. For the EMQX built-in database authorizer, create equivalent
username/action/topic entries using the table. The Helm chart does not configure
EMQX or apply this file.

The example allows all QoS and retained/nonretained combinations. HA publication
uses QoS 1, retained discovery and availability (including the bridge last will),
and nonretained readings. Device commands/replies are nonretained. Granting publish
permission does not enable device responses; those remain controlled by runtime
and per-device response settings.

`homeassistant/sensor/#` covers all sensor discovery, not only Qingping. MQTT
wildcards cannot match part of a level: `qingping_*` is not a prefix wildcard.
For narrower discovery access, list exact nodes such as
`homeassistant/sensor/qingping_bedroom/#` and repeat for each HA ID. Adapt the ACLs
when changing device topics, `discovery_prefix`, `state_prefix` or `birth_topic`.
HA's configured discovery prefix and birth topic must match the daemon's values.

EMQX file rules use first-match order. Dashboard/API edits can make
`data/authz/acl.conf` the active file instead of the original configured file;
update the active authorizer, then reconnect affected clients to check the change.
See [EMQX file authorization](https://docs.emqx.com/en/emqx/latest/guides/access-control/authz/file.html).

## Troubleshooting missing Home Assistant discovery

For a device whose stable ID and HA ID are both `bedroom`, default topics are:

| Purpose | Topic | Retained |
| --- | --- | --- |
| CO2 discovery | `homeassistant/sensor/qingping_bedroom/co2/config` | Yes |
| CO2 reading | `qiotlctl/bedroom/co2/state` | No |
| CO2 availability | `qiotlctl/bedroom/co2/availability` | Yes |
| Bridge availability | `qiotlctl/bridge/qiotlctl/availability` | Yes |
| HA birth/status | `homeassistant/status` | Controlled by HA |

Discovery uses `devices.<alias>.home_assistant.id` (default: lowercase device ID);
state uses `devices.<alias>.id`. The bridge topic uses `bridge_id`. These identities
can differ. Known models announce sensors on connection/startup without waiting
for a new measurement. Generic models announce supported sensors once observed.

1. **Check the configuration mounted in the running pod.** Confirm
   `outputs.home_assistant.enabled: true`, the broker/prefixes and that the device
   and its HA output are not disabled. A local values edit needs a Helm upgrade;
   external config/Secret changes need a rollout. Environment overrides can win
   over YAML. With the standard deployment name/path:

   ```sh
   kubectl -n app-qiotld exec deployment/qiotld -- \
     qiotlctl -c /etc/qiotld/config.yaml config show
   kubectl -n app-qiotld exec deployment/qiotld -- \
     qiotlctl -c /etc/qiotld/config.yaml ha preview
   ```

   `config show` emits redacted **YAML**, not JSON; do not pipe it directly to
   `jq`. Preview emits JSON without publishing, and forces HA on for the preview;
   a successful preview does not prove the daemon has HA enabled. If using
   `config.overlayFiles`, repeat `-c` for those files in the pod's argument order
   in these inspection commands and the status command below.

2. **Listen using Home Assistant's MQTT integration.** In its configuration UI,
   listen to `homeassistant/#`, then `qiotlctl/#`. Retained discovery should be
   visible immediately if it reached that broker and HA's login can subscribe.
   An administrator's MQTT client can see messages that HA's login cannot.
   See [HA's MQTT testing instructions](https://www.home-assistant.io/integrations/mqtt/#testing-your-setup).

3. **If discovery is absent, inspect authorization and delivery status.** The
   current worker subscribes to `homeassistant/status` before publishing discovery.
   A denied birth subscription blocks all announcements. Permission to publish
   the bridge last will/availability is also needed before discovery can work.

   ```sh
   kubectl -n app-qiotld exec deployment/qiotld -- \
     qiotlctl -c /etc/qiotld/config.yaml outputs status
   ```

   For the daemon's complete live diagnosis, including connection failures:

   ```sh
   kubectl -n app-qiotld exec deployment/qiotld -- \
     qiotlctl doctor --daemon-url http://127.0.0.1:9090
   ```

   `doctor` reports denied topics and missing/mismatched retained discovery.
   Add **subscribe** permission on qiotld's discovery topics for readback.
   `/readyz` includes required enabled exporters; `/healthz` explains each check.
   Metrics expose `qiotl_ha_errors_total`, `qiotl_ha_publications_total`,
   `qiotl_ha_healthy` and `qiotl_health_check_blocking`. Disabled outputs do not
   establish delivery. MQTT 3.1.1 PUBACK alone does not prove delivery or HA entity
   registration. After fixing silently filtered publications, use HA's online
   birth or restart qiotld to republish. See [diagnostics](DIAGNOSTICS.md).

4. **If discovery reaches HA but entities do not appear, inspect HA's MQTT logs.**
   Check rejected payloads and duplicate `unique_id` values from old manually
   configured sensors. Confirm HA's discovery prefix matches. Preserve IDs when
   migrating existing entities; do not change them merely to hide a conflict.

5. **If entities appear but are unavailable, check freshness.** Both bridge and
   sensor availability must be `online`. Measurements need valid device time,
   compatible units/quality and age within `expire_after` (default 600 seconds).
   States are nonretained, so a new listener may need to wait for the next reading;
   HA's online birth message requests a refresh of still-fresh state. Historical
   backfill does not populate HA. See [HA freshness and recovery](OUTPUTS.md#ha-freshness-and-recovery).

## Runtime and storage

The chart uses one replica and `Recreate`, with no HPA. This avoids overlapping
old/new deployments during normal upgrades. It is not distributed fencing: do not
run another responding daemon or HA publisher for the same devices/bridge, or
force-delete a pod on an unfenced node. Independent observing instances need their
own databases and unique MQTT identities. No Kubernetes RBAC/API access is needed.

Pod defaults satisfy Restricted admission: UID/GID/fsGroup 65532, RuntimeDefault
seccomp, dropped capabilities, no privilege escalation or API token, read-only
root filesystem. `/tmp` is a bounded emptyDir and `/data` is writable storage.
Resource defaults request 50m CPU/64Mi memory and limit memory to 256Mi; tune from
operational metrics for your inventory/history workload. The CSI driver must
support fsGroup ownership or the existing volume must already be writable.

Use `http.listen: :9090`, including with external config. The chart's container
port/probes use 9090; `service.port` changes only the Service's port. `/livez` tests
the HTTP-serving process; `/readyz` requires connected/subscribed MQTT, usable
storage and required enabled exporters. `/healthz` returns cached component
reasons. Export failures use `health.failure_grace` after a previous success;
initialization is unready until first checks finish. `helm --wait` may time out
during an exporter outage. Export outages do not fail liveness. Readiness does
not suspend MQTT processing. The metrics Service publishes unready endpoints so
outage metrics remain reachable. It is internal ClusterIP only, without Ingress.

The 30-second termination grace allows the daemon's bounded capture flush and
shutdown; VM pending work is resumed from SQLite after restart rather than forcing
an unlimited shutdown drain. Abrupt failures can still lose in-memory captures.

Persistence defaults to a 20Gi PVC with a 10Gi application soft budget, retaining
space for WAL/checkpoints. Size it for capture history and your longest VM outage;
there is no automatic raw-history retention. The budget is not a filesystem quota.
Use `persistence.existingClaim` to reuse storage; no second PVC is created.
`storageClass: null` uses the cluster default, while `storageClass: ''` explicitly
requests no class. Only ReadWriteOnce / ReadWriteOncePod are accepted; the latter
requires compatible CSI support. For disposable tests only, disable persistence
with explicit `allowEphemeral: true`, and lower `storage.max_bytes` accordingly.

Use block-backed storage with local filesystem semantics, not NFS/RWX shared
storage: [SQLite WAL requires same-host shared memory](https://www.sqlite.org/wal.html).
A chart-created PVC is annotated for retention on uninstall by default. This
retains a Helm object, not a backup; PV reclaim policy and external deletion still
matter. Reinstall using `existingClaim`, and delete retained storage only after
checking its contents. Existing Python databases are read-only import sources;
never use one as the Go recorder destination.

For backup, stop the daemon, wait for the pod to terminate, and snapshot/copy the
volume consistently, including SQLite sidecar files if present. Restore to a
separate claim and validate before switching the release. Preserve the original
claim when testing upgrades. Helm rollback does not roll back database migrations;
check database schema compatibility before downgrading an application image.

## VM, HA and monitoring

`outputs.victoriametrics` pushes historical sensor samples directly to the import
API. `/metrics` is the separate operational exporter. Choose `monitoring.kind`
`vmServiceScrape` or `serviceMonitor` to generate one scrape resource; its CRD must
already exist, and labels must match the collector selector. Templates fail if a
requested CRD is absent. Offline `helm template` tests must supply the corresponding
`--api-versions` entry. The chart installs neither CRDs nor monitoring backends.

VMUser creation is opt-in, in the daemon namespace so its generated
`vmuser-<name>` Secret is mountable. The VictoriaMetrics operator must watch that
namespace; VMAuth's user/namespace selectors must select it. Target namespaces are
independent of credential namespaces. Explicit targetRefs/routes are required;
there is no default unrestricted route. The supplied VMCluster example routes
`/api/v1/import` to tenant 0 on vminsert using `targetPathSuffix`. Review that tenant
and endpoint against your VMAuth installation. Existing VM users in another
namespace require a separately managed credential Secret in the daemon namespace;
this chart does not copy Secrets across namespaces or grant cross-namespace RBAC.
See the [VMUser reference](https://docs.victoriametrics.com/operator/resources/vmuser/).

HA uses the configured MQTT broker. Preserve your per-device HA IDs (for example
`bedroom`) and bridge ID through upgrades; changing them can create new entities.
Enable only after reviewing `qiotlctl ha preview`. The chart has no automatic
cleanup/uninstall hook that removes discovery or publishes device commands.

NetworkPolicy defaults to denying unspecified traffic, allowing only DNS to the
configured DNS selectors. Add broker/VM egress and collector ingress explicitly.
An endpoint's namespaceSelector and podSelector are combined; an empty namespace
selector intentionally permits matching pods in any namespace. External endpoints
use ipBlock CIDRs and ports; Kubernetes NetworkPolicy cannot select DNS names.
Adjust DNS rules for NodeLocal DNS or nonstandard installations. Enforcement
requires a capable CNI; kubelet probe behavior must also be checked on your CNI.

## Recovery, backfill and Python cutover

VM retries and pending payloads survive pod restarts on the PVC. Delivery is at
least once: an accepted request followed by a lost local checkpoint can repeat.
HA restores latest state/discovery with existing freshness rules, rather than
replaying historical measurements as current. A daemon outage cannot capture
messages it never received; MQTT clean sessions do not promise broker catch-up.
See [output recovery and retention](OUTPUTS.md).

For Python history backfill, mount an immutable snapshot read-only into an
operator pod/job using the same image, and a separate persistent state database.
Override its command to `qiotlctl backfill vm`, explicitly providing config,
`--sqlite /history/qingping_mqtt.sqlite3`, a stable `--job` and `--state-db` path.
First preview with `--dry-run`; repeat the same job to resume. Do not let an
independent job share the daemon's SQLite database across nodes. Alternatively,
run the operator locally with a persistent local state database. Keep source
snapshots unchanged between retries; backfill checkpoints are tied to that source.
No chart hook starts backfill or writes settings automatically.

Deploy initially with automatic responses and HA disabled. VM comparison should
use a test destination/prefix to avoid duplicate production ingestion. After
validation, stop the Python responder before enabling Go response policies.
Enable HA separately after identity review. Retain the Python history and its
claim until the migration is verified.

| Python chart value | Go chart/application equivalent |
| --- | --- |
| `config.existingSecret`, `configDir`, `configFilename` | Same chart names; Secret contents must use native Go configuration |
| `config.broker.host/port` | `config.data.mqtt.url` with scheme and port |
| broker username/password | `mqttCredentials` Secret keys or native credential references |
| `clientIdPrefix` | Omit `mqtt.client_id` for a random process ID, or set an explicit unique ID; no render-time randomness |
| `keepaliveSeconds` | `mqtt.keepalive`, e.g. `60s` |
| `cleanSession` | Go currently always uses clean sessions; no equivalent chart switch |
| `config.devices` | `config.data.devices`; old alias/MAC/topic list syntax remains accepted |
| `config.loggingDB` | `config.data.storage`; empty path disables recording, outputs require storage |
| loggingDB persistentVolume options | `persistence.enabled/existingClaim/storageClass/accessModes/size/mountPath` |
| `config.victoria_metrics` | `config.data.outputs.victoriametrics` |
| VM `retry_attempts` | Durable retries with `retry_max` backoff; not a finite attempt count |
| VM `ignore_ssl_errors`, CA path | `insecure_skip_verify`, `ca_file`; verification stays enabled by default |
| VM credential Secret/key options | `vmCredentials` |
| VM credentials `vmUserCRD` | `vmUser`; workload namespace, explicit targetRefs |
| `imagePullSecrets: [name]` | Kubernetes object syntax `[{name: name}]` |
| service account/security/resources/scheduling | Equivalent top-level chart controls |
| networkPolicy egress/DNS/ingress | Equivalent controls; no hardcoded EMQX/VM endpoints |

## Repeatable local integration test

```sh
make image VERSION=dev
make k8s-smoke VERSION=dev
```

The script creates a uniquely named kind cluster with a dedicated kubeconfig, a
loopback-only OCI registry, synthetic MQTT broker and disposable VM. It round-trips
the chart through that registry and installs the retrieved package under Restricted
pod admission. It checks ACK-off behavior, liveness/readiness through outages,
queued VM delivery after pod replacement, configuration upgrades, PVC retention, and HA discovery/state recovery against the
disposable broker.
It never opens the production broker or uses private configuration/history.

The test removes its own cluster/registry on exit and saves evidence under
`.local/qiotlctl-smoke-*`. `KEEP_TEST_CLUSTER=1` preserves them for debugging; remove
only the names printed by the test when finished. It does not change your current
Kubernetes context. kind's default CNI does not enforce NetworkPolicy; those rules
are schema/render validated, not claimed as enforcement-tested. Optional monitoring
operators/VMUser are also render-tested separately. Actual validation results are
recorded in [VALIDATION.md](VALIDATION.md).
