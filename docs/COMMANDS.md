# Active sessions and commands

Automatic replies remain **disabled by default**. `qiotlctl command` and `settings get` execute only the requested operation, even when the configuration enables daemon replies. They use a fresh client ID, subscribe only to the selected uplink, wait for SUBACK, publish nonretained to its downlink, and disconnect after completion. Downlink subscription permission is unnecessary.

## Operator commands

```sh
qiotlctl -c config.yaml settings get living-room
qiotlctl -c config.yaml command living-room realtime --duration 1m --interval 5s --verify
qiotlctl -c config.yaml command living-room sync-time
qiotlctl -c config.yaml command bedroom sync-time --wait-for-uplink --timeout 6m
qiotlctl -c config.yaml command bedroom raw --file command.bin
qiotlctl -c config.yaml command bedroom raw --file command.hex --input-format hex
```

Aliases are case-insensitive. `--dry-run` emits a redacted plan, payload size/hash and expected completion evidence without opening a broker connection, journal or command session. It works without broker configuration. `--record journal.sqlite` overrides `storage.path`; `--record ''` disables recording for this invocation, including with a daemon config
that enables exporters. One-shot sessions never start those exporters. When
recording, `storage.max_bytes` applies to the command journal too. JSON is the default result format; `--format text` summarizes the outcome and prints any settings response.

`--timeout` overrides `commands.timeout` (default `30s`, environment `QIOTLCTL_COMMAND_TIMEOUT`). It covers connection, subscription, an optional uplink wait, publication and response waiting. `--wait-for-uplink` delays sending until a valid, nonretained, nonduplicate report arrives. This is useful for devices with short receive windows; use a deadline longer than their upload interval. Timestamp-bearing commands are built after this wait. Cancellation disconnects the session and reports uncertainty if publication was attempted. Bounded connection cleanup and journal flushing can add several seconds after the operation deadline.

### Settings reads

CGDN1 sends `{"type":"28"}` and waits for a new type-28 settings report. The normalized response includes common interval names and preserves unknown settings. This is an uncorrelated read: the protocol does not echo a request ID, so a simultaneous spontaneous report can supply the observation. The result says so explicitly.

CGP22C ordinary `settings get` returns unsupported (exit 3) without connecting. Empty `0x32` requests did not elicit settings reports in live tests. `watch` and replay still decode received `0x39` reports.

`settings get --reassert-upload-interval 5m` explicitly writes the upload interval to solicit a report. On tested CGP22C firmware, this single property consistently returned `0x39`; several other properties, including physically verified display changes, applied silently. This is a write-based probe with uncorrelated readback, not a read-only query or a universal acknowledgement mechanism. Use a current or desired interval, and leave absent fields unresolved. See [firmware observations and scope](FIRMWARE_NOTES.md).

### Realtime reporting

CGDN1 supports temporary realtime reporting with a whole-second duration of `1m`-`1h`, and a whole-second reporting interval greater than `2s` and no longer than the duration. The wire fields `duration` and `up_itvl` are strings, as in the vendor example. The mode ends automatically.

