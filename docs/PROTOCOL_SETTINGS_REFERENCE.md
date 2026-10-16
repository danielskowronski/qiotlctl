# Protocol settings and manual test catalogue

Reviewed 16 October 2026. Sources: [HEX MQTT](https://developer.qingping.co/private/communication-protocols/public-mqtt-hex), [Air Monitor Lite JSON](https://developer.qingping.co/private/communication-protocols/air-monitor-lite/mqtt), [Air Monitor JSON](https://developer.qingping.co/private/communication-protocols/air-monitor/mqtt), and [the pinned Python definitions](https://github.com/danielskowronski/qingping-iot-mqtt/blob/d4fa28a2bd182227b2396c33a0e38f1e50d75631/src/qingping_iot_mqtt/protocols/json_spec.py). The older `public-mqtt-json` URL redirects to the introduction; the current JSON specifications are model-specific.

This is a protocol catalogue, **not a claim that every device implements every entry**. Observed capabilities below come from CGP22C HEX and CGDN1 Lite v1 JSON devices. See [firmware notes](FIRMWARE_NOTES.md) for the scope of hardware testing. "Typed" means this tool validates and converts a human-readable setting. "Raw" means it can be sent explicitly, but no named setter is supplied. Both use the same transport and command journal.

Where the specification gives no minimum/maximum, the table says so. A two-byte encoding does not establish a safe or accepted device range. Implementation limits derived from integer width are explicitly distinguished from vendor limits. Do not infer reset/disable behavior from zero unless documented or identified as an observation.

The encoder ranges below describe **default validation**. `--ignore-official-limits` relaxes vendor/default semantic limits for every understood writable field, including experimental enum codes, times and ordered ranges. It never relaxes wire types, widths, signedness or exact scaling. For example, HEX `0x7b` can then use 64801-65535 seconds, JSON `display_off_time` can exceed 300 seconds, and a one-byte enum can use codes 2-255. See [experimental input forms and hard bounds](SETTINGS.md#trying-values-outside-official-limits). Observed compatibility exceptions already work without the flag.

## HEX: settings sent in command `0x32`

Integers are little-endian. Each property is `KEY` (one byte), `LEN` (two bytes), `VALUE` (LEN bytes). `0x39` as a **command** is a settings report; `0x39` as a **property key** is a CO2 high-threshold alarm. They are different namespaces.

| Property key | Meaning and specified encoding | Official values/range | Typed support / observed behavior |
| --- | --- | --- | --- |
| `0x04` | Upload period, 2 bytes, minutes | No device range stated | Typed `reporting.upload_interval`; encoder 1-65535 min. CGP22C same-value write triggers report |
| `0x05` | Record period, 2 bytes, seconds | No device range stated | Typed `reporting.record_interval`; encoder 1-65535 s. CGP22C same-value write silent |
| `0x19` | Temperature display unit, 1 byte | `00` Celsius, `01` Fahrenheit | Typed; both changes physically verified on CGP22C, no report |
| `0x1b` | Repeat-notification period, seconds | Catalogue does not explicitly state length/range; existing decoder uses 2 bytes | Typed `reporting.event_repeat_interval`, 0-65535 s encoding bound; untested |
| `0x37` | Reporting delay, 2 bytes, seconds | No range stated | Typed `reporting.delay`, 0-65535 s encoding bound; untested |
| `0x3b` | CO2 measurement period, 2 bytes, minutes | No range stated | Typed `co2.measurement_interval`, 1-65535 min encoding bound; CGP22C same-value silent |
| `0x3c` | CO2 middle/yellow range, 2 x 2 bytes | Both endpoints inclusive; no device range stated | Typed `co2.indicator`; each 0-65535 ppm encoding bound, ordered endpoints; CGP22C reassertion silent |
| `0x3d` | Battery auto-off period, 2 bytes, minutes | No range or zero meaning stated | Typed `power.battery_off_after`; 0-65535 min encoding bound; zero observed on three tested CGP22C devices |
| `0x3e` | Time format, 1 byte | `00` 24-hour, `01` 12-hour | Typed; physical changes verified on CGP22C, no report |
| `0x40` | CO2 automatic self-calibration | `00` off, `01` on, 1 byte | Typed `co2.automatic_calibration`; reported on all three, CGP22C same-value silent |
| `0x43` | SNTP server text | Example `cn.pool.ntp.org`; no hostname-length range stated | Typed `clock.sntp_server`; tool permits 1-65535 bytes without NUL/newlines; untested |
| `0x44` | SNTP enable switch | `00` off, `01` on; length not explicitly stated in catalogue | Typed `clock.sntp_enabled`, observed 1 byte; CGP22C write silent |
| `0x3f` | CO2 percentage offset, 2 bytes | Step 0.1%; no accepted range/signedness explicitly stated | Typed `calibration.co2.offset_percent`; existing signed interpretation; CGP22C zero silent |
| `0x45` | CO2 absolute offset, 2 bytes | Step 1 ppm; no accepted range stated | Typed `calibration.co2.offset_ppm`; CGP22C zero silent |
| `0x46` | Temperature absolute offset, 2 bytes | Step 0.1°C; no accepted range stated | Typed `calibration.temperature.offset_c`; CGP22C zero silent |
| `0x47` | Temperature percentage offset, 2 bytes | Step 0.1%; no accepted range stated | Typed `calibration.temperature.offset_percent`; CGP22C zero silent |
| `0x48` | Humidity absolute offset, 2 bytes | Step 0.1 percentage points RH; no accepted range stated | Typed `calibration.humidity.offset_percent_rh`; CGP22C zero silent |
| `0x49` | Humidity percentage offset, 2 bytes | Step 0.1%; no accepted range stated | Typed `calibration.humidity.offset_percent`; CGP22C zero silent |
| `0x4b`, `0x4d` | PM2.5 / PM10 absolute offset, 2 bytes | Step 1; no accepted range stated | Typed PM `offset_ug_m3`; not hardware-tested on HEX PM devices |
| `0x4c`, `0x4e` | PM2.5 / PM10 percentage offset, 2 bytes | Step 0.1%; no accepted range stated | Typed PM `offset_percent`; not hardware-tested on HEX PM devices |
| `0x62` | TVOC display switch | `00` hide, `01` display | Typed `display.tvoc_enabled`; CGP22C produced no reply, support unknown |
| `0x63` | LED switch | `00` off, `01` on | Typed `display.led_enabled`; CGP22C produced no reply, support unknown |
| `0x7b` | High-frequency upload period, 2 bytes, seconds; generally USB/Wi-Fi | Maximum **18 hours**; no minimum/zero meaning stated | Typed `reporting.high_frequency_interval`, encoder 0-64800 s; untested |
| `0x88` | Fixed timezone offset, 2 bytes; hours x 10 | Example UTC+8 = 80; no min/max or signed representation explicitly stated | Typed `clock.timezone`, signed interpretation; tool policy -12:00...+14:00 in six-minute steps; UTC+2 write on CGP22C silent |

The current signed 16-bit offset encoder represents -32768...32767 wire counts: -3276.8...3276.7 for 0.1-step fields and -32768...32767 for whole-unit fields. Those are **representation limits**, not vendor-endorsed calibration limits. Negative writes and nonzero calibration changes have not been validated on the tested devices.

### HEX threshold alarms

| High / low keys | Sensor | Threshold conversion | Typed support |
| --- | --- | --- | --- |
| `0x07` / `0x08` | Temperature | `°C x 10 + 500`, 2 bytes | Yes |
| `0x0a` / `0x0b` | Humidity | `%RH x 10`, 2 bytes | Yes |
| `0x0d` / `0x0e` | Pressure | `kPa x 100`, 2 bytes | Yes |
| `0x39` / `0x3a` | CO2 | Whole ppm, 2 bytes; observed in CGP22C history | Yes |
| `0x29` / `0x2a` | External/AUX temperature | Listed as over-limit event; separate threshold conversion not stated | Raw |
| `0x57` / `0x58` | TVOC | Listed as over-limit event; threshold scaling not specified here | Raw |
| `0x59` / `0x5a` | PM2.5 | Listed as over-limit event; no device limits stated | Raw |
| `0x5b` / `0x5c` | PM10 | Same limitation | Raw |
| `0x5d` / `0x5e` | Noise | Same limitation | Raw |
| `0x5f` / `0x60` | Light | Same limitation | Raw |
| `0x68` / `0x69` | Generic sensor high/low reporting | Listed without a complete settings payload layout | Raw; layout needs investigation |

The published alarm structure is 11 bytes: recurrence byte, start minute (`uint32`), end minute (`uint32`), threshold (`uint16`). Devices with sound accept a 13-byte form with a two-byte sound duration in seconds. Prose defines `01` once and `fe` daily; the example contradicts itself. Start/end count minutes from midnight, equal values represent all day, and sound duration zero switches sound off. The typed API uses local `HH:MM` values (00:00-23:59), `once`/`daily`, and 0-65535 seconds for sound. The last limit comes from width; the vendor does not state an audible-duration device limit.

Threshold representability: temperature -50.0...6503.5°C, humidity 0...6553.5%RH, pressure 0...655.35 kPa, CO2 0...65535 ppm. These mathematical limits are **not usable sensor ranges**. Firmware acceptance limits, overnight schedules and clearing/deleting a threshold remain unverified. A previously reported alarm was absent from later reports; missing fields must not be interpreted as current desired state or a known disable operation.

### Remaining HEX features for manual investigation

These have no typed persistent setting in the typed API. Full raw commands remain available; commands other than `0x32` need `encode raw --command ...` and `command ALIAS raw`.

| Key / command | Documented function | Values, width and limits in specification |
| --- | --- | --- |
| `0x15` in `0x32` | Set clock | Timestamp; tool uses four-byte Unix seconds, host UTC. Existing `command ALIAS sync-time`; CGP22C publication observed, correction unproven |
| `0x41` | Start CO2 manual calibration | One byte, `01` performs calibration. An action, not persistent configuration |
| `0x42` | Temporary realtime reporting | Duration in seconds; catalogue does not specify width/range. No typed HEX command yet |
| `0x6c` | CO2 calibration request/status | `0` no calibration needed, `1` request, `2` calibrating; direction/width incomplete |
| `0x2b` | BLE transmit power | No encoding/range specified |
| `0x36` | BLE broadcast name | Maximum 28 bytes |
| `0x2f`, `0x30` | Main/AUX temperature and humidity offsets | Two bytes temperature + two bytes humidity; 0.1 steps; limits not stated |
| `0x31` | Pressure offset | Two bytes, 0.1 step; unit/range not fully stated |
| `0x4f`, `0x50` | Temperature / humidity middle range | Two two-byte endpoints, step 0.1; humidity maximum explicitly inclusive, temperature inclusivity not explicit |
| `0x51`-`0x55` | PM2.5, PM10, TVOC, noise, light middle ranges | Two two-byte endpoints, step 1; maximum described as inclusive; no device limits |
| `0x77`, `0x78` | RS485 baud rate / address | Four bytes / two bytes; supported rates and address limits unspecified |
| `0x1d` | End-of-data / disconnect hint | `00` more data remains, `01` all sent; can let device sleep |
| `0x1e` | Factory reset | Key listed; complete value contract not provided |
| `0x01`, `0x28` | Device ID/encryption | ID may be AES key; encryption `0x28` is 16 bytes to enable, empty to disable |
| `0x20`, `0x25` in command `0x3a` | Wi-Fi / MQTT provisioning | Wi-Fi quoted SSID/password; MQTT space-delimited URL, port, username, password, client ID, subscribe topic, publish topic; MQTT value <250 bytes |
| `0x12`, `0x13` in command `0x33` | Wi-Fi-module / MCU firmware URL | HTTP URLs; not a persistent sensor setting |
| `0x06`, `0x6a` | Undocumented report fields | Three tested CGP22C devices report `e803` and `00`; meaning unknown. Exact single-field reassertions on CGP22C were silent |
| `0x67` | Undocumented telemetry field | Preserve raw; not established as writable configuration |
| `>0xc8` | User-defined functions | Reserved for vendor/user extensions; no universal semantics |

Product ID, serial numbers, firmware strings, USB state, calibration state, battery and RSSI are metadata. Their presence in a report does not make them writable settings. NB-IoT fragmentation `0x26` and byte substitution `0x27` are transport features, outside local MQTT settings.

## JSON Lite: settings inside `{"type":"17","setting":{...}}`

The tool supplies a random integer request ID and a current Unix timestamp. Documented interval writes return type `18` with matching `ack_id` and `code` (`0` means success). Other examples return type `28` settings. Query settings with type `28`; a settings report is uncorrelated even if it has its own `id`.

| Vendor key | Official representation and range | Human setting / evidence |
| --- | --- | --- |
| `report_interval` | Integer seconds, **>10** in response table; no maximum | `reporting.upload_interval`; tool max signed int32, not a device limit; CGDN1 reports 60 |
| `collect_interval` | Integer seconds, **>10** in response table; no maximum | `reporting.record_interval`; CGDN1 reports and accepts **10**; compatibility minimum 10 |
| `is_12_hour_mode` | `0` 24-hour, `1` 12-hour | `display.clock_format` |
| `temperature_unit` | String `C` or `F` | `display.temperature_unit` |
| `timezone` | Integer hours x10; `80` UTC+8, `-80` UTC-8 | `clock.timezone`; CGDN1 reports 20; tool limits are the same policy as HEX |
| `need_ack` | `1` requires history ACK; zero not explicitly defined in this table | `reporting.require_ack`; CGDN1 reports 0; independent of the optional clock response |
| `auto_slideing_time` | Integer seconds, **0-60** | `display.rotate_interval`; CGDN1 and vendor example both show **120**; tool accepts 0-60 and exactly 120 |
| `display_off_time` | Integer seconds, **0-300**, zero never | `display.off_after`; CGDN1 reports 120 |
| `power_off_time` | Integer minutes, **0-60**, zero never | `power.battery_off_after`; CGDN1 reports 30 |
| `night_mode_start_time`, `night_mode_end_time` | Integer minutes since midnight; no further behavior/range stated | `display.night_mode.start/end`; tool 00:00-23:59, equal-time meaning unconfirmed |
| `page_sequence` | CSV sensor names; no enum/length stated | `display.pages`; known names mapped, duplicates preserved; Python notes say first/last repeat to close loop |
| `screensaver_type` | `0` rotating readings, `1` current reading, `2` time + rotating, `3` time + current | `display.screensaver`; CGDN1 reports 1 |
| `temperature_offset` | Integer, **°C x100** | `calibration.temperature.offset_c`, exact 0.01°C |
| `humidity_offset` | Integer, **%RH x10** | `calibration.humidity.offset_percent_rh`, exact 0.1 points |
| `co2_offset`, `pm25_offset`, `pm10_offset` | Integer, whole units | Respective `calibration.*.offset_ppm/offset_ug_m3` |
| `humidity_zoom`, `co2_zoom`, `pm25_zoom`, `pm10_zoom` | Percentage x10 | Respective `calibration.*.offset_percent`, exact 0.1% |
| `temperature_zoom` | Specific response table says percentage, without x10 | `calibration.temperature.offset_percent`, whole percent under this interpretation; nonzero behavior unverified |
| `co2_asc` | Present as `1` in example, no detailed current table | `co2.automatic_calibration`, boolean interpretation from legacy code/HEX counterpart; CGDN1 reports 1 |
| `pm_sampling_interval` | Present as `1` in example; current table omits definition | `pm.measurement_interval`, seconds from legacy definitions; no official range available |
| `co2_sampling_interval` | Legacy public JSON field, absent from current Lite document/report | `co2.measurement_interval`, seconds from legacy definitions; support on CGDN1 unknown |
| `temp_led_th`, `humi_led_th` | CSV integers in example; scaling not stated in current table | Typed threshold arrays using x100 from Python notes; color and boundary semantics unverified |
| `co2_led_th`, `pm25_led_th`, `pm10_led_th` | CSV integers in example; no limits stated | Typed whole-unit threshold arrays from legacy interpretation; no assumed color mapping |
| `pm25_standard` | Example `0`; no enum definition | **Raw only**. CGDN1 reports `1`; meaning needs testing |
| `pm25_calib_mode` | Example `0`; no enum definition | **Raw only**. CGDN1 reports `0`; meaning needs testing |

JSON offset encoders use signed int32 wire limits because that is the implementation's supported integer contract; the specification says `int` and supplies no range. Divide those limits by the listed scale to obtain representable human values. This is not a vendor calibration recommendation. PM sampling uses positive signed-int32 seconds as a tool bound. Threshold arrays use ordered signed-int32 counts (nonnegative except temperature); no firmware element-count limits are established.

### Other JSON protocol functions

| Message / keys | Published behavior | Ranges / limitations / current support |
| --- | --- | --- |
| Type `12`, `up_itvl`, `duration` | Temporary realtime reporting | String seconds; interval **>2**, duration **60-3600**. Existing typed `command CGDN1 realtime`; tested on CGDN1 |
| Type `18`, `timestamp` | Time-bearing history response | Unix seconds. Old Python sends timestamp-only replies to repair clock; existing explicit `sync-time` compatibility command. Not evidence that every packet needs an ACK |
| Type `10` -> `11`, `dev_list` | Lite v1 Bluetooth sub-device startup | `FFFFFFFFFFFF` sentinel if none; type11 timestamp is current time. Responder implemented, opt-in; no arbitrary child-device management API yet |
| Type `9`, `adv_data` | Lite v1 BLE data forwarding | Incoming raw advertisement data, not a settings write; no full sub-device integration |
| Type `32`, `scenes.scenes[].device_scene.rules[]` | Air Monitor threshold scenes | One scene/rule documented. Operator `1 >`, `2 <`, `3 =`, `4 >=`, `5 <=`. Sensor IDs: battery `2`, temperature `4`, humidity `5`, CO2 `7`, TVOC `8`, PM10 `9`, PM2.5 `10`, VOC index `15`, noise `17`. Floating threshold; no ranges stated. `condition`/`duration` appear in example but are not fully defined. **Raw only; not documented for Lite v1** |
| Type `40`, `action: alarmUpdate` | Air Monitor generation-2 alarm clock add/update | `hhmmss` integer, `enabled` and `delayable` 0/1; weekday CSV such as `1,2`; ringtone ID; `reply_all` boolean. No complete ID/name/ringtone bounds. **Raw only; not Lite v1** |
| Type `40`, `alarmDel`, `alarmQuery`, `alarmSync` | Delete/query/synchronize generation-2 alarm clocks | Delete example uses `alarm` object while table lists `alarms` array; update examples disagree about IDs/repeat format. Do not guess typed schema. Raw full-envelope commands only |
| Type `16`, `host` object | Change MQTT server | Host/port/user/password/client ID/sub/pub topics, port a string in examples. No documented acknowledgement. Existing raw full-envelope path only |
| `tvoc_offset`, `tvoc_zoom` | Air Monitor response example | No adequate per-field scale contract; raw only pending model-specific evidence |

Scene thresholds and alarm clocks are different features. The Air Monitor specification's presence does not establish support on CGDN1. Network changes, manual calibration, firmware update and reset remain explicit raw actions rather than configuration continuously reapplied by the daemon.

## Firmware deviations and interpretation limits

| Finding | Official/legacy expectation | Actual evidence and implementation decision |
| --- | --- | --- |
| CGP22C's `0x39` trigger | HEX defines configuration sending/reporting but does not promise an ACK for every field; Python had an empty-query helper | `0x04=0500` alone returned four original reports; 20 other tested keys did not. Display changes nevertheless worked. Keep report generation separate from setter support |
| Empty HEX query | Python helper constructed zero-field `0x32`; wrapper itself was broken and had no callers | Three attempts across two CGP22C devices returned no settings report. Ordinary `settings get` remains unsupported for HEX unless the operator explicitly chooses a reassertion write |
| CGDN1 collection interval | Response table says >10 seconds | Fresh CGDN1 readback says 10, and same-value reassertion/readback succeeds. Allow 10 explicitly |
| CGDN1 page rotation | Table says 0-60 seconds; its own example says 120 | Fresh CGDN1 reports 120. Allow 0-60 and exactly 120; no assumed support for 61-119 |
| CGDN1 settings-write response | Interval-write example shows type18 acknowledgement | A same-value type17 write produced type28 settings instead; compare readback without pretending that report ID is an ACK ID |
| JSON thresholds / PM sampling | Current Lite table omits scale/units despite including values in an example | Human conversions use documented legacy Python interpretations, labelled as such; firmware write/color semantics remain unverified |
| JSON temperature percentage | Other zoom fields explicitly x10; temperature's specific table only says percentage | Use whole percent for temperature, preserve raw escape hatch, call out unverified nonzero behavior |
| Air Monitor offset tables | Command table distinguishes x100, x10 and whole units; generic response row says all offsets x100 | Use specific command definitions, not contradictory generic row; do not apply x100 to CO2/PM |
| HEX alarm repeat example | Prose: once `01`, daily `fe`; example mixes the two and has malformed length annotations | Encode named values from prose; preserve unfamiliar report bytes raw; require manual schedule tests |
| HEX pressure alarm decoder | Specification says pressure x100 | Decode using /100 kPa, covered by an independent vector test |
| Clock/timezone | Timestamp and timezone are distinct | Unix UTC timestamp is not shifted by timezone. UTC+2 write produced no CGP22C report; accurate clock beforehand means correction was not demonstrated |
| CGP22C historical alarm absent | A captured report includes a 1400 ppm daily alarm | Later reports omit it. Missing fields do not establish a reset, disabled alarm or deletion encoding |
| CO2 measurement cadence | Reported HEX measurement setting is 5 or 30 minutes | USB-powered history shows changing CO2 at 30-second record cadence. Configured interval is not a promise of effective sample cadence |
| Unknown keys | `06`, `67`, `6a` absent from public catalogue | Preserve raw; `06=e803` and `6a=00` same-value writes on CGP22C were silent; no invented meanings |

Request frames, response timing and observation limits are in [FIRMWARE_NOTES.md](FIRMWARE_NOTES.md). New writes are not labelled "verified" merely because the broker accepted them.

## Reporting manual results

Use a single-property request and save its command journal. Record the alias/model/firmware, human value and exact raw property, previous value, publication time, whether a response arrived, the settings readback, physical effect, and restoration result. Missing response and unchanged readback after a same-value write do not by themselves prove support or rejection.

For an unimplemented HEX feature, build a frame with `encode settings --protocol hex --raw-hex 0xKEY=BYTES`. For JSON vendor settings, use `encode settings --protocol json --raw-json '{"vendor_key":value}'`. Other message types need `encode raw` and `command ALIAS raw`. See [SETTINGS.md](SETTINGS.md) for syntax and explicit CGP22C readback probes. Include protocol references and reproducible results when proposing additional typed features.
