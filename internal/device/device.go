// Package device interprets wire messages using model capabilities.
package device

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/danielskowronski/qiotlctl/internal/domain"
	"github.com/danielskowronski/qiotlctl/internal/protocol"
	"github.com/danielskowronski/qiotlctl/internal/settings"
)

type Profile struct {
	Model              string   `json:"model"`
	Name               string   `json:"name"`
	Protocol           string   `json:"protocol"`
	Sensors            []string `json:"sensors"`
	OfflineSettings    []string `json:"offline_settings"`
	LiveWrites         bool     `json:"live_writes"`
	OnlineCommands     []string `json:"online_commands,omitempty"`
	AutomaticResponses []string `json:"automatic_responses,omitempty"`
}

var profiles = map[string]Profile{
	"CGP22C": {Model: "CGP22C", Name: "Qingping CO2 & Temp & RH Monitor", Protocol: "hex", Sensors: []string{"temperature", "humidity", "co2", "battery", "signal_strength"}, OfflineSettings: []string{"upload_interval", "record_interval", "co2_indicator"}},
	"CGDN1":  {Model: "CGDN1", Name: "Qingping Air Monitor Lite v1", Protocol: "json", Sensors: []string{"temperature", "humidity", "co2", "pm25", "pm10", "battery", "signal_strength"}, OfflineSettings: []string{"upload_interval", "record_interval"}},
	"CGP23W": {Model: "CGP23W", Name: "Qingping Temp & RH Monitor", Protocol: "hex", Sensors: []string{"temperature", "humidity", "pressure", "battery", "signal_strength"}, OfflineSettings: []string{"upload_interval", "record_interval"}},
}

func Lookup(model string) Profile {
	if p, ok := profiles[strings.ToUpper(model)]; ok {
		p.OfflineSettings = nil
		for _, s := range settings.Catalog {
			if p.Protocol == "hex" && s.HEX != 0 || p.Protocol == "json" && s.JSON != "" {
				p.OfflineSettings = append(p.OfflineSettings, s.Name)
			}
		}
		switch p.Model {
		case "CGP22C":
			p.LiveWrites = true
			p.OnlineCommands = []string{"apply-settings", "sync-time", "raw"}
		case "CGDN1":
			p.LiveWrites = true
			p.OnlineCommands = []string{"apply-settings", "settings-get", "sync-time", "realtime", "raw"}
			p.AutomaticResponses = []string{"startup", "history-ack", "type18-heartbeat (opt-in)"}
		}
		return p
	}
	return Profile{Model: model, Name: "Generic Qingping device", Protocol: "auto", Sensors: []string{}}
}

type Adapter interface {
	Interpret(protocol.Message, domain.Direction, time.Time) (domain.Event, error)
}
type ModelAdapter struct{ Model string }

func Decode(raw []byte, kind, model string, direction domain.Direction, received time.Time) (domain.Event, error) {
	if !direction.Valid() {
		return domain.Event{}, errors.New("direction must be from-device or to-device")
	}
	p := Lookup(model)
	if p.Protocol != "auto" && kind != "" && kind != "auto" && kind != p.Protocol {
		return domain.Event{}, errors.New("model and protocol conflict")
	}
	if (kind == "" || kind == "auto") && p.Protocol != "auto" {
		kind = p.Protocol
	}
	m, err := protocol.Decode(raw, kind)
	if err != nil {
		return domain.Event{}, err
	}
	return (ModelAdapter{model}).Interpret(m, direction, received)
}

