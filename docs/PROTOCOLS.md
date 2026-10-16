# Protocol contracts and evidence

Sources reviewed for this implementation:

- [Qingping HEX MQTT protocol](https://developer.qingping.co/private/communication-protocols/public-mqtt-hex).
- [Air Monitor Lite JSON MQTT protocol](https://developer.qingping.co/private/communication-protocols/air-monitor-lite/mqtt).
- [Air Monitor JSON MQTT protocol](https://developer.qingping.co/private/communication-protocols/air-monitor/mqtt).
- [Previous Python implementation](https://github.com/danielskowronski/qingping-iot-mqtt/tree/d4fa28a2bd182227b2396c33a0e38f1e50d75631), pinned for fixture comparison.

## Layers

`internal/protocol` validates lossless wire envelopes and supports raw encoding. `internal/device` composes a model profile with interpretation through an `Adapter` interface. `internal/domain` defines normalized events and command results. `internal/command` plans model-aware operations and matches completion evidence; `internal/transport` shares MQTT connection settings. The observer routes exact topics into an alias, stable ID, profile and direction. Persistence and exporters consume events; they do not inspect TLV bytes.

A model is a profile/capability description, not a Go inheritance hierarchy. Add profile-specific decoding and fixtures without changing the MQTT runtime. Generic JSON typed sensors work independently of model identity. Binary legacy fields whose meaning varies by model stay ambiguous until a profile or observed product ID resolves them. CGP23W is a specification-derived profile, not a live-tested device.

## Implemented coverage

| Area | Behavior | Evidence |
| --- | --- | --- |
| HEX envelope | `CG`, command byte, little-endian uint16 body length, ordered 1+2+N TLVs, additive uint16 checksum | Independent vectors, captured frames, truncation/checksum/length tests |
| Legacy history `0x03` | Timestamp + interval + packed 6-byte groups; FF terminator | CGP22C historical corpus, Python parity |
| Legacy network `0x14` | Timestamp, packed temperature/humidity/CO2/battery, signed RSSI | CGP22C corpus, Python parity |
| Extended history `0x33` | Base readings from 8-byte groups; auxiliary bytes preserved with warning | Specification-derived; live verification pending |
| Typed v2 `0x85`, layout 4 | Signed temperature, humidity, CO2, per-value F0 error codes | Vendor example + synthetic negative temperature; live verification pending |
| Metadata and settings | Firmware/product/power state, common periods, ranges, offsets, alarm schedule fields | Corpus where present; remaining fields specification-derived |
| CGP22C alarm events | Sensor/condition/threshold plus timestamped readings | Captured high CO2 alarm |
| JSON envelope | Object-only, rejects duplicate keys and trailing documents; string/numeric message type | Corpus and malformed-input tests |
| JSON measurements | Typed groups, timestamps, temperature/humidity/CO2/PM/battery, RSSI heartbeat | CGDN1 corpus, Python parity |
| JSON status | Error/init/disabled/not-installed statuses separate from values; battery status is charging state | Synthetic status tests; units/unknown status preserved |
| Unknowns | Ordered unknown/repeated TLVs and unknown JSON fields retained | Codec tests and observed undocumented keys |
| Lite settings query | Type 28; normalized fresh settings report with unknown fields retained | Live CGDN1 verification; no request ID in this exchange |
| Lite temporary realtime | Type 12 with string interval/duration; optional two-sample observation | Live CGDN1 verification; no documented acknowledgement |
| Raw JSON correlation | Type-17 request ID matched to type-18 ack_id/code | Scripted MQTT peer; typed writes and readback tests |
| Clock commands | HEX 0x32/key 0x15; JSON observed type-18 timestamp workaround | Wire vectors; HEX and JSON publication tested live, clock adjustment unconfirmed |
| Automatic responses | Explicitly enabled CGDN1 startup/history ACK and rate-limited heartbeat clock compatibility | Unit/scripted MQTT peer only; left disabled on live devices |
| Typed settings | Shared human catalogue, exact scales, alarms, raw extensions; offline and live application | Independent vectors, bounds/overlay tests, scripted peers and scoped live checks |

Raw JSON encoding normalizes whitespace/key order. HEX decode/encode retains byte order and repeated keys. `decode --explain` reports raw JSON fields or indexed TLV offsets, lengths and values. Normalized events redact credential fields; explicit raw inspection does not.

## Current settings support

The tool provides a shared settings catalogue for configuration validation, CLI overrides, offline encoding and readback interpretation. See [SETTINGS.md](SETTINGS.md) for all human-readable paths and [PROTOCOL_SETTINGS_REFERENCE.md](PROTOCOL_SETTINGS_REFERENCE.md) for raw keys, units, ranges, unsupported features and deviations from vendor documentation. Encoding support is not proof of execution on every model.

HEX typed coverage includes upload/record/event-repeat/delay/high-frequency periods, CO2 measurement/automatic calibration/indicator bounds, battery auto-off, temperature/clock format, LED/TVOC display, timezone/SNTP, signed CO2/temperature/humidity/PM offsets and scheduled temperature/humidity/pressure/CO2 thresholds with optional sound. JSON includes common periods, display/night-mode/screensaver/pages, timezone, acknowledgement preference, CO2 ASC, offsets and threshold arrays; PM/CO2 sampling and threshold scales retain explicitly labelled legacy-reference interpretations. Unknown properties use literal HEX TLVs or JSON values.

Decoded events retain the original `settings` map and add `settings_config`, using the same human names as YAML. Unknown properties remain raw, subject to credential redaction. Missing settings stay missing. Pressure-alarm thresholds use the vendor's /100 kPa scale.

`apply-settings` validates all requested fields before connecting, queries JSON state to skip matching typed properties, publishes explicit patches, and compares individual readback values. Raw properties deliberately send even when unchanged. JSON acknowledgement IDs are correlated; settings reports are not. Silent writes report weaker evidence. Automatic replies remain disabled; no daemon reconciliation or rollback is performed.

CGP22C read-only query remains unsupported. `settings get bedroom --reassert-upload-interval 5m` is an explicitly named write-based probe, and `apply-settings --readback-upload-interval 5m` opts into a separate probe after a silent write. These never masquerade as read-only operations. CGDN1's observed 10-second record period and 120-second display rotation are accepted as documented compatibility exceptions.

## Event contract (schema version 1)

Each event includes protocol, message type, receive time, direction, optional device/alias/model/topic/raw record ID, samples, metadata, settings, alarms, unknown fields, warnings and an optional error. `response_required` is descriptive. Only an explicitly enabled model reply policy can publish during live observation; decode and replay never reply.

Each sample retains device timestamp, origin (`historical`, `realtime`, `event`, `heartbeat`) and time quality. Each reading contains sensor, numeric value, unit, quality and optional sensor/charging status. Consumers must examine quality and time quality rather than treating every numeric value as valid.

Time quality is `missing` for zero/absent timestamps, `before-2020` for implausibly early values, `future` when more than five minutes ahead of receive time, otherwise `device`. Original timestamps are retained. Stored measurement timestamps are never repaired or retimed, and there is no sample deduplication. Outbound messages are decoded as settings/control metadata and never produce fabricated measurements.

A structurally valid message can contain an unknown/malformed field. Known valid fields remain useful; problematic fields become warnings plus raw unknown data. Structural envelope errors are fatal to that message, preserved as error events by observation/replay.

## History-derived fixtures

`testdata/history.json` contains 24 sanitized cases extracted from recorded device traffic. Network identities and salts are replaced, credential/serial TLVs are removed, and measurements/timestamps remain intact. Expected samples come from the pinned Python decoder with its model sensor filtering applied. Regenerate with:

```sh
python3 tools/extract_fixtures.py /path/to/history.sqlite3 testdata/history.json \
  --reference /path/to/qingping-iot-mqtt
```

The fixtures cover HEX `0x31`, `0x34`, `0x39`, JSON startup/heartbeat/history/settings and observed outbound time messages. Undocumented HEX keys such as `0x06`, `0x67`, `0x6a` remain raw. Product key `0x38 == 0x0033` consistently identified captured CGP22C reports; this mapping is based on observation.

The Python tool's JSON type-18 behavior is a clock-synchronization workaround, not evidence that every packet requires an acknowledgement. Automatic responses remain disabled by default; see [command behavior](COMMANDS.md).

[Model-specific firmware notes](FIRMWARE_NOTES.md) describe the tested CGP22C write/report behavior, silent display changes, missing alarm fields, measurement cadence and CGDN1 settings responses. These observations do not establish support on other models or firmware. Missing fields remain unknown, and historical reports must not be restored as desired settings automatically.
