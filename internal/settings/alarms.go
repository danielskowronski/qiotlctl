package settings

import (
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"github.com/danielskowronski/qiotlctl/internal/protocol"
)

type alarmDefinition struct {
	sensor, unit  string
	above, below  byte
	scale, offset int64
}

var alarmDefinitions = []alarmDefinition{
	{"temperature", "c", 0x07, 0x08, 10, 500}, {"humidity", "percent_rh", 0x0a, 0x0b, 10, 0},
	{"pressure", "kpa", 0x0d, 0x0e, 100, 0}, {"co2", "ppm", 0x39, 0x3a, 1, 0},
}

func encodeAlarms(value any, opt Options) ([]Field, error) {
	list, ok := value.([]any)
	if !ok {
		return nil, errors.New("threshold_alarms must be a list")
	}
	out := []Field{}
	for i, v := range list {
		m, ok := v.(map[string]any)
		if !ok {
			return nil, errors.New("alarm must be a mapping")
		}
		for k := range m {
			switch k {
			case "sensor", "condition", "threshold", "unit", "repeat", "start", "end", "sound_duration":
			default:
				return nil, fmt.Errorf("unknown alarm field %s", k)
			}
		}
		var def *alarmDefinition
		for j := range alarmDefinitions {
			if m["sensor"] == alarmDefinitions[j].sensor {
				def = &alarmDefinitions[j]
			}
		}
		if def == nil {
			return nil, errors.New("alarm sensor must be temperature, humidity, pressure or co2")
		}
		if m["unit"] != def.unit {
			return nil, fmt.Errorf("%s alarm requires unit: %s", def.sensor, def.unit)
		}
		key := def.above
		switch m["condition"] {
		case "above":
		case "below":
			key = def.below
		default:
			return nil, errors.New("alarm condition must be above or below (strict comparisons)")
		}
		threshold, err := integer(m["threshold"], def.scale, -def.offset, 65535-def.offset)
		if err != nil {
			return nil, fmt.Errorf("alarm threshold: %w", err)
		}
		threshold += def.offset
		start, err := clockMinute(m["start"], 0, 4294967295, opt)
		if err != nil {
			return nil, err
		}
		end, err := clockMinute(m["end"], 0, 4294967295, opt)
		if err != nil {
			return nil, err
		}
		repeat := byte(1)
		switch m["repeat"] {
		case "once":
		case "daily":
			repeat = 0xfe
		default:
			if !opt.IgnoreOfficialLimits {
				return nil, errors.New("alarm repeat must be once or daily")
			}
			n, err := integer(m["repeat"], 1, 0, 255)
			if err != nil {
				return nil, fmt.Errorf("alarm repeat: %w", err)
			}
			repeat = byte(n)
		}
		b := make([]byte, 11)
		b[0] = repeat
		binary.LittleEndian.PutUint32(b[1:], uint32(start))
		binary.LittleEndian.PutUint32(b[5:], uint32(end))
		binary.LittleEndian.PutUint16(b[9:], uint16(threshold))
		if value, ok := m["sound_duration"]; ok {
			s := Spec{Kind: "duration", Unit: "s", Min: 0, Max: 65535}
			f, err := s.encode("hex", value, opt)
			if err != nil {
				return nil, fmt.Errorf("sound_duration: %w", err)
			}
			b = append(b, f.HEX.Value...)
		}
		out = append(out, Field{Name: fmt.Sprintf("threshold_alarms[%d]", i), Key: fmt.Sprintf("0x%02x", key), Desired: m, HEX: &protocol.TLV{Key: key, Value: b}})
	}
	return out, nil
}
func decodeAlarm(key byte, b []byte) (any, bool) {
	if len(b) != 11 && len(b) != 13 {
		return nil, false
	}
	for _, d := range alarmDefinitions {
		if key != d.above && key != d.below {
			continue
		}
		repeat := ""
		switch b[0] {
		case 1:
			repeat = "once"
		case 0xfe:
			repeat = "daily"
		default:
			return nil, false
		}
		a, z := binary.LittleEndian.Uint32(b[1:]), binary.LittleEndian.Uint32(b[5:])
		if a >= 1440 || z >= 1440 {
			return nil, false
		}
		condition := "above"
		if key == d.below {
			condition = "below"
		}
		m := map[string]any{"sensor": d.sensor, "condition": condition, "unit": d.unit, "threshold": float64(int64(binary.LittleEndian.Uint16(b[9:]))-d.offset) / float64(d.scale), "repeat": repeat, "start": fmt.Sprintf("%02d:%02d", a/60, a%60), "end": fmt.Sprintf("%02d:%02d", z/60, z%60)}
		if len(b) == 13 {
			m["sound_duration"] = (time.Duration(binary.LittleEndian.Uint16(b[11:])) * time.Second).String()
		}
		return m, true
	}
	return nil, false
}