func (a ModelAdapter) Interpret(m protocol.Message, d domain.Direction, received time.Time) (result domain.Event, resultErr error) {
	defer func() {
		if m.Protocol == "hex" && (m.Command == 0x32 || m.Command == 0x39) {
			fields := map[byte][][]byte{}
			for _, f := range m.Entries {
				fields[f.Key] = append(fields[f.Key], f.Value)
			}
			result.SettingsConfig, result.SettingsObserved = settings.Observe("hex", fields, nil)
		}
		if m.Protocol == "json" {
			safe, _ := protocol.RedactJSON(m.Raw)
			var envelope struct {
				Settings map[string]json.RawMessage `json:"setting"`
			}
			if json.Unmarshal(safe, &envelope) == nil && envelope.Settings != nil {
				result.SettingsConfig, result.SettingsObserved = settings.Observe("json", nil, envelope.Settings)
			}
		}
	}()
	e := domain.Event{SchemaVersion: 1, Model: a.Model, Protocol: m.Protocol, Direction: d, ReceivedAt: received.UTC(), Metadata: map[string]any{}, Settings: map[string]any{}, Unknown: map[string]any{}}
	if m.Protocol == "json" {
		return interpretJSON(m, e)
	}
	e.MessageType = fmt.Sprintf("0x%02x", m.Command)
	model := strings.ToUpper(a.Model)
	// Product 0x33 occurs consistently in captured CGP22C reports. Only this
	// observed association is inferred; never infer CO2 from a plausible value.
	for _, f := range m.Entries {
		if f.Key == 0x38 && len(f.Value) == 2 && binary.LittleEndian.Uint16(f.Value) == 0x33 {
			if Lookup(model).Protocol == "auto" {
				model = "CGP22C"
				e.Model = model
			} else if model != "CGP22C" {
				return e, errors.New("reported product ID conflicts with configured model")
			}
		}
	}
	for i, f := range m.Entries {
		v := f.Value
		handled := true
		var err error
		switch {
		case f.Key == 0x03 && d == domain.FromDevice:
			err = history(&e, v, 6, model)
		case f.Key == 0x33 && d == domain.FromDevice && m.Command != 0x33:
			err = history(&e, v, 8, model)
		case f.Key == 0x14 && d == domain.FromDevice:
			if len(v) != 12 {
				err = errors.New("network report must contain 12 bytes")
				break
			}
			s := sample(int64(binary.LittleEndian.Uint32(v)), "realtime", received)
			s.Readings = legacy(v[4:10], model)
			s.Readings = append(s.Readings, reading("signal_strength", float64(int8(v[10])), "dBm"))
			e.Samples = append(e.Samples, s)
		case f.Key == 0x85 && d == domain.FromDevice:
			err = typed(&e, v, m.Command)
		case alarmSensor(f.Key) != "" && (m.Command == 0x32 || m.Command == 0x39):
			if len(v) != 11 && len(v) != 13 {
				err = errors.New("alarm settings require 11 or 13 bytes")
				break
			}
			value := map[string]any{"sensor": alarmSensor(f.Key), "condition": alarmCondition(f.Key), "repeat_raw": v[0], "start_minute": binary.LittleEndian.Uint32(v[1:5]), "end_minute": binary.LittleEndian.Uint32(v[5:9]), "threshold": threshold(f.Key, binary.LittleEndian.Uint16(v[9:11]))}
			if len(v) == 13 {
				value["sound_duration_seconds"] = binary.LittleEndian.Uint16(v[11:13])
			}
			e.Settings[fmt.Sprintf("alarm_0x%02x", f.Key)] = value
		case alarmSensor(f.Key) != "" && d == domain.FromDevice:
			if len(v) != 12 {
				err = errors.New("alarm report requires 12 bytes")
				break
			}
			ts := int64(binary.LittleEndian.Uint32(v))
			s := sample(ts, "event", received)
			s.Readings = legacy(v[4:10], model)
			e.Samples = append(e.Samples, s)
			e.Alarms = append(e.Alarms, domain.Alarm{Sensor: alarmSensor(f.Key), Condition: alarmCondition(f.Key), Threshold: threshold(f.Key, binary.LittleEndian.Uint16(v[10:12])), Timestamp: ts})
		default:
			handled, err = hexField(&e, f)
		}
		if !handled || err != nil {
			e.Unknown[fmt.Sprintf("%d:0x%02x", i, f.Key)] = hex.EncodeToString(v)
		}
		if err != nil {
			e.Warnings = append(e.Warnings, fmt.Sprintf("key 0x%02x: %s", f.Key, err))
		}
	}
	if model != "CGP22C" && model != "CGP23W" {
		unresolved := false
		for i, f := range m.Entries {
			if f.Key == 0x03 || f.Key == 0x14 || f.Key == 0x33 {
				e.Unknown[fmt.Sprintf("legacy_%d:0x%02x", i, f.Key)] = hex.EncodeToString(f.Value)
				unresolved = true
			}
		}
		if unresolved {
			e.Warnings = append(e.Warnings, "legacy third sensor is unresolved; specify a model for CO2/pressure")
		}
	}

	return e, nil
}

