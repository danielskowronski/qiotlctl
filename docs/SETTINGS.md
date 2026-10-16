# Setting up and controlling devices

Human-readable desired settings work in YAML, `apply-settings`, offline encoding and fresh readback comparison, with raw vendor properties as an extension. Reusable named profiles share settings with per-device overrides. Loading configuration or starting `qiotld` does **not** apply these settings. Automatic replies remain disabled by default.

Use [the complete protocol reference](PROTOCOL_SETTINGS_REFERENCE.md) to choose additional features to test. That reference distinguishes published protocol definitions, encoding support, and observed behavior on CGP22C and CGDN1 devices. A valid encoding does not guarantee that every model implements it.

## Everyday workflow

Put desired values under `devices.<alias>.settings`, either in your main configuration or in a later overlay file. Only values present in the merged configuration are managed.

To share these values, put the same settings mapping under `profiles.standard_co2.settings` and select `profile: standard_co2` on each device. Device settings override inherited values, and `null` removes inherited management. Profiles can extend one parent; see [profile syntax, precedence and migration](CONFIGURATION.md#reusable-settings-profiles). `config explain bedroom` shows the effective values and their source files. `--from-config` uses the fully resolved result.

```yaml
---
devices:
  bedroom:
    settings:
      reporting:
        upload_interval: 5m
        record_interval: 30s
      display:
        temperature_unit: celsius
        clock_format: 24h
      co2:
        measurement_interval: 30m
        automatic_calibration: false
        indicator:
          yellow_min_ppm: 1000
          yellow_max_ppm: 1500
      power:
        battery_off_after: 0s
      clock:
        sntp_enabled: true
      calibration:
        temperature: {offset_c: 0, offset_percent: 0}
        humidity: {offset_percent_rh: 0, offset_percent: 0}
        co2: {offset_ppm: 0, offset_percent: 0}
```

The two CO2 yellow endpoints are inclusive. `0s` in `battery_off_after` represents the zero value; JSON explicitly documents this as never switching off. HEX's vendor table does not explain zero, although this is the observed value on continuously operating CGP22C monitors.

Review and apply:

```sh
qiotlctl config validate
qiotlctl apply-settings bedroom --from-config --dry-run
qiotlctl apply-settings bedroom --from-config --wait-for-uplink --timeout 7m
```

For an online comparison without applying the desired patch, use `apply-settings living-room --from-config --check`. It queries fresh JSON settings and reports `in_sync` (exit 0) or `drift_or_unknown` (exit 7). HEX checking requires an explicit `--readback-upload-interval 5m` write because no read-only query is verified. Check mode cannot be combined with dry-run, force or single-property mode.

The dry-run preview is offline: it validates values and shows the proposed properties and readback policy, without contacting the broker. Actual JSON application first requests fresh settings and skips matching typed properties. HEX has no verified read-only settings query, so it does not claim to skip values based on a fresh preflight. `--force` disables the JSON skip check. Explicit raw properties always send, even if present in the baseline, to support experiments.

Override just the selected values:

```sh
qiotlctl apply-settings bedroom --set display.clock_format=24h
qiotlctl apply-settings living-room --from-config --set display.off_after=2m
qiotlctl apply-settings bedroom --settings-file desired-settings.yaml --single-property
```

`--settings-file` contains the settings mapping itself, without a `devices:` or `settings:` wrapper. It overlays the selected config settings; repeated `--set path=value` flags override both. Without `--from-config`, only the file/flags are applied. `--set` values use YAML types: `false`, `0`, `5m`, `[co2, temperature]`, or an inline mapping. Unknown paths and typed/raw collisions are errors.

`--single-property` sends each wire property as a separate operation. A range or an alarm schedule occupies one property and stays together. Each operation uses a distinct connection/request ID and is journaled separately when storage is enabled; the whole invocation is serialized against other operations in this process. There is no cross-workstation lock and no automatic write retry.

## Trying values outside official limits

Add `--ignore-official-limits` to accept device settings outside documented ranges and the tool's default semantic checks. It applies to `--set`, `--settings-file`, legacy shorthand flags and settings from the merged configuration. The same option works with offline encoding, `apply-settings` (including preview/check), `settings get` upload probes, and realtime commands. It is a per-invocation option, also available on `config validate`, `config show` and `qiotld` when loading experimental desired settings. It is not saved in YAML and has no environment override.

```sh
# Offline: JSON normally limits display-off time to 300 seconds.
qiotlctl encode settings --protocol json --set display.off_after=10m \
  --ignore-official-limits --output-format json

# Preview a HEX period above the documented 18-hour maximum.
qiotlctl apply-settings bedroom --set reporting.high_frequency_interval=18h10m \
  --ignore-official-limits --dry-run

# Use the same opt-in when experimental values come from config.
qiotlctl config validate --ignore-official-limits
qiotlctl apply-settings living-room --from-config --ignore-official-limits --dry-run
```

Known firmware exceptions remain accepted **without** the flag: CGDN1's 10-second collection interval and 120-second page rotation. `--force` is independent: it reasserts unchanged settings, without relaxing validation.

Types, byte widths, signedness, scaling and exact units remain enforced. For example, HEX `65536s` overflows a two-byte seconds field, `90s` cannot become a whole-minute upload period, and a 0.01°C HEX calibration offset cannot fit a 0.1°C step. These still fail. The table below describes the experimental input forms and the bounds that remain:

| Value kind | With `--ignore-official-limits` |
| --- | --- |
| Durations | Keep `5m`/`30s` syntax and whole wire units. HEX uses unsigned 16-bit counts (0-65535). JSON uses signed 32-bit counts (-2147483648...2147483647). Duration text must also fit Go's `time.Duration` type. |
| Calibration offsets | Keep human units and exact scale. HEX remains signed 16-bit; JSON remains signed 32-bit after scaling. |
| Switches and numeric enums | Keep `true`/`false` or known names, or supply an **unquoted numeric wire code**, e.g. `--set display.clock_format=2`. HEX codes are one unsigned byte (0-255); JSON codes are signed 32-bit integers. Unknown HEX names cannot be encoded. |
| JSON temperature unit | Known `celsius`/`fahrenheit` still map to `C`/`F`; other strings pass through literally, e.g. `--set display.temperature_unit=K`. Numbers remain invalid for this string field. |
| Clock times | `HH:MM` may exceed 23 hours, e.g. `24:00`; minutes still use two digits 00-59. An unquoted integer supplies wire minutes directly. JSON night-mode times use signed 32-bit minutes; HEX alarm times use unsigned 32-bit minutes (0-4294967295). |
| Timezone | Signed `+/-HH:MM` may exceed -12...+14 hours, but must still be an exact six-minute step. Alternatively, an unquoted integer supplies wire tenths of an hour, e.g. `200` = UTC+20. HEX uses signed 16-bit; JSON uses signed 32-bit. |
| Threshold lists / CO2 yellow endpoints | Order and nonempty-list checks are relaxed. Each scaled JSON threshold must fit signed 32-bit; each HEX endpoint must fit unsigned 16-bit. |
| Page lists | Unknown string page names and an empty list are permitted. Known `temperature` still maps to `temp`. The wire representation remains a comma-separated string. |
| HEX alarms | Sensor, condition and unit still select the understood encoding. Threshold and sound-duration widths are unchanged. `repeat` also accepts a numeric byte code; start/end use the experimental clock forms above. |
| SNTP server | Empty text and NUL/newlines are permitted; the value must still be a string fitting the 65535-byte TLV length. The complete HEX envelope must also fit its length field. |

Unknown named settings, unsupported protocol mappings, typed/raw collisions, malformed payloads and transport/configuration constraints still fail. Raw extensions already accept literal wire values and do not require this flag. This option controls what can be encoded; a device may still ignore or reject a value. Per-field readback retains its usual meaning.

## Understanding the result

Results report evidence for each property:

| Result | Meaning |
| --- | --- |
| `unchanged` | A fresh JSON query already matched; no settings write for that typed property |
| `matched` | A subsequent settings report contains exactly the requested encoded value |
| `published_unverified` | MQTT publication completed, but there is no matching readback |
| `acknowledged_unverified` | JSON returned matching `ack_id` with success; the property was absent from readback |
| `different` | A report contains a different value; this does not prove which controller caused it |
| `ambiguous` | A report repeats that wire key; no unique comparison is possible |
| `rejected` | A matching JSON acknowledgement returned a nonzero status |
| `unknown` / `not_sent` | The operation failed after an attempted publication / before attempting publication |

Overall `verified` means every requested property matched or was unchanged; `partial` preserves weaker evidence. `--require-confirmation` makes incomplete readback exit with code 7. An acknowledgement is not readback. HEX reports and JSON type-28 reports have no request correlation ID, so matching values establish observed state, not exclusive causality. Retained, duplicate, wrong-identity and pre-publication messages cannot confirm an operation.

A JSON application normally queries after each write (`--readback=false` suppresses that query). If the write already returns a settings report, that report can also provide matching evidence. `--timeout` applies to each connect/send/wait operation, not to the entire multi-operation invocation. Cancellation stops further operations; an uncertain write is never retried or rolled back.

## CGP22C settings reports

On tested CGP22C firmware, upload interval `0x04` was the only single property that consistently triggered a `0x39` settings report. Other properties were silent, including physically verified clock-format and temperature-unit changes. An empty `0x32` did not produce a report. See [firmware notes](FIRMWARE_NOTES.md) for the tested firmware, response timing and limits of these observations.

Request readback explicitly through a write:

```sh
# This really writes 5m. Use a value known to be current or desired.
qiotlctl settings get bedroom --reassert-upload-interval 5m --wait-for-uplink --timeout 7m

# Apply a silent setting, then explicitly write upload interval to solicit a report.
qiotlctl apply-settings bedroom --set display.clock_format=24h \
  --readback-upload-interval 5m --wait-for-uplink --timeout 7m \
  --require-confirmation
```

The extra write is never hidden inside an ordinary `settings get`. If the settings patch also changes upload interval, a conflicting probe value is rejected before connecting. A patch containing `0x04` waits for its own report; otherwise an explicitly requested probe follows the write. In single-property mode this may require several device wake cycles. Missing fields remain unknown: a previously reported CGP22C CO2 alarm was absent from later reports, which does not establish an alarm-disable encoding.

## Export and offline encoding

```sh
qiotlctl settings get living-room --format yaml
qiotlctl settings get bedroom --reassert-upload-interval 5m --wait-for-uplink --timeout 7m --format yaml
qiotlctl encode settings bedroom --from-config --output-format hex
qiotlctl encode settings --protocol hex --set display.clock_format=24h
qiotlctl encode settings --protocol json --set reporting.record_interval=10s --output-format json
qiotlctl settings catalog
```

YAML export is a settings section, not a complete device definition. Inspect unknown properties before reusing it. JSON numbers remain numbers and strings remain strings. Decoded events also provide `settings_config` alongside the original `settings` keys, preserving consumers of the earlier event schema. No broker connection or configuration file is needed for offline encoding when protocol and values are supplied; generated JSON envelopes include a request ID and current timestamp.

## Human-readable settings

These paths work identically in YAML and `--set`. Omitted fields are unmanaged, `false` and zero are explicit values, and no implicit defaults are sent. Durations are exact: `90s` cannot be encoded as a HEX upload interval because that field uses whole minutes.

| Path | Human value | Protocol / notes |
| --- | --- | --- |
| `reporting.upload_interval` | `5m` | HEX whole minutes; JSON whole seconds >10 |
| `reporting.record_interval` | `30s` | HEX seconds; JSON >=10s including the observed Lite exception |
| `reporting.event_repeat_interval` | `30s` | HEX; repeated event notification, not alarm schedule recurrence |
| `reporting.delay` | `0s` | HEX |
| `reporting.high_frequency_interval` | `30s` | HEX, at most 18h; zero meaning unconfirmed |
| `reporting.require_ack` | `true` / `false` | JSON device preference; does not enable this program's replies |
| `display.temperature_unit` | `celsius` / `fahrenheit` | Both |
| `display.clock_format` | `24h` / `12h` | Both |
| `display.led_enabled`, `display.tvoc_enabled` | `true` / `false` | HEX; CGP22C support unverified |
| `display.off_after` | `2m`, `0s` for never | JSON, 0-300 seconds |
| `display.rotate_interval` | `30s`, `120s` | JSON, 0-60s plus observed 120s exception |
| `display.screensaver` | `rotating_readings`, `current_reading`, `time_and_rotating_readings`, `time_and_current_reading` | JSON enums 0-3 |
| `display.pages` | `[pm25, pm10, co2, temperature, pm25]` | JSON; ordered list, repeats preserved; temperature maps to `temp` |
| `display.night_mode.start`, `.end` | quoted `"22:00"`, `"07:00"` | JSON local times; no guessed meaning for equal endpoints |
| `clock.timezone` | quoted `"+02:00"` | Both, fixed offset; no DST automation; representable in six-minute steps |
| `clock.sntp_enabled`, `clock.sntp_server` | boolean, hostname | HEX |
| `power.battery_off_after` | `30m`, `0s` | HEX minutes; JSON 0-60 minutes |
| `co2.measurement_interval` | `30m` | HEX minutes; legacy JSON `co2_sampling_interval` seconds, unverified on CGDN1 |
| `co2.automatic_calibration` | boolean | Both; configuration switch, not an immediate calibration command |
| `co2.indicator` | `{yellow_min_ppm: 1000, yellow_max_ppm: 1500}` | HEX, two inclusive endpoints |
| `pm.measurement_interval` | `1s` | JSON; units from legacy Python definitions, not the current vendor table |
| `calibration.temperature.offset_c` | `-1.2` | HEX 0.1°C; JSON 0.01°C |
| `calibration.humidity.offset_percent_rh` | `-1.1` | Both, 0.1 percentage points RH |
| `calibration.co2.offset_ppm` | `-10` | Both, whole ppm |
| `calibration.pm25.offset_ug_m3`, `calibration.pm10.offset_ug_m3` | `-2` | Both, whole µg/m³ |
| `calibration.<sensor>.offset_percent` | `2.5` | HEX and JSON CO2/humidity/PM: 0.1%; JSON temperature: whole percent per its specific table |
| `indicators.temperature_thresholds_c` | `[23, 26.5, 28]` | JSON; 0.01°C, inferred from Python notes |
| `indicators.humidity_thresholds_percent_rh` | `[20, 40, 60]` | JSON; 0.01% RH, inferred from Python notes |
| `indicators.co2_thresholds_ppm` | `[1000, 2000, 3000]` | JSON, whole ppm |
| `indicators.pm25_thresholds_ug_m3` | `[35, 75, 115, 150, 250]` | JSON, whole µg/m³ |
| `indicators.pm10_thresholds_ug_m3` | `[50, 150, 250, 350, 450]` | JSON, whole µg/m³ |

Threshold lists must be strictly increasing. Their color assignments and endpoint inclusivity are **not** inferred from the HEX yellow range. JSON temperature/humidity threshold scales and PM sampling units carry legacy-reference evidence, not a new live verification claim. Current UTC clock synchronization stays an explicit `command bedroom sync-time`, rather than a stale timestamp stored as desired configuration.

## Scheduled threshold alarms (HEX)

An illustrative alarm configuration; these values are not device defaults:

```yaml
---
threshold_alarms:
  - sensor: co2
    condition: above
    threshold: 1400
    unit: ppm
    repeat: daily
    start: "00:00"
    end: "00:00"
    sound_duration: 15s
```

Supported sensors/units are `co2`/`ppm`, `temperature`/`c`, `humidity`/`percent_rh`, and `pressure`/`kpa`. Conditions are strictly `above` or `below`. Recurrence is `once` (`0x01`) or `daily` (`0xfe`), following the specification's prose despite its contradictory example. Equal start/end means all day according to the specification; overnight behavior still needs testing. Times are device-local and do not acquire a timezone from your workstation.

Omitting `sound_duration` encodes the 11-byte form. Providing it encodes the 13-byte buzzer form; `0s` disables the sound, **not the threshold event**. No documented alarm deletion/disable request has been established, so there is no invented `enabled: false` encoding. Removing an alarm from configuration only stops managing it. One property exists per sensor/direction, so duplicate definitions are rejected.

## Raw values and layering

```yaml
---
raw:
  hex:
    - key: "0x06"
      value_hex: "e803"
    - key: "0x6a"
      value_hex: "00"
```

For JSON devices:

```yaml
---
raw:
  json:
    pm25_standard: 1
    pm25_calib_mode: 0
    vendor_extension: {enabled: true, levels: [1, 2], label: "00"}
```

```sh
qiotlctl apply-settings bedroom --raw-hex 0x06=e803
qiotlctl apply-settings living-room --raw-json '{"pm25_calib_mode":0}'
qiotlctl encode settings --protocol hex --raw-hex 0x6a=00
```

Values are literal bytes or JSON, not guessed conversions. HEX keys/bytes must be quoted in YAML. Typed/raw collisions and duplicate keys are rejected by the settings planner; the lower-level `encode raw --tlv ...` still supports intentionally repeated TLVs. Non-settings commands use the existing full-envelope `command ALIAS raw` interface.

YAML overlays recursively merge mappings and replace lists. A `null` removes an inherited setting; it does **not** send a device reset. In a raw YAML mapping, null also means removal. To transmit an actual JSON null, use `--raw-json '{"key":null}'`. Cleanenv continues to handle application environment overrides; device settings belong in YAML overlays or explicit CLI flags, rather than a new arbitrary environment-path convention. Raw values are omitted from previews and `config show`; explicit offline encoding shows the actual payload.

[The examples](../examples/config.yaml) share display settings between `standard_co2` and `standard_lite`, then add per-device overrides. Model and response-policy settings stay on each device. See [output configuration](OUTPUTS.md) to export readings to VictoriaMetrics and Home Assistant.
