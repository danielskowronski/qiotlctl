# Qingping IoT Local Control - `qiotlctl`

Go tools for Qingping devices in local MQTT mode, evolution of 
[Python qingping-iot-mqtt](https://github.com/danielskowronski/qingping-iot-mqtt).

## Disclaimer

This project was envisioned, designed and managed by human maintainer with 
generative AI assisted source code  implementation. Entire architecture, all 
requirements, implementation decisions and validation were performed under 
human direction.

If you believe this is automatically "AI slop" or that it's morally wrong, 
you are free to ignore this project.

## What it does

`qiotlctl` is an operator CLI and `qiotld` is a daemon for Qingping devices in
local MQTT mode. They decode HEX and JSON payloads into common readings, while
keeping model-specific settings and raw protocol fields accessible. The initial
models are the **CGP22C CO2 & Temp & RH Monitor** and **CGDN1 Air Monitor Lite v1**;
unknown devices can use a protocol and topic pair with more limited interpretation.

Use the CLI to inspect traffic, encode/decode payloads offline, send commands by
device alias, and apply human-readable settings. The daemon records to SQLite,
buffers VictoriaMetrics exports across outages, and publishes Home Assistant MQTT
discovery and state. Configuration supports multiple YAML files, reusable profiles,
per-device overrides and environment variables through cleanenv. Docker and the
OCI Helm chart support Kubernetes deployment with health checks and metrics.

Automatic device acknowledgements and time replies are **off by default**.
Settings in configuration are applied only by an explicit command. `doctor`
explains MQTT permissions, missing discovery, storage and VM delivery problems.
Firmware support and confirmation limits are documented separately from what the
protocol can encode.

## Start here

Build from source with Go 1.26 or newer (0.2.0 release builds use Go 1.27.2):

```sh
make build VERSION=0.2.0
./bin/qiotlctl --help
MQTT_USERNAME=example MQTT_PASSWORD=example \
  ./bin/qiotlctl --config examples/config.yaml config validate
```

The example validation uses dummy credentials and does not connect.
Copy and adapt the [example configuration](examples/config.yaml) and its included
files before connecting to your broker. The CLI defaults to
`~/.config/qiotlctl/config.yaml`; the daemon defaults to `/etc/qiotld/config.yaml`.
Repeat `--config` for overlays, or use `QIOTLCTL_CONFIG` / `QIOTLD_CONFIG`.

## Documentation

| Task | Guide |
| --- | --- |
| Configure devices, profiles, multiple files and environment overrides | [Configuration](docs/CONFIGURATION.md) |
| Inspect devices and send explicit commands | [Commands and confirmation](docs/COMMANDS.md) |
| Set upload periods, display options and alarms | [Human-readable settings](docs/SETTINGS.md) |
| Understand protocol codes, ranges and observed firmware deviations | [Settings reference](docs/PROTOCOL_SETTINGS_REFERENCE.md) and [protocol coverage](docs/PROTOCOLS.md) |
| Export readings and recover after outages | [VictoriaMetrics and Home Assistant](docs/OUTPUTS.md) |
| Diagnose MQTT permissions, HA discovery and VM delivery | [Doctor and health checks](docs/DIAGNOSTICS.md) |
| Build containers, deploy Helm and configure EMQX/HA ACLs | [Deployment](docs/DEPLOYMENT.md) |
| Run tests and understand coverage limits | [Testing and validation](docs/VALIDATION.md) |
| Build and publish a release | [Release guide](docs/RELEASING.md) and [changelog](CHANGELOG.md) |
| Understand the code and extend device support | [Architecture](docs/ARCHITECTURE.md) |

The project is licensed under [BSD-3-Clause](LICENSE). Binary distributions include
[third-party notices](third_party/README.md).