func reading(sensor string, v float64, unit string) domain.Reading {
	return domain.Reading{Sensor: sensor, Value: v, Unit: unit, Quality: "normal"}
}
func sample(ts int64, origin string, received time.Time) domain.Sample {
	return domain.Sample{Timestamp: ts, TimeQuality: domain.TimeQuality(ts, received), Origin: origin, Readings: []domain.Reading{}}
}
func legacy(v []byte, model string) []domain.Reading {
	p := uint32(v[0]) | uint32(v[1])<<8 | uint32(v[2])<<16
	r := []domain.Reading{reading("temperature", float64(int(p>>12)-500)/10, "°C"), reading("humidity", float64(p&4095)/10, "%")}
	x := float64(binary.LittleEndian.Uint16(v[3:5]))
	if model == "CGP22C" {
		r = append(r, reading("co2", x, "ppm"))
	}
	if model == "CGP23W" {
		r = append(r, reading("pressure", x/10, "hPa"))
	}
	return append(r, reading("battery", float64(v[5]), "%"))
}
func history(e *domain.Event, v []byte, width int, model string) error {
	if len(v) < 6 || (len(v)-6)%width != 0 {
		return errors.New("invalid history group length")
	}
	ts := int64(binary.LittleEndian.Uint32(v))
	step := int64(binary.LittleEndian.Uint16(v[4:6]))
	if step == 0 && len(v) > 6+width {
		e.Warnings = append(e.Warnings, "history has zero recording interval")
	}
	for i, pos := 0, 6; pos < len(v); i, pos = i+1, pos+width {
		if bytes.Equal(v[pos:pos+6], bytes.Repeat([]byte{255}, 6)) {
			break
		}
		s := sample(ts+int64(i)*step, "historical", e.ReceivedAt)
		s.Readings = legacy(v[pos:pos+6], model)
		e.Samples = append(e.Samples, s)
	}
	if width == 8 {
		e.Unknown["history_aux_bytes"] = hex.EncodeToString(v)
		e.Warnings = append(e.Warnings, "extended legacy AUX interpretation is unverified; base readings decoded")
	}
	return nil
}
func typed(e *domain.Event, v []byte, cmd byte) error {
	if len(v) < 5 {
		return errors.New("typed report requires timestamp and format")
	}
	if v[4] != 4 {
		return fmt.Errorf("unsupported typed sensor layout %d", v[4])
	}
	if len(v) != 11 {
		return errors.New("typed temperature/humidity/CO2 layout requires 11 bytes")
	}
	origin := "realtime"
	if cmd == 0x42 {
		origin = "historical"
	}
	if cmd == 0x41 || cmd == 0x44 {
		origin = "event"
	}
	s := sample(int64(binary.LittleEndian.Uint32(v)), origin, e.ReceivedAt)
	for i, name := range []string{"temperature", "humidity", "co2"} {
		raw := binary.LittleEndian.Uint16(v[5+i*2 : 7+i*2])
		n := float64(raw)
		unit := "ppm"
		if i == 0 {
			n = float64(int16(raw)) / 10
			unit = "°C"
		}
		if i == 1 {
			n /= 10
			unit = "%"
		}
		r := reading(name, n, unit)
		if raw&0xff00 == 0xf000 {
			r.Quality = "sensor-error"
			status := int(raw - 0xf000)
			r.Status = &status
		}
		s.Readings = append(s.Readings, r)
	}
	e.Samples = append(e.Samples, s)
	return nil
}
func alarmSensor(k byte) string {
	switch k {
	case 0x07, 0x08:
		return "temperature"
	case 0x0a, 0x0b:
		return "humidity"
	case 0x0d, 0x0e:
		return "pressure"
	case 0x39, 0x3a:
		return "co2"
	}
	return ""
}
func alarmCondition(k byte) string {
	switch k {
	case 0x07, 0x0a, 0x0d, 0x39:
		return "above"
	}
	return "below"
}
func threshold(k byte, v uint16) float64 {
	switch alarmSensor(k) {
	case "temperature":
		return float64(int(v)-500) / 10
	case "humidity":
		return float64(v) / 10
	case "pressure":
		return float64(v) / 100
	}
	return float64(v)
}

