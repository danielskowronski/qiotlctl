package output

import (
	"context"
	"encoding/json"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/danielskowronski/qiotlctl/internal/config"
	"github.com/danielskowronski/qiotlctl/internal/device"
	"github.com/danielskowronski/qiotlctl/internal/domain"
)

type Publication struct {
	Topic   string          `json:"topic"`
	Retain  bool            `json:"retain"`
	QoS     byte            `json:"qos"`
	Payload json.RawMessage `json:"payload"`
}
type SensorInfo struct {
	Name, Class, Unit string
	Diagnostic        bool
}

var HASensors = map[string]SensorInfo{
	"temperature": {"Temperature", "temperature", "°C", false}, "humidity": {"Humidity", "humidity", "%", false}, "co2": {"CO2", "carbon_dioxide", "ppm", false},
	"pm1": {"PM1", "pm1", "µg/m³", false}, "pm25": {"PM2.5", "pm25", "µg/m³", false}, "pm10": {"PM10", "pm10", "µg/m³", false},
	"battery": {"Battery", "battery", "%", true}, "signal_strength": {"Signal strength", "signal_strength", "dBm", true},
	"pressure": {"Pressure", "atmospheric_pressure", "hPa", false}, "tvoc": {"TVOC", "volatile_organic_compounds_parts", "ppb", false},
	"tvoc_index": {"TVOC index", "", "index", false}, "noise": {"Noise", "sound_pressure", "dB", false}, "light": {"Illuminance", "illuminance", "lx", false},
}

