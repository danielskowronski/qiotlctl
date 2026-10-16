# Changelog

## 0.2.0

- Operator CLI (`qiotlctl`) and daemon (`qiotld`) for local Qingping MQTT devices.
- HEX and JSON decoding, offline payload encoding, normalized readings and raw
  access to undocumented properties.
- CGP22C CO2 monitor and CGDN1 Air Monitor Lite v1 profiles, explicit commands,
  human-readable settings and readback with documented confirmation limits.
- Layered YAML configuration, reusable settings profiles, per-device overrides
  and environment variables through cleanenv.
- SQLite capture/replay, durable VictoriaMetrics export and resumable historical
  import, plus Home Assistant MQTT discovery/state publishing.
- `doctor` diagnostics for MQTT permissions, retained discovery, storage and VM
  delivery; exporter-aware readiness and independent liveness checks.
- Linux/macOS binaries, multi-platform OCI images and an OCI Helm chart with
  persistent storage, Secret integration, health probes and monitoring options.

Automatic device responses and exporters are disabled by default. Enabled HA and
VM exporters affect readiness; dependency outages do not trigger liveness restarts.
HA discovery verification requires subscribe access to discovery topics as well
as publish access. See [deployment](docs/DEPLOYMENT.md) and
[diagnostics](docs/DIAGNOSTICS.md).

For existing installations, review [configuration paths and environment names](docs/CONFIGURATION.md#existing-installations)
and [Helm upgrade guidance](docs/DEPLOYMENT.md#upgrading-an-existing-installation).