func hexField(e *domain.Event, f protocol.TLV) (bool, error) {
	v := f.Value
	strs := map[byte]string{0x02: "serial_number", 0x11: "firmware", 0x22: "hardware", 0x34: "wireless_firmware", 0x35: "mcu_firmware", 0x43: "sntp_server"}
	if name, ok := strs[f.Key]; ok {
		e.Metadata[name] = string(bytes.TrimRight(v, "\x00"))
		return true, nil
	}
	if f.Key == 0x01 || f.Key == 0x20 || f.Key == 0x25 || f.Key == 0x28 {
		e.Metadata[fmt.Sprintf("sensitive_0x%02x", f.Key)] = "[redacted]"
		return true, nil
	}
	bools := map[byte]string{0x19: "fahrenheit_display", 0x3e: "12_hour_display", 0x40: "co2_auto_calibration", 0x44: "sntp_enabled", 0x63: "led_enabled"}
	if name, ok := bools[f.Key]; ok {
		if len(v) != 1 || v[0] > 1 {
			return true, errors.New("invalid boolean")
		}
		e.Settings[name] = v[0] == 1
		return true, nil
	}
	statuses := map[byte]string{0x1d: "all_data_sent", 0x2c: "usb_connected", 0x4a: "co2_calibrating", 0x64: "battery_percent"}
	if name, ok := statuses[f.Key]; ok {
		if len(v) != 1 {
			return true, errors.New("expected one byte")
		}
		e.Metadata[name] = int(v[0])
		return true, nil
	}
	if f.Key == 0x15 || f.Key == 0x24 {
		if len(v) != 4 {
			return true, errors.New("expected 4-byte timestamp")
		}
		ts := int64(binary.LittleEndian.Uint32(v))
		if f.Key == 0x15 {
			e.ReportedAt = &ts
		} else {
			e.Metadata["next_upload_timestamp"] = ts
		}
		return true, nil
	}
	if f.Key == 0x38 {
		if len(v) != 2 {
			return true, errors.New("expected 2-byte product ID")
		}
		e.Metadata["product_id"] = binary.LittleEndian.Uint16(v)
		return true, nil
	}
	if f.Key == 0x3c {
		if len(v) != 4 {
			return true, errors.New("expected two CO2 range endpoints")
		}
		e.Settings["co2_indicator"] = map[string]any{"yellow_min_ppm": binary.LittleEndian.Uint16(v[:2]), "yellow_max_ppm": binary.LittleEndian.Uint16(v[2:]), "inclusive": true}
		return true, nil
	}
	intervals := map[byte]string{0x04: "upload_interval_seconds", 0x05: "record_interval_seconds", 0x1b: "alert_repeat_seconds", 0x37: "report_delay_seconds", 0x3b: "co2_measurement_interval_seconds", 0x3d: "battery_poweroff_seconds", 0x7b: "high_frequency_interval_seconds"}
	if name, ok := intervals[f.Key]; ok {
		if len(v) != 2 {
			return true, errors.New("expected 2-byte interval")
		}
		n := int(binary.LittleEndian.Uint16(v))
		if f.Key == 0x04 || f.Key == 0x3b || f.Key == 0x3d {
			n *= 60
		}
		e.Settings[name] = n
		return true, nil
	}
	offsets := map[byte]string{0x3f: "co2_offset_percent", 0x45: "co2_offset_ppm", 0x46: "temperature_offset_c", 0x47: "temperature_offset_percent", 0x48: "humidity_offset_percent_rh", 0x49: "humidity_offset_percent"}
	if name, ok := offsets[f.Key]; ok {
		if len(v) != 2 {
			return true, errors.New("expected 2-byte offset")
		}
		n := float64(int16(binary.LittleEndian.Uint16(v)))
		if f.Key != 0x45 {
			n /= 10
		}
		e.Settings[name] = n
		return true, nil
	}
	return false, nil
}

