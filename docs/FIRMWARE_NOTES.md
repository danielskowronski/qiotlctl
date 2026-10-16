# Firmware observations

These observations describe tested devices and firmware, not universal Qingping
behavior. Keep documented encoding, observed reporting and verified physical
effects separate. Missing fields remain unknown; historical settings must not be
restored automatically as if they were current desired configuration.

## CGP22C, MCU firmware 2.0.6

The observed devices reported wireless firmware 1.9.5. The controlled write
experiments below used one CGP22C, nonretained single-property requests after fresh
uplinks, and disabled automatic application replies. Device reports do not carry
request IDs, so timing and matching values provide uncorrelated evidence.

### Settings-report trigger

An empty configuration frame (`4347320000bc00`, command `0x32`, no TLVs) produced
no settings report in repeated tests on two devices, including a response window
longer than two minutes. It is not exposed as a supported read-only query.

Writing upload interval `0x04=0500` (five minutes, little-endian) produced a fresh
`0x39` settings report in four isolated control tests, approximately 2.09-2.35
seconds after publication. The complete request is `43473205000402000500cc00`.
The existing value was reasserted: neither multiple fields nor a changed value
was necessary. Three interleaved controls returned byte-identical reports while
other settings writes remained silent.

A later typed application of the same upload interval also returned matching
readback. A clock-format write followed by an explicit upload-interval probe
returned matching clock state. The latter verifies observed state after the pair
of writes; it is not an isolated test of which request caused the report.

This exchange underlies `--reassert-upload-interval` and
`--readback-upload-interval`. Both are explicit writes, not a hidden read-only
query. Use a known current or deliberately desired interval.

### Silent writes

| Property | Tested bytes or value | Observed outcome |
| --- | --- | --- |
| `0x19` temperature unit | `00` Celsius, `01` Fahrenheit | Physical C -> F -> C changes verified; no settings report |
| `0x3e` clock format | `00` 24h, `01` 12h | Physical 24h -> 12h -> 24h changes verified; no settings report |
| `0x05` recording interval | `1e00`, 30 seconds | Same-value write, no report |
| `0x3b` CO2 measurement interval | `1e00`, 30 minutes | Same-value write, no report |
| `0x3c` yellow indicator bounds | `e803dc05`, 1000-1500 ppm | Same-value write, no report |
| `0x3d` battery auto-off | `0000` | Same-value write, no report |
| `0x40` automatic CO2 calibration | `00` | Same-value write, no report |
| `0x3f`, `0x45`-`0x49` calibration offsets | `0000` | Same-value writes, no reports |
| `0x88` timezone | `1400`, UTC+2 | No report; application unverified |
| `0x44` SNTP enable | `01` | No report; application unverified |
| `0x15` clock | Current four-byte UTC Unix timestamp | No report; clock correction unverified |
| `0x63` LED enable | `01` | No report; model support unverified |
| `0x62` TVOC display enable | `01` | No report; model support unverified |
| Unknown `0x06`, `0x6a` | `e803`, `00` | Same-value writes, no reports; meanings unknown |

The individual probes used response windows of at least 90 seconds. Ordinary
network/history reports and interleaved upload controls continued. A silent
same-value write can have been applied or ignored; absence of a report does not
prove either. The physical display changes independently establish that some
successful setters are silent.

### Historical fields and timing

Captured product key `0x38=0x0033` identified CGP22C reports. Settings snapshots
included zero battery auto-off, zero offsets and CO2 intervals of five or thirty
minutes. USB-powered history nevertheless contained changing CO2 readings at a
30-second recording cadence. A configured interval is not a guarantee of the
active measurement cadence under every power mode.

A historical alarm configuration included a high CO2 threshold of 1400 ppm,
repeat `0xfe`, schedule 00:00-23:00 and a 15-second sound duration. Captured alarm
events corroborated threshold reporting, not audible buzzer behavior. Later
settings reports omitted the alarm field. The evidence does not distinguish a
previous configuration change, incomplete reporting or a partial-write side
effect, and does not establish an alarm deletion encoding.

## CGDN1 Air Monitor Lite v1

A fresh type-28 settings query reported a 10-second collection interval and
120-second display rotation. Reasserting the 10-second collection setting through
a type-17 write returned type-28 settings rather than a matching type-18
acknowledgement; a separate query returned the same value. Ordinary application
of an already matching value skipped the write.

The tool accepts 10 seconds for collection despite the response table's `>10`
wording. Display rotation accepts 0-60 seconds and the observed 120-second value;
values 61-119 are not inferred from that exception. The specification's own
rotation example also uses 120.

JSON type-18 timestamp replies in the Python implementation are a clock repair
workaround. They do not show that every inbound message needs an ACK. Startup,
history acknowledgement and optional heartbeat clock policies are separate and
remain opt-in.

Firmware-specific support for other settings, nonzero calibration values, JSON
threshold/color semantics and additional models needs independent verification.
See the [raw-code and range catalogue](PROTOCOL_SETTINGS_REFERENCE.md).