func HAName(d config.Device) string {
	if d.HomeAssistant.Name != "" {
		return d.HomeAssistant.Name
	}
	words := strings.Fields(strings.NewReplacer("_", " ", "-", " ").Replace(d.HAID()))
	for i, w := range words {
		r := []rune(w)
		r[0] = unicode.ToUpper(r[0])
		words[i] = string(r)
	}
	return "Qingping " + strings.Join(words, " ")
}
func ModelDescription(model string) string {
	p := device.Lookup(model)
	if p.Model == "" {
		return p.Name
	}
	return p.Model + " - " + strings.TrimPrefix(p.Name, "Qingping ")
}
func HATarget(c config.Config) string {
	h := c.Outputs.HomeAssistant
	return Hash([]string{c.MQTT.URL, h.DiscoveryPrefix, h.StatePrefix, h.BridgeID})
}
func BridgeTopic(h config.HA) string {
	return h.StatePrefix + "/bridge/" + h.BridgeID + "/availability"
}
func SensorTopic(h config.HA, d config.Device, sensor string) string {
	return h.StatePrefix + "/" + strings.ReplaceAll(url.PathEscape(d.ID), "+", "%2B") + "/" + sensor
}
func Discovery(c config.Config, d config.Device, sensor string) (Publication, bool) {
	info, ok := HASensors[sensor]
	if !ok {
		return Publication{}, false
	}
	h := c.Outputs.HomeAssistant
	mac := strings.ToUpper(strings.ReplaceAll(d.MAC, ":", ""))
	identity := mac
	if identity == "" {
		identity = d.ID
	}
	dev := map[string]any{"identifiers": []string{"qingping_" + identity}, "manufacturer": "Qingping", "model": ModelDescription(d.Model), "name": HAName(d)}
	if mac != "" {
		dev["connections"] = [][]string{{"mac", mac}}
	}
	base := SensorTopic(h, d, sensor)
	p := map[string]any{
		"name": info.Name, "unique_id": "qingping_" + d.HAID() + "_" + sensor, "state_topic": base + "/state",
		"value_template": "{{ value_json.value }}", "unit_of_measurement": info.Unit, "state_class": "measurement", "expire_after": int64(d.HAExpiry(h, sensor) / time.Second),
		"device": dev, "qos": 1, "availability_mode": "all", "availability": []map[string]string{{"topic": BridgeTopic(h)}, {"topic": base + "/availability"}},
		"origin": map[string]string{"name": "qiotlctl"},
	}
	if info.Class != "" {
		p["device_class"] = info.Class
	}
	if info.Diagnostic {
		p["entity_category"] = "diagnostic"
	}
	b, _ := json.Marshal(p)
	return Publication{h.DiscoveryPrefix + "/sensor/qingping_" + d.HAID() + "/" + sensor + "/config", true, 1, b}, true
}
func Preview(c config.Config) []Publication {
	var out []Publication
	for _, d := range c.Devices {
		if d.HAActive() {
			for _, sensor := range device.Lookup(d.Model).Sensors {
				if p, ok := Discovery(c, d, sensor); ok {
					out = append(out, p)
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Topic < out[j].Topic })
	return out
}

type HAState struct {
	DeviceID  string
	Timestamp int64
	Reading   domain.Reading
}

func (s *Store) HAStates(ctx context.Context) ([]HAState, error) {
	rows, err := s.Recorder.Database().QueryContext(ctx, `SELECT device_id,timestamp,reading FROM ha_state ORDER BY device_id,sensor`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HAState
	for rows.Next() {
		var state HAState
		var b []byte
		if err = rows.Scan(&state.DeviceID, &state.Timestamp, &b); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(b, &state.Reading); err != nil {
			return nil, err
		}
		out = append(out, state)
	}
	return out, rows.Err()
}

type Publisher interface {
	Publish(context.Context, string, byte, bool, []byte) error
}
type HAWorker struct {
	Store     *Store
	Stats     *Stats
	Publisher Publisher
	// Worker-owned caches are cleared on reconnect/HA birth, never used as freshness clocks.
	sent map[string]string
}

func (w *HAWorker) publish(ctx context.Context, topic string, retain bool, b []byte) error {
	if err := w.Publisher.Publish(ctx, topic, 1, retain, b); err != nil {
		return err
	}
	w.Stats.HAPublished.Add(1)
	return nil
}
func (w *HAWorker) Sync(ctx context.Context, now time.Time, full bool) error {
	if full || w.sent == nil {
		w.sent = map[string]string{}
	}
	c := w.Store.Config
	h := c.Outputs.HomeAssistant
	states, err := w.Store.HAStates(ctx)
	if err != nil {
		return err
	}
	stateMap := map[string]HAState{}
	sensors := map[string]map[string]bool{}
	for _, d := range c.Devices {
		sensors[d.ID] = map[string]bool{}
		for _, s := range device.Lookup(d.Model).Sensors {
			sensors[d.ID][s] = true
		}
	}
	for _, s := range states {
		stateMap[s.DeviceID+"/"+s.Reading.Sensor] = s
		for _, d := range c.Devices {
			if d.ID == s.DeviceID && Allowed(d.Model, s.Reading.Sensor) {
				sensors[s.DeviceID][s.Reading.Sensor] = true
			}
		}
	}
	for _, d := range c.Devices {
		if !d.HAActive() {
			continue
		}
		for sensor := range sensors[d.ID] {
			discovery, ok := Discovery(c, d, sensor)
			if !ok {
				continue
			}
			if string(discovery.Payload) != w.sent[discovery.Topic] {
				// Remember ownership before publishing so interrupted discovery can be cleaned up.
				_, err = w.Store.Recorder.Database().ExecContext(ctx, `INSERT INTO ha_discovery(target,topic,payload) VALUES(?,?,?) ON CONFLICT(target,topic) DO UPDATE SET payload=excluded.payload`, HATarget(c), discovery.Topic, string(discovery.Payload))
				if err != nil {
					return err
				}
				if err = w.publish(ctx, discovery.Topic, true, discovery.Payload); err != nil {
					return err
				}
				w.sent[discovery.Topic] = string(discovery.Payload)
			}
			base := SensorTopic(h, d, sensor)
			state, exists := stateMap[d.ID+"/"+sensor]
			info := HASensors[sensor]
			fresh := exists && now.Before(time.Unix(state.Timestamp, 0).Add(d.HAExpiry(h, sensor))) && state.Timestamp <= now.Add(5*time.Minute).Unix()
			valid := fresh && GoodQuality(state.Reading.Quality) && state.Reading.Unit == info.Unit
			availability := "offline"
			if valid {
				availability = "online"
			}
			if w.sent[base+"/availability"] != availability {
				if err = w.publish(ctx, base+"/availability", true, []byte(availability)); err != nil {
					return err
				}
				w.sent[base+"/availability"] = availability
			}
			if !fresh {
				continue
			}
			var value any
			if valid {
				value = state.Reading.Value
			}
			b, _ := json.Marshal(map[string]any{"value": value, "timestamp": state.Timestamp, "quality": state.Reading.Quality})
			if w.sent[base+"/state"] != string(b) {
				if err = w.publish(ctx, base+"/state", false, b); err != nil {
					return err
				}
				w.sent[base+"/state"] = string(b)
			}
		}
	}
	return nil
}
