# Architecture and extension points

The Go module contains two entry points: `qiotlctl` for operator commands and
`qiotld` for continuous observation and export. Both use the same configuration,
protocol, device and settings packages.

## Device composition

A configured device combines an alias, stable identity, exact MQTT topics, a wire
protocol and a model profile. Model profiles describe capabilities and interpret
model-dependent fields; they do not form an inheritance hierarchy. Shared settings
provide common operations while retaining model-specific features and raw fields.

For example, a CGP22C profile selects HEX and interprets the legacy third sensor
slot as CO2. An unknown HEX device retains that slot as ambiguous instead of
inventing a CO2 or pressure reading. JSON typed readings can often be interpreted
without knowing the model. A reusable YAML settings profile is a separate concept:
it shares desired values without sharing device identity or topics.

## Package boundaries

| Package | Responsibility |
| --- | --- |
| `internal/protocol` | Validate/encode wire envelopes, preserve ordered TLVs and unknown JSON fields, identify sensitive data |
| `internal/device` | Model capabilities, sensor interpretation and optional automatic responses |
| `internal/domain` | Normalized captures, events, samples, readings and command results |
| `internal/settings` | Human-readable setting catalogue, exact conversions, limits, alarms and raw extensions |
| `internal/config` | Strict YAML loading, file/profile resolution, cleanenv overrides and validation |
| `internal/transport` | Shared MQTT options and bounded inspection helpers |
| `internal/command` | Plan explicit operations, match response evidence, apply settings and journal outcomes |
| `internal/observer` | MQTT subscriptions, capture processing, optional responses, reconnect and output workers |
| `internal/storage` | SQLite capture/replay, command journal, output queues and schema management |
| `internal/output` | VictoriaMetrics batching/retry, HA discovery/state and exporter diagnostics |
| `internal/backfill` | Read-only historical sources and resumable VM queue insertion |
| `internal/diagnostic`, `internal/health`, `internal/doctor` | Cached health state, HTTP probes, console reporting and operator diagnostics |
| `internal/cli` | Cobra commands shared by both executables |

## Observation and persistence

Exact topic routes identify each configured device and message direction.
Incoming MQTT metadata and bytes enter a bounded queue, then decode into canonical
events. A valid envelope may contain unsupported fields: known readings remain
available, with warnings and raw unknown fields. Invalid envelopes become error
events. Normalized output redacts recognized credentials.

When recording is enabled, capture and derived output work are committed in one
SQLite transaction. Export workers operate independently of MQTT capture. VM
network failures leave work pending for retry; HA keeps the latest eligible
sensor state and republishes discovery after reconnect or HA birth. Measurement
timestamps remain distinct from receive time. Health endpoints read cached state
rather than making network requests on the probe path.

The daemon uses one persistent local database. The Helm chart runs one replica
with Recreate updates. SQLite state and process-local locks do not provide
coordination between independent replicas or workstations.

## Explicit commands

One-shot sessions use unique MQTT client IDs, subscribe to the selected uplink,
wait for SUBACK, publish nonretained commands and disconnect after completion.
They do not start daemon exporters or automatic responses. An uncertain write is
not retried automatically.

JSON request IDs correlate acknowledgements where the protocol supports them.
HEX settings reports and JSON type-28 reports are uncorrelated observations.
Results distinguish publication, acknowledgement, matching readback and unknown
outcomes. Per-device locks serialize operations within a process; independent
controllers may issue conflicting commands. See [command semantics](COMMANDS.md).

## Adding device support

1. Add sanitized wire fixtures and record the model, firmware and source of each
   interpretation. Distinguish documented behavior from observed firmware quirks.
2. Extend the model profile and decoder without changing MQTT routing. Preserve
   unknown or ambiguous fields until their meaning is established.
3. Add typed settings to the shared catalogue only when units, widths and value
   mappings are understood. Keep raw encoding available for unsupported fields.
4. Define command completion evidence and optional response policy separately.
   Broker PUBACK must not be reported as device execution or settings readback.
5. Update sensor/export mappings where needed and add focused protocol, command
   and output tests. Document support and limits in the
   [protocol reference](PROTOCOL_SETTINGS_REFERENCE.md).

Automatic settings reconciliation, distributed controller ownership, arbitrary
BLE sub-device management and firmware-update workflows are not implemented.
