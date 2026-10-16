# Configuration contract

Authored YAML files and CLI/Helm-generated configuration start with `---`.
Input without a document marker remains supported for existing configurations.

The schema is `api_version: qiotl/v1alpha1`. The YAML and environment conversion use [cleanenv](https://github.com/ilyakaznacheev/cleanenv); recursive map merging and copying use [koanf/maps](https://github.com/knadh/koanf). Application glue handles file inclusion, device normalization, settings-profile resolution, source provenance and validation.

## Sources and precedence

From lowest to highest:

1. Built-in defaults.
2. YAML files in the order supplied by repeated `--config` / `-c`.
3. cleanenv's `QIOTLCTL_*` overrides.
4. Explicit command flags, where available (`--record`, `--listen`, `--respond`, command deadlines and device selection).

When no config flag is supplied, the CLI reads `QIOTLCTL_CONFIG`, and the daemon reads `QIOTLD_CONFIG`. These are OS path lists (`:` separated on macOS/Linux). Otherwise the default paths are `~/.config/qiotlctl/config.yaml` and `/etc/qiotld/config.yaml`. A missing default file allows offline/env-only operation; a missing explicitly requested file is an error. `config validate` requires a usable broker/device configuration but makes no connection.

An `include` accepts a path or a list of paths/globs. Paths are relative to the including file, globs are sorted, and includes load before the document that names them. YAML documents separated by `---` load in order. Cycles, unmatched globs, duplicate keys, unknown fields, and nesting deeper than 32 includes are rejected. Each file is limited to 4 MiB. YAML anchors and merge keys work within a document; they do not span files. YAML values are not interpolated as shell expressions.

Maps merge recursively. Scalars and ordinary lists replace earlier values. `null` removes an inherited key after all layers have been merged (a removed setting uses its default if one exists). In particular, `devices: {bedroom: null}` removes that device. The legacy device list is normalized into a map keyed by alias before merging; it therefore merges by alias, rather than replacing the whole inventory.

Settings profiles resolve **after all YAML files/documents are merged**, so a device may refer to a profile declared in a later file. For desired settings, the resulting precedence is ancestor profiles -> selected profile -> device settings -> explicit `--settings-file` -> `--set`/raw CLI inputs. Device settings win even if a profile definition was supplied in a later file. The settings file and CLI start from the resolved device settings only with `--from-config`; without it they remain explicit-only. Environment overrides still target application settings, not individual device/profile settings.

Relative TLS, password, connection-file and storage paths resolve against the file defining that value. Environment/flag paths are relative to the current working directory; use absolute paths for services. File-sourced paths accept `~/`. Config selection via flags also accepts `~/`.

## Existing installations

The CLI default directory is `~/.config/qiotlctl`; the daemon default directory
is `/etc/qiotld`. Move an existing configuration tree, including its relative
includes and credential files, to the new directory or continue selecting its
current path with `--config`. Paths supplied explicitly are not renamed.

Application environment overrides use `QIOTLCTL_*`; config selection uses
`QIOTLCTL_CONFIG` for the CLI and `QIOTLD_CONFIG` for the daemon. Update service
definitions, shell environments and Secret/ConfigMap environment keys accordingly.
Earlier environment spellings are not aliases. YAML setting names, profile
syntax, the `qiotl/v1alpha1` schema and SQLite data remain compatible.

The default HA state prefix and bridge ID are both `qiotlctl`. To keep an existing
installation's MQTT identity, set `outputs.home_assistant.state_prefix` and
`bridge_id` explicitly to its current values before upgrading. Otherwise update
the broker ACLs to allow the new prefix and arrange rediscovery. Keep per-device
HA IDs stable to preserve entity identity. Use the previous broker/prefix/bridge
configuration when cleaning up its discovery manifest; see [HA recovery and
cleanup](OUTPUTS.md#ha-identity-and-discovery).

## Device mapping

```yaml
---
devices:
  bedroom:
    id: stable-device-id
    model: CGP22C
    protocol: hex
    enabled: true
    topics:
      telemetry: qingping/EXAMPLE/up
      commands: qingping/EXAMPLE/down
```

The map key is the operator alias. IDs default to MAC without colons if `mac` is supplied, otherwise to the alias; supply a stable `id` if aliases may change. Aliases are unique ignoring case; IDs and topic ownership are unique. Topics are exact: wildcards are intentionally rejected because they cannot provide an unambiguous device/direction map. Disabled devices are omitted from subscriptions and replay.

Known models infer their protocol. Unknown models default to `auto` and use generic decoding; wire recognition still validates the complete frame. A fixed model/protocol mismatch is an error. Generic binary legacy data includes temperature, humidity and battery; the ambiguous third slot is not guessed. An observed product ID can identify CGP22C. JSON typed sensor names can be interpreted generically.

The old list format with `alias`, `mac`, `model`, `protocol`, `topic_up` and `topic_down` is accepted directly. Supplying old and new forms for the same topic in one layer is an error. Device changes belong in YAML overlays; cleanenv environment overrides target application settings, not arbitrary per-device paths.

## Reusable settings profiles

Define a name under `profiles`, then select it with `devices.<alias>.profile`. Names such as `standard_co2` are user-defined, not built-in presets. Each profile contains `settings` and optionally one `extends` parent. Profiles share desired settings; each device retains its own model, protocol, identity, topics, enabled state and response policy. A settings profile is separate from the Go model adapter that describes protocol capabilities.

```yaml
---
profiles:
  common_display:
    settings:
      display: {temperature_unit: celsius, clock_format: 24h}
  standard_co2:
    extends: common_display
    settings:
      reporting: {upload_interval: 5m, record_interval: 30s}
      co2:
        indicator: {yellow_min_ppm: 1000, yellow_max_ppm: 1500}

devices:
  bedroom:
    model: CGP22C
    profile: standard_co2
    topics: {telemetry: qingping/bedroom/up, commands: qingping/bedroom/down}
    settings:
      reporting:
        upload_interval: 10m  # Override the profile's 5m.
        record_interval: null # Leave this inherited setting unmanaged.
  office:
    model: CGP22C
    profile: standard_co2
    topics: {telemetry: qingping/office/up, commands: qingping/office/down}
```

`bedroom` manages a 10-minute upload period, display units/clock format and the CO2 indicator. It does not manage recording interval. `office` inherits the entire profile, including 5-minute upload and 30-second recording. Loading either device does not send anything; apply with `apply-settings bedroom --from-config` when desired.

Profiles can be split into files using the normal `include` mechanism or repeated `-c`. Definitions with the same name merge across files before inheritance. Profile names are case-sensitive; aliases remain case-insensitive for CLI selection. Each device selects one profile, and each profile extends at most one parent. Unknown references, cycles, malformed definitions, unknown profile-level keys and chains longer than 32 profiles are errors, including in unused profiles.

Profiles are settings fragments. Protocol support, setting names/types/ranges and typed/raw conflicts are validated on the **effective settings of every configured device**, including disabled devices. Unused profile settings are not validated against a protocol; a fragment may also be made valid by a child/device override or removal. To validate a profile's values, select it on a device with the intended model/protocol. The limit override applies to inherited values just as it does to direct values.

The same merge rules apply at every level:

| Input | Effect |
| --- | --- |
| Omitted key | Inherit the lower-precedence value, if any |
| `false`, `0`, `0s` | Explicit values; never treated as absent |
| Nested mapping | Merge individual keys; e.g. override one CO2 endpoint |
| List | Replace the whole list, including alarm schedules, pages and `raw.hex`; `[]` clears it subject to setting validation |
| Setting or subtree `null` | Remove management of that setting/subtree, without publishing null, zero or a reset |
| Device `settings: null` | Remove all inherited desired settings |
| Device `settings: {}` | Inherit unchanged; an empty mapping removes nothing |
| Device `profile: null` or `profile: ''` | Stop using a profile; retain explicitly authored device settings |
| Profile `extends: null` or `extends: ''` | Remove the parent, retaining the profile's own settings |
| `profiles: {standard_co2: null}` | Delete the definition; remaining references are errors |

Raw JSON objects merge by key; raw HEX and alarm lists replace atomically. Unknown fields never gain an assumed encoding through inheritance. A later non-null value can reintroduce a removed setting.

### Inspecting and migrating configuration

```sh
qiotlctl -c examples/config.yaml config show
qiotlctl -c examples/config.yaml config explain bedroom
qiotlctl -c examples/config.yaml -c examples/overrides/bedroom.yaml config explain bedroom
qiotlctl -c examples/config.yaml apply-settings bedroom --from-config --dry-run
```

`config show` displays the effective configuration with profiles expanded and profile references/definitions omitted. `config explain ALIAS` returns JSON with the selected profile, ancestor-to-child chain, effective settings, and each winning setting's absolute file path, YAML document number (one-based) and normalized YAML path. Null removals are retained in `sources` with `removed: true`; lists have one source because they replace atomically. This explains the loaded configuration before command-specific flags. It does not record every overridden value. Both commands redact broker credentials/raw setting values. Redacted output is an inspection view, not a credential-preserving backup.

Existing configurations need no migration and keep `qiotl/v1alpha1`. To adopt profiles, move repeated settings into `profiles.<name>.settings`, select that name on each applicable device, and leave only device overrides under `devices.<alias>.settings`. Keep IDs/topics/models and response settings on their devices. Compare `config show`/`config explain` and an application dry-run before applying. The [example config](../examples/config.yaml) includes shared display, CO2 and Lite profiles; the [optional overlay](../examples/overrides/bedroom.yaml) demonstrates an override and a removal.

## Broker and credentials

```yaml
---
mqtt:
  url: mqtts://broker.example:8883
  username_env: MY_MQTT_USERNAME
  password_file: /run/secrets/mqtt-password
  tls:
    ca_file: /run/secrets/broker-ca.pem
    # Optional mutually authenticated TLS:
    # cert_file: /run/secrets/client.pem
    # key_file: /run/secrets/client-key.pem
    # server_name: broker.example
```

Accepted schemes: `mqtt`/`tcp`, `mqtts`/`ssl`/`tls`, `ws`, `wss`. Default ports are 1883, 8883, 80 and 443. URL userinfo, query strings and fragments are rejected; credentials have separate fields. TLS uses system trust plus an optional CA and requires TLS 1.2 or newer. Client certificate/key must be provided together. There is no insecure verification bypass.

Credential sources:

- `mqtt.username` / `mqtt.password`: literal values, redacted by `config show`.
- `username_env` / `password_env`: required variable references, resolved after YAML loading.
- `password_file`: secret text; one trailing newline is stripped. Mutually exclusive with `password_env`.
- `connection_file`: a JSON object with `user`, `pass`, `mqtt_uri`. It fills values not explicitly supplied in YAML.

Reference/file credentials override literal YAML credentials. Direct `QIOTLCTL_MQTT_USERNAME`, `QIOTLCTL_MQTT_PASSWORD` and `QIOTLCTL_MQTT_URL` override those sources. Referenced files/variables must still be readable/present when configured. Errors never echo invalid environment values. `config show` displays paths/reference names and topics, but redacts resolved username/password.

One-shot command sessions always generate a separate client ID, even if the config specifies a daemon ID. They subscribe only to the target uplink and require publish permission on its downlink.

Do not set a client ID unless needed; the default is unique per process. Reusing an active client's ID can disconnect it. Observation requires subscription permission for every configured topic. Set `runtime.observe_commands: false` for telemetry-only accounts; denied subscriptions fail clearly rather than reporting readiness.

## Defaults and environment

| YAML key | Default | Environment |
| --- | --- | --- |
| `mqtt.url` | empty | `QIOTLCTL_MQTT_URL` |
| `mqtt.username`, `mqtt.password` | empty | `QIOTLCTL_MQTT_USERNAME`, `QIOTLCTL_MQTT_PASSWORD` |
| `mqtt.username_env`, `mqtt.password_env` | empty | `QIOTLCTL_MQTT_USERNAME_ENV`, `QIOTLCTL_MQTT_PASSWORD_ENV` |
| `mqtt.password_file`, `mqtt.connection_file` | empty | `QIOTLCTL_MQTT_PASSWORD_FILE`, `QIOTLCTL_MQTT_CONNECTION_FILE` |
| `mqtt.client_id` | random per run | `QIOTLCTL_MQTT_CLIENT_ID` |
| `mqtt.keepalive` | `60s` | `QIOTLCTL_MQTT_KEEPALIVE` |
| `mqtt.connect_timeout` | `15s` | `QIOTLCTL_MQTT_CONNECT_TIMEOUT` |
| `mqtt.reconnect_max` | `30s` | `QIOTLCTL_MQTT_RECONNECT_MAX` |
| `mqtt.qos` | `1` (0 or 1) | `QIOTLCTL_MQTT_QOS` |
| `mqtt.tls.ca_file`, `cert_file`, `key_file`, `server_name` | empty | `QIOTLCTL_MQTT_TLS_CA_FILE`, `QIOTLCTL_MQTT_TLS_CERT_FILE`, `QIOTLCTL_MQTT_TLS_KEY_FILE`, `QIOTLCTL_MQTT_TLS_SERVER_NAME` |
| `runtime.mode` | `observe` (`respond` opts in) | `QIOTLCTL_MODE` |
| `runtime.queue_size` | `1024` (1-100000) | `QIOTLCTL_QUEUE_SIZE` |
| `runtime.observe_commands` | `true` | `QIOTLCTL_OBSERVE_COMMANDS` |
| `commands.timeout` | `30s` | `QIOTLCTL_COMMAND_TIMEOUT` |
| `storage.path` | empty (disabled) | `QIOTLCTL_STORAGE_PATH` |
| `storage.capture_raw` | `true` | `QIOTLCTL_STORAGE_CAPTURE_RAW` |
| `http.listen` | empty in CLI; daemon falls back to `:9090` | `QIOTLCTL_HTTP_LISTEN` |

Use `qiotlctl config env` for the list generated directly from cleanenv tags. Both executables use the same setting variables; only their config-selection variables differ. `qiotld --listen ''` explicitly disables HTTP. False and zero environment values are honored when valid. Empty strings clear string values; invalid numeric/duration/bool strings fail validation. There is no automatic `.env` loading or hot reload: restart to change configuration.

## Response policy

Automatic replies default off. Global `runtime.mode: respond` enables supported profile replies; `devices.ALIAS.responses.enabled: true/false` overrides that inheritance. `--respond=false` on `watch` or the daemon explicitly disables replies regardless of per-device values. `QIOTLCTL_MODE=observe` changes the inherited global policy but does not remove explicit per-device opt-ins.

`responses.clock` defaults to `off`; `type18-heartbeat` enables the CGDN1 compatibility clock message. `responses.min_interval` defaults to `15m` and must be at least `1m`. These device properties merge by alias across files like the rest of the inventory. They deliberately have no dynamic environment-variable naming convention: put device policies in a YAML overlay. [Command documentation](COMMANDS.md) describes the exact exchanges and coexistence behavior.

## Desired device settings

`devices.ALIAS.settings` accepts the human-readable fields and explicit raw extension blocks listed in [SETTINGS.md](SETTINGS.md). Shared validation rejects unknown named fields, unsupported protocol mappings, invalid ranges/resolutions and collisions between typed and raw wire properties. Every property is optional; false and zero are preserved. Loading config and running the daemon never applies desired settings. Use `apply-settings ALIAS --from-config` explicitly.

For experimental values outside documented/default ranges, pass `--ignore-official-limits` on each invocation that loads the config, including `config validate`, offline encoding, explicit application or `qiotld`. It applies after all YAML layers/environment overrides are resolved and survives command-session revalidation. It changes only device-value validation: types, wire widths, exact units, unknown fields, MQTT constraints and runtime settings are still checked. There is no persistent YAML/environment switch. Known firmware exceptions do not need the flag; see [the settings guide](SETTINGS.md#trying-values-outside-official-limits).

Settings participate in the existing recursive overlays, sorted includes and null-removal rules. Lists (including alarm lists and raw HEX lists) replace earlier lists. Null removes management; it never resets a device. `--settings-file` overlays a bare settings mapping and `--set` overrides selected paths. Application environment overrides still use cleanenv; use device YAML overlays/CLI overrides instead of inventing per-device environment variable names. Raw values are omitted by `config show` because undocumented fields can contain credentials.

Profile resolution is a loading step only. `watch`, `qiotld`, validation and offline encoding never apply desired settings automatically.

## Output configuration

Output configuration includes `outputs.victoriametrics`, `outputs.home_assistant`, `storage.max_bytes` and per-device `home_assistant` identity/expiry options. They use the same layering and cleanenv overrides as existing fields. The global exporters default to disabled; enabling them requires durable storage when starting an observer. A profile still contains settings only, never a shared HA identity. See [complete output configuration and examples](OUTPUTS.md#configuration), [HA ID mapping](OUTPUTS.md#ha-identity-and-discovery), and [the optional output overlay](../examples/outputs.yaml).

## Health configuration

Health configuration includes `health.require_vm`, `require_ha`, `check_interval`, `check_timeout`,
`failure_grace`, `max_pending_age` and `log_interval`. Defaults, validation and
YAML examples are in [the diagnostic guide](DIAGNOSTICS.md#continuous-checks-and-kubernetes-probes).
Each has a `QIOTLCTL_HEALTH_<UPPERCASE_KEY>` cleanenv override and uses normal
file layering. Required enabled outputs affect readiness; liveness and
capture continue during exporter failures.
