package settings

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Observation keeps exact normalized wire values for comparing fields. Missing or
// repeated properties cannot establish a match. It is not persisted as desired state.
type Observation map[string][]string

type Comparison struct {
	Name     string `json:"name"`
	Key      string `json:"wire_key"`
	Status   string `json:"status"`
	Desired  any    `json:"desired"`
	Observed any    `json:"observed,omitempty"`
}

func canonical(v any) string { b, _ := json.Marshal(v); return string(b) }
func (f Field) wire() string {
	if f.HEX != nil {
		return hex.EncodeToString(f.HEX.Value)
	}
	return canonical(f.JSON)
}
func (p Patch) Compare(o Observation) []Comparison {
	out := []Comparison{}
	for _, f := range p.Fields {
		c := Comparison{Name: f.Name, Key: f.Key, Status: "missing", Desired: f.Desired}
		if f.Raw {
			c.Desired = "[raw value omitted]"
		}
		if values := o[f.Key]; len(values) == 1 {
			c.Status = "different"
			c.Observed = values[0]
			if !f.Raw {
				for _, s := range Catalog {
					if s.Name != f.Name {
						continue
					}
					var b []byte
					var v any
					kind := "json"
					if f.HEX != nil {
						kind = "hex"
						b, _ = hex.DecodeString(values[0])
					} else {
						d := json.NewDecoder(strings.NewReader(values[0]))
						d.UseNumber()
						_ = d.Decode(&v)
					}
					if human, ok := s.decode(kind, b, v); ok {
						c.Observed = human
					}
					break
				}
				if f.HEX != nil {
					b, _ := hex.DecodeString(values[0])
					if a, ok := decodeAlarm(f.HEX.Key, b); ok {
						c.Observed = a
					}
				}
			}
			if f.Raw {
				c.Observed = "[raw value omitted]"
			}
			if values[0] == f.wire() {
				c.Status = "matched"
			}
		} else if len(values) > 1 {
			c.Status = "ambiguous"
		}
		out = append(out, c)
	}
	return out
}

// Observe receives only a validated settings envelope. Secret values are omitted.
func Observe(kind string, hexFields map[byte][][]byte, jsonFields map[string]json.RawMessage) (Values, Observation) {
	values := Values{}
	observed := Observation{}
	if kind == "hex" {
		alarms := []any{}
		raw := []any{}
		for key, entries := range hexFields {
			switch key {
			case 0x01, 0x20, 0x25, 0x28:
				continue
			}
			wirekey := fmt.Sprintf("0x%02x", key)
			for _, b := range entries {
				observed[wirekey] = append(observed[wirekey], hex.EncodeToString(b))
			}
			if len(entries) != 1 {
				continue
			}
			b := entries[0]
			known := false
			for _, s := range Catalog {
				if s.HEX == key && s.HEX != 0 {
					if v, ok := s.decode(kind, b, nil); ok {
						_ = Set(values, s.Name, v)
						known = true
					}
					break
				}
			}
			if !known {
				if a, ok := decodeAlarm(key, b); ok {
					alarms = append(alarms, a)
					known = true
				}
			}
			// Report metadata is not a desired setting.
			switch key {
			case 0x02, 0x11, 0x15, 0x16, 0x1d, 0x22, 0x24, 0x2c, 0x34, 0x35, 0x38, 0x4a, 0x61, 0x64, 0x65:
				known = true
			}
			if !known {
				raw = append(raw, map[string]any{"key": wirekey, "value_hex": hex.EncodeToString(b)})
			}
		}
		// Map iteration must not make exported config or tests nondeterministic.
		sortRaw(raw)
		sortAlarms(alarms)
		if len(alarms) > 0 {
			values["threshold_alarms"] = alarms
		}
		if len(raw) > 0 {
			values["raw"] = map[string]any{"hex": raw}
		}
	} else {
		raw := map[string]any{}
		for key, b := range jsonFields {
			// Caller redacts recursively before this function; never compare redactions.
			if bytes.Contains(b, []byte("[redacted]")) {
				continue
			}
			var v any
			d := json.NewDecoder(bytes.NewReader(b))
			d.UseNumber()
			if d.Decode(&v) != nil {
				continue
			}
			observed[key] = []string{canonical(v)}
			known := false
			for _, s := range Catalog {
				if s.JSON == key {
					if human, ok := s.decode(kind, nil, v); ok {
						_ = Set(values, s.Name, human)
						known = true
					}
					break
				}
			}
			if !known {
				raw[key] = v
			}
		}
		if len(raw) > 0 {
			values["raw"] = map[string]any{"json": raw}
		}
	}
	return values, observed
}
func (s Spec) decode(kind string, b []byte, value any) (any, bool) {
	var n int64
	if kind == "hex" {
		switch s.Kind {
		case "string":
			return string(bytes.TrimRight(b, "\x00")), true
		case "range":
			if len(b) != 4 {
				return nil, false
			}
			return map[string]any{"yellow_min_ppm": int(binary.LittleEndian.Uint16(b)), "yellow_max_ppm": int(binary.LittleEndian.Uint16(b[2:]))}, true
		case "bool", "enum":
			if len(b) != 1 {
				return nil, false
			}
			n = int64(b[0])
		default:
			if len(b) != 2 {
				return nil, false
			}
			n = int64(binary.LittleEndian.Uint16(b))
			if s.Kind == "number" || s.Kind == "timezone" {
				n = int64(int16(n))
			}
		}
	} else {
		if s.Kind == "thresholds" {
			text, ok := value.(string)
			if !ok {
				return nil, false
			}
			out := []any{}
			for _, v := range strings.Split(text, ",") {
				n, e := strconv.ParseInt(v, 10, 32)
				if e != nil {
					return nil, false
				}
				out = append(out, float64(n)/float64(s.JSONScale))
			}
			return out, true
		}
		if s.Kind == "pages" {
			text, ok := value.(string)
			if !ok {
				return nil, false
			}
			list := []any{}
			for _, x := range strings.Split(text, ",") {
				if x == "temp" {
					x = "temperature"
				}
				list = append(list, x)
			}
			return list, true
		}
		if s.JSON == "temperature_unit" {
			switch value {
			case "C":
				return "celsius", true
			case "F":
				return "fahrenheit", true
			default:
				return nil, false
			}
		}
		var err error
		n, err = integer(value, 1, -2147483648, 2147483647)
		if err != nil {
			return nil, false
		}
	}
	switch s.Kind {
	case "bool":
		if n != 0 && n != 1 {
			return nil, false
		}
		return n == 1, true
	case "enum":
		if n < 0 || n >= int64(len(s.Enum)) {
			return nil, false
		}
		return s.Enum[n], true
	case "duration":
		return (time.Duration(n) * s.durationUnit(kind)).String(), true
	case "number":
		return float64(n) / float64(s.scale(kind)), true
	case "time":
		if n < 0 || n >= 1440 {
			return nil, false
		}
		return fmt.Sprintf("%02d:%02d", n/60, n%60), true
	case "timezone":
		minutes := n * 6
		sign := "+"
		if minutes < 0 {
			sign = "-"
			minutes = -minutes
		}
		return fmt.Sprintf("%s%02d:%02d", sign, minutes/60, minutes%60), true
	}
	return nil, false
}

func sortRaw(v []any) {
	sort.Slice(v, func(i, j int) bool {
		return v[i].(map[string]any)["key"].(string) < v[j].(map[string]any)["key"].(string)
	})
}
func sortAlarms(v []any) {
	sort.Slice(v, func(i, j int) bool { return canonical(v[i]) < canonical(v[j]) })
}