func number(raw json.RawMessage) (float64, bool) {
	var n json.Number
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if d.Decode(&n) != nil {
		return 0, false
	}
	v, err := n.Float64()
	return v, err == nil && !math.IsNaN(v) && !math.IsInf(v, 0)
}
func scalar(raw json.RawMessage) any {
	var v any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	_ = d.Decode(&v)
	return v
}
func stringField(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	if n, ok := number(raw); ok {
		return strconv.FormatFloat(n, 'f', -1, 64)
	}
	return ""
}

func interpretJSON(m protocol.Message, e domain.Event) (domain.Event, error) {
	// Sanitize before extracting unknown metadata or nested settings.
	safe, _ := protocol.RedactJSON(m.Raw)
	f := m.Fields
	if len(safe) > 0 {
		var fields map[string]json.RawMessage
		if json.Unmarshal(safe, &fields) == nil {
			f = fields
		}
	}
	e.MessageType = stringField(f["type"])
	if e.MessageType == "" {
		return e, errors.New("missing or invalid JSON message type")
	}
	if n, ok := number(f["timestamp"]); ok && n == math.Trunc(n) && n >= 0 && n < 1e12 {
		ts := int64(n)
		e.ReportedAt = &ts
	}
	if e.Direction == domain.FromDevice {
		ack, _ := number(f["need_ack"])
		e.ResponseRequired = ack == 1 || e.MessageType == "10" || e.MessageType == "25"
	}
	meta := map[string]bool{"mac": true, "wifi_mac": true, "version": true, "sw_version": true, "hw_version": true, "module_version": true, "iot_platform": true, "timezone": true, "id": true, "ack_id": true, "code": true, "result": true, "desc": true}
	for k, v := range f {
		if meta[k] {
			e.Metadata[k] = scalar(v)
			continue
		}
		switch k {
		case "type", "timestamp", "sensorData", "setting", "wifi_info", "need_ack":
		case "hk_salt", "mqtt_cfg", "wifi_cfg", "host", "password":
			e.Unknown[k] = "[redacted]"
		default:
			e.Unknown[k] = scalar(v)
		}
	}
	if raw, ok := f["setting"]; ok {
		var settings map[string]json.RawMessage
		if json.Unmarshal(raw, &settings) != nil || settings == nil {
			e.Warnings = append(e.Warnings, "setting must be an object")
		} else {
			for k, v := range settings {
				if strings.Contains(strings.ToLower(k), "pass") || strings.Contains(strings.ToLower(k), "secret") {
					e.Settings[k] = "[redacted]"
				} else {
					e.Settings[k] = scalar(v)
				}
			}
			for key, name := range map[string]string{"report_interval": "upload_interval_seconds", "collect_interval": "record_interval_seconds"} {
				if n, ok := number(settings[key]); ok {
					e.Settings[name] = n
				}
			}
		}
	}
	if raw, ok := f["wifi_info"]; ok && e.Direction == domain.FromDevice {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			parts := strings.Split(s, ",")
			if len(parts) >= 4 {
				rssi, err := strconv.ParseFloat(parts[len(parts)-3], 64)
				if err == nil && !math.IsNaN(rssi) && !math.IsInf(rssi, 0) {
					ts := int64(0)
					if e.ReportedAt != nil {
						ts = *e.ReportedAt
					}
					x := sample(ts, "heartbeat", e.ReceivedAt)
					x.Readings = append(x.Readings, reading("signal_strength", rssi, "dBm"))
					e.Samples = append(e.Samples, x)
					e.Metadata["wifi_channel"] = parts[len(parts)-2]
				}
			}
		}
	}
	if raw, ok := f["sensorData"]; ok && e.Direction == domain.FromDevice {
		var groups []map[string]json.RawMessage
		if json.Unmarshal(raw, &groups) != nil {
			return e, errors.New("sensorData must be an array of objects")
		}
		origin := "event"
		if e.MessageType == "17" {
			origin = "historical"
		}
		if e.MessageType == "12" {
			origin = "realtime"
		}
		units := map[string]string{"temperature": "°C", "humidity": "%", "co2": "ppm", "pm1": "µg/m³", "pm25": "µg/m³", "pm10": "µg/m³", "battery": "%", "tvoc": "ppb", "tvoc_index": "index", "noise": "dB", "light": "lx"}
		for i, g := range groups {
			var t struct {
				Value json.RawMessage `json:"value"`
			}
			_ = json.Unmarshal(g["timestamp"], &t)
			n, ok := number(t.Value)
			ts := int64(0)
			if ok && n >= 0 && n < 1e12 && n == math.Trunc(n) {
				ts = int64(n)
			}
			s := sample(ts, origin, e.ReceivedAt)
			keys := make([]string, 0, len(g))
			for k := range g {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				raw := g[k]
				if k == "timestamp" {
					continue
				}
				unit, known := units[k]
				if !known {
					e.Unknown[fmt.Sprintf("sensorData[%d].%s", i, k)] = scalar(raw)
					continue
				}
				var r struct {
					Value  json.RawMessage `json:"value"`
					Status *int            `json:"status"`
					Unit   string          `json:"unit"`
				}
				if json.Unmarshal(raw, &r) != nil {
					e.Warnings = append(e.Warnings, fmt.Sprintf("invalid %s reading in group %d", k, i))
					continue
				}
				value, ok := number(r.Value)
				if !ok {
					e.Warnings = append(e.Warnings, fmt.Sprintf("missing or invalid %s value in group %d", k, i))
					continue
				}
				x := reading(k, value, unit)
				if r.Unit != "" && r.Unit != unit {
					x.OriginalValue = &value
					x.OriginalUnit = r.Unit
					x.Unit = r.Unit
					x.Quality = "unrecognized-unit"
					if k == "temperature" && (r.Unit == "F" || r.Unit == "°F") {
						x.Value = (value - 32) / 9 * 5
						x.Unit = "°C"
						x.Quality = "normal"
					} else if k == "temperature" && r.Unit == "C" {
						x.Unit = "°C"
						x.Quality = "normal"
					}
				}
				if k == "battery" {
					x.ChargingStatus = r.Status
				} else {
					x.Status = r.Status
					if r.Status != nil && *r.Status != 0 {
						status := map[int]string{1: "sensor-error", 2: "initializing", 3: "disabled", 4: "not-installed"}
						x.Quality = status[*r.Status]
						if x.Quality == "" {
							x.Quality = "unknown-status"
						}
					}
				}
				s.Readings = append(s.Readings, x)
			}
			e.Samples = append(e.Samples, s)
		}
	}
	return e, nil
}
