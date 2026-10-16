#!/usr/bin/env python3
"""Extract a small, sanitized corpus; optionally compare the original Python codecs.

The source SQLite database is always opened read-only. No broker credentials are read.
Usage: python3 tools/extract_fixtures.py HISTORY OUTPUT --reference PYTHON_REPO
"""
import argparse
import collections
import hashlib
import json
import logging
import os
import sqlite3
import sys


def sanitize(payload):
    if payload.startswith(b"CG"):
        # Selected captures are telemetry/config reports; discard credential TLVs.
        out = bytearray()
        pos = 5
        while pos < len(payload) - 2:
            key = payload[pos]
            size = int.from_bytes(payload[pos + 1:pos + 3], "little")
            value = payload[pos + 3:pos + 3 + size]
            if key not in (0x01, 0x02, 0x20, 0x25, 0x28):
                out.extend(bytes([key]) + len(value).to_bytes(2, "little") + value)
            pos += 3 + size
        frame = b"CG" + payload[2:3] + len(out).to_bytes(2, "little") + out
        return bytes(frame + (sum(frame) & 65535).to_bytes(2, "little"))
    data = json.loads(payload)
    for key in list(data):
        if key in ("mac", "wifi_mac"):
            data[key] = "001122334455"
        elif key == "wifi_info":
            parts = data[key].split(",")
            data[key] = "fixture-network," + ",".join(parts[-3:-1]) + ",00:11:22:33:44:55"
        elif any(word in key.lower() for word in ("salt", "pass", "secret", "mqtt_cfg", "wifi_cfg", "host")):
            data[key] = "[redacted]"
    return json.dumps(data, separators=(",", ":")).encode()


def signature(payload, direction):
    if payload.startswith(b"CG"):
        keys = []
        pos = 5
        while pos < len(payload) - 2:
            key = payload[pos]
            size = int.from_bytes(payload[pos + 1:pos + 3], "little")
            keys.append(key)
            pos += 3 + size
        return ("hex", payload[2], direction, tuple(keys))
    data = json.loads(payload)
    return ("json", data.get("type"), direction, tuple(sorted(data)))


def baseline(payload, protocol, direction, model):
    from qingping_iot_mqtt.protocols.base import ProtocolMessageDirection, ProtocolMessageCategory
    from qingping_iot_mqtt.protocols.common_spec import DeviceModel
    from qingping_iot_mqtt.protocols.hex import HexProtocol, HexSensorReadingMessage
    from qingping_iot_mqtt.protocols.json import JsonProtocol, JsonSensorReadingMessage
    if direction != "from-device":
        return []
    decoder, readings = (HexProtocol(), HexSensorReadingMessage) if protocol == "hex" else (JsonProtocol(), JsonSensorReadingMessage)
    message = decoder.decode_message(payload, ProtocolMessageDirection.DEVICE_TO_SERVER)
    if message.category != ProtocolMessageCategory.READINGS:
        return []
    allowed = DeviceModel(model).device_model_info.sensors
    return [{"timestamp": context.timestamp, "readings": [
        {"sensor": reading.sensor.value, "value": reading.value, "unit": reading.unit}
        for reading in context.readings if reading.sensor in allowed
    ]} for context in readings(message).get_reading_contexts()]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("history")
    parser.add_argument("output")
    parser.add_argument("--reference")
    args = parser.parse_args()
    if args.reference:
        sys.path.insert(0, os.path.join(args.reference, "src"))
    logging.disable(logging.CRITICAL)
    db = sqlite3.connect("file:" + os.path.abspath(args.history) + "?mode=ro", uri=True)
    selected = {}
    counts = collections.Counter()
    clock_reset_selected = False
    for row, timestamp, topic, payload in db.execute("SELECT id,timestamp,topic,payload FROM raw_payloads ORDER BY id"):
        payload = bytes(payload)
        direction = "from-device" if topic.endswith("/up") else "to-device"
        sig = signature(payload, direction)
        counts[(sig[0], str(sig[1]), direction)] += 1
        extra = False
        if sig[0] == "json" and not clock_reset_selected:
            timestamp_value = json.loads(payload).get("timestamp")
            if isinstance(timestamp_value, int) and timestamp_value < 1577836800:
                extra = clock_reset_selected = True
        key = sig if not extra else (*sig, "clock-reset")
        if key in selected:
            continue
        clean = sanitize(payload)
        model = "CGP22C" if sig[0] == "hex" else "CGDN1"
        case = {"name": f"capture-{row}", "source_row_id": row, "received_at": timestamp,
                "protocol": sig[0], "model": model, "direction": direction,
                "payload_hex": clean.hex()}
        if args.reference:
            try:
                case["expected_samples"] = baseline(clean, sig[0], direction, model)
            except Exception as exc:
                case["reference_unsupported"] = type(exc).__name__
        selected[key] = case
    result = {"schema_version": 1, "reference_commit": "d4fa28a2bd182227b2396c33a0e38f1e50d75631",
              "sanitization": "Network identities and salt redacted; credential TLVs excluded; measurements preserved.",
              "cases": list(selected.values())}
    with open(args.output, "w") as f:
        json.dump(result, f, indent=2, ensure_ascii=False)
        f.write("\n")
    print(f"Extracted {len(selected)} sanitized cases from {sum(counts.values())} raw messages")
    print("Reference unsupported:", [x["name"] for x in selected.values() if "reference_unsupported" in x])


if __name__ == "__main__":
    main()