`--ignore-official-limits` permits other durations/intervals, including zero, negative values and intervals exceeding the duration. Both values must still be whole seconds fitting the tool's signed 32-bit integer contract; wire values remain decimal strings. This changes encoding acceptance, not the device's implementation. For example, preview an experimental one-second cadence with `qiotlctl command living-room realtime --duration 30s --interval 1s --ignore-official-limits --dry-run`. The option also applies to [typed settings and config loading](SETTINGS.md#trying-values-outside-official-limits), without changing ACK policy or retry behavior.

There is no documented device acknowledgement. The default result is `published`. With `--verify`, the CLI waits for two distinct type-12 sample timestamps close to current time, received after publication. This confirms observed realtime reporting, without proving which controller caused it or that the requested cadence/duration will be honored in full. Existing reports from another realtime session can provide that evidence.

### Clock synchronization

CGP22C sends configuration key `0x15` with the current Unix time. CGDN1 sends the observed type-18 timestamp workaround used by the Python tool. Both return publication evidence only: neither has a documented correlated clock-adjustment reply. The tool never manufactures a future time or rewrites historical timestamps.

### Raw commands and IDs

`command ALIAS raw` validates the wire envelope and model/protocol compatibility, then sends the exact supplied bytes. Input can be binary/raw JSON, hex text or base64. Model-specific semantic effects are the caller's responsibility. There are no automatic retries.

For JSON type-17 commands that return type-18 acknowledgements, add `--wait-ack`. The payload must contain a fresh nonnegative integer `id`; matching requires the target uplink, expected response type, matching `ack_id`, and a valid numeric `code`. An explicit conflicting device MAC is rejected. Code zero means acknowledged; nonzero means rejected. Acknowledgement does not prove settings readback.

Use the offline encoder to add a random 31-bit request ID rather than deriving one from the current second:

```sh
qiotlctl encode raw --protocol json --file request.json \
  --new-request-id --output-format raw > request-with-id.json
qiotlctl -c config.yaml command living-room raw \
  --file request-with-id.json --wait-ack
```

The encoder replaces any existing ID only when explicitly asked. Raw publication itself never modifies the payload. Each operation also gets a separate UUID for local result/journal tracking; that UUID is not a wire correlation ID.

Per-device serialization applies **within one process**. Independent CLI processes or workstations can issue commands concurrently. There is no shared lock, leader election, distributed ordering or conflict resolution. IDs distinguish responses where the protocol supports them; they cannot prevent one controller's settings from overriding another's. For reports without IDs, results explicitly state the weaker evidence.

## Results and exit codes

Each JSON result includes operation/device identity, status, evidence, timestamps, payload hash/size and, where relevant, a normalized response. Payloads and credentials are not dumped into the result. Retained replies, MQTT duplicate deliveries and reports received before publication do not complete operations. A settings report with a valid but older device timestamp is also rejected; reset/epoch device clocks cannot establish age.

| Status | Meaning |
| --- | --- |
| `published` | MQTT publish completed. With QoS 1 this includes broker PUBACK; it does not prove device execution or, for MQTT 3.1.1, downstream ACL acceptance. |
| `acknowledged` | A matching application response returned code zero. |
| `verified` | The operation's defined report/observation arrived. Uncorrelated evidence is labelled. |
| `rejected` | A matching device response returned a nonzero code. |
| `unknown` | Publication was attempted, but the final device outcome could not be established. |
| `timed_out`, `cancelled`, `failed` | Failure before publication, or another explicitly reported failure. |

`--require-confirmation` makes publication-only completion exit unsuccessfully. It does not invent a response for operations without one. Command sessions do not reconnect or resend after uncertain delivery. The daemon retains its normal reconnect/resubscribe behavior.

| Exit | Meaning |
| --- | --- |
| 0 | Requested completion criterion met |
| 1 | General/local I/O or journal error |
| 2 | Invalid command/configuration |
| 3 | Unsupported named operation/profile |
| 4 | Connection, subscription, publication or receive-session failure |
| 5 | Deadline exceeded; inspect status to see whether publication was attempted |
| 6 | Device rejected the command |
| 7 | Strict confirmation requested, but only publication established |
| 8 | Another command for this device is active in this process |
| 10 | Doctor: a required diagnostic check failed (including reported config failures) |
| 11 | Doctor: required evidence remains unverified |
| 130 | Command cancelled |

Cobra argument/flag parsing errors outside an operation currently use the general exit code 1. An I/O failure may prevent a result from being written; inspect stderr as well as the exit status.

## Automatic replies

Enable replies in the daemon or `watch` with `runtime.mode: respond`, or enable only a selected device:

```yaml
---
runtime:
  mode: observe
  observe_commands: false
devices:
  living-room:
    responses:
      enabled: true
      clock: off
```

A per-device `enabled` value overrides the global mode. Omitting it inherits the mode. `--respond` explicitly enables replies on the selected devices; `--respond=false` force-disables all replies on them, including per-device overrides. Ordinary command sessions ignore this responder configuration.

CGDN1 exchanges:

- Startup type 10 -> type 11 with current timestamp, code zero and the documented `dev_list` wildcard.
- History type 17 with `need_ack: 1`, a valid integer ID and sensor-data array -> type 18 with matching `ack_id`, code zero and current timestamp.
- Optional `responses.clock: type18-heartbeat` -> an independent type-18 timestamp on a type-13 heartbeat, at most once per `responses.min_interval` (default `15m`, minimum `1m`). Startup and historical replies also reset this rate limit. This compatibility behavior is separate from historical ACKs.

There is no guessed HEX ACK and no generic reply to unknown message types. Retained, replayed, outbound, malformed, wrong-identity and over-30-second queued packets never trigger replies. Retransmitted history may receive another ACK after capture. Invalid host time before 2020 suppresses replies; backwards host-clock jumps delay compatibility messages instead of generating future timestamps. Restarting resets the in-memory clock rate limit.

When recording is enabled, inbound history commits before its application ACK. Outbound intents and final outcomes are journalled too. A storage error stops processing before the corresponding reply. MQTT transport PUBACK can still precede SQLite persistence, so this is not a promise of lossless ingestion. Automatic replies use nonretained QoS 0, are not replayed after reconnect, and report uncertain delivery on connection changes.

Run only one automatic responder for a device. Multiple passive observers and explicit operator commands can coexist with the existing Python responder; leave Go replies disabled during that overlap. Slow log consumers can still delay the observer's processing queue; VM and HA network delivery run in independent workers.

## Journal and metrics

The existing capture schema gains an additive `command_events` table. It appends prepared/attempting/published/final result transitions keyed by operation UUID. Outbound captures recorded before PUBLISH are labelled **intent**; consult the outcome, since a crash can happen between persistence and transmission. Captures observed on a downlink remain separate wire observations. Existing qiotlctl databases are extended in place; legacy Python databases remain read-only and cannot become recording targets.

Daemon `/metrics` adds automatic reply publication/error counters and a counter of samples with pre-2020 or future timestamps. One-shot outcomes are returned to the CLI and recorded in the optional journal; they are not exported through a separate short-lived HTTP endpoint.

References: [Lite MQTT protocol](https://developer.qingping.co/private/communication-protocols/air-monitor-lite/mqtt), [HEX protocol](https://developer.qingping.co/private/communication-protocols/public-mqtt-hex).

## Applying settings

`apply-settings ALIAS --from-config` uses `devices.ALIAS.settings`; `--set path=value`, `--settings-file file.yaml`, `--raw-hex 0xKEY=BYTES` and `--raw-json '{"key":value}'` select or override values. `--dry-run` previews without networking; `--single-property` isolates wire properties; `--force` skips JSON's preflight comparison. See [the settings guide](SETTINGS.md) for supported paths, examples, readback policy and per-field result meanings.

`settings get --format yaml` exports human settings. HEX `settings get --reassert-upload-interval 5m` explicitly performs a populated write to request a report; the ordinary query still fails as unsupported. `apply-settings --readback-upload-interval 5m` opts into the same extra write for post-write verification. Automatic replies remain off even if daemon configuration enables them. Each operation has a deadline and journal record; multi-operation application holds one process-local device lock, but no workstation-wide lock.

## Output commands

`ha preview` renders discovery offline. `ha cleanup` previews removal of obsolete topics owned by this journal; only `--publish` performs those MQTT publications. `backfill vm` imports an immutable legacy/Go SQLite snapshot with resumable progress and measurement-time filters; `--dry-run` prints JSON lines without network or durable writes. `outputs status`, `outputs rejections`, `outputs drain-vm`, and `outputs prune-vm` inspect/recover/retain the delivery queue. See [outputs, exact flags and recovery rules](OUTPUTS.md). None of these commands enables device acknowledgements or sends device commands.

## Doctor

`qiotlctl doctor` inspects the effective configuration and services without
publishing MQTT messages or changing journal data. `--daemon-url URL` reads the
running daemon's cached `/healthz`; `--format json` gives structured checks.
`--probe-publish` opts into a nonce round trip on a dedicated diagnostic topic.
See [the diagnostic guide](DIAGNOSTICS.md) for evidence limits, exit codes,
readiness, ACLs and VM endpoint probes.
