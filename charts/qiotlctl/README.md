# qiotlctl

One Qingping local MQTT daemon with persistent capture/output state. Image:
`ghcr.io/danielskowronski/qiotlctl`; chart:
`oci://ghcr.io/danielskowronski/charts/qiotlctl`.

Read [values.yaml](values.yaml) and [examples/](examples/) for configuration.
Full build, migration, persistence and recovery guidance lives in
[docs/DEPLOYMENT.md](https://github.com/danielskowronski/qiotlctl/blob/main/docs/DEPLOYMENT.md).

Supply devices, MQTT credentials and NetworkPolicy egress rules. Defaults disable
automatic replies, VictoriaMetrics and Home Assistant publishing. Desired device
settings never apply automatically. Use one active responder/HA publisher per set
of devices; the chart does not provide distributed coordination.

Broker authorization is configured separately. The [EMQX ACL example](examples/emqx-acl.conf)
contains permissions for both qiotld and Home Assistant; replace the MQTT usernames
and add it to the broker's existing rules before matching denies. The chart does
not install these ACLs. The daemon needs device-topic access, discovery/state
publication, discovery readback subscriptions and a subscription to HA's birth topic. HA needs discovery/state
subscriptions and permission to publish its birth/last-will status.

Use [home-assistant.yaml](examples/home-assistant.yaml) after your device/broker
values to enable only HA, or [outputs.yaml](examples/outputs.yaml) for VM and HA
with VMServiceScrape. These examples explicitly show the discovery/state prefixes
and birth topic that the ACLs cover. For example, render without connecting:

```sh
helm template qiotld ./charts/qiotlctl -n app-qiotld \
  -f charts/qiotlctl/examples/minimal.yaml \
  -f charts/qiotlctl/examples/home-assistant.yaml --set fullnameOverride=qiotld
```

Discovery is retained at `homeassistant/sensor/qingping_<ha-id>/<sensor>/config`;
state is nonretained at `qiotlctl/<device-id>/<sensor>/state`. If nothing appears,
follow [the discovery troubleshooting guide](https://github.com/danielskowronski/qiotlctl/blob/main/docs/DEPLOYMENT.md#troubleshooting-missing-home-assistant-discovery).

The chart requires Kubernetes >=1.28 and uses standard Helm v2 chart APIs. Helm 4
is used by the local validation workflow. Optional VMUser/VMServiceScrape or
ServiceMonitor resources require their operators/CRDs to be installed separately.
No backend or CRD dependencies are bundled.

Readiness includes enabled VM/HA outputs by default. `health` in
`config.data` controls grace, check timing and required outputs. `/livez` remains
independent; `/healthz` explains readiness. In the pod, use
`qiotlctl doctor --daemon-url http://127.0.0.1:9090`. Grant qiotld discovery
subscribe permission before upgrading. An exporter outage may time out
`helm --wait` while capture and durable queueing continue.
