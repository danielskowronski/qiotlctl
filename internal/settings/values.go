package settings

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/danielskowronski/qiotlctl/internal/protocol"
	"github.com/knadh/koanf/maps"
	"gopkg.in/yaml.v3"
)

type Values map[string]any

// Options changes device-value validation only. Wire types, widths and exact
// representability are always checked; known firmware exceptions need no opt-in.
type Options struct {
	IgnoreOfficialLimits bool
}

// Field is one atomic property. Compound ranges and alarms remain one field.
type Field struct {
	Name    string        `json:"name"`
	Key     string        `json:"wire_key"`
	Desired any           `json:"value"`
	Raw     bool          `json:"raw,omitempty"`
	HEX     *protocol.TLV `json:"-"`
	JSON    any           `json:"-"`
}
type Patch struct {
	Protocol string  `json:"protocol"`
	Fields   []Field `json:"fields"`
}

func Set(v Values, path string, value any) error {
	parts := strings.Split(path, ".")
	m := map[string]any(v)
	for _, part := range parts[:len(parts)-1] {
		if part == "" {
			return errors.New("empty settings path")
		}
		if m[part] == nil {
			m[part] = map[string]any{}
		}
		child, ok := m[part].(map[string]any)
		if !ok {
			return fmt.Errorf("%s conflicts with a parent value", path)
		}
		m = child
	}
	if parts[len(parts)-1] == "" {
		return errors.New("empty settings path")
	}
	m[parts[len(parts)-1]] = value
	return nil
}
func Clone(v Values) Values {
	b, _ := json.Marshal(v)
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var out Values
	_ = d.Decode(&out)
	if out == nil {
		out = Values{}
	}
	return out
}

// ParseSet accepts YAML values (including booleans/lists) but does not invent paths.
func ParseSet(v Values, assignment string) error {
	path, text, ok := strings.Cut(assignment, "=")
	if !ok {
		return errors.New("--set requires path=value")
	}
	var value any
	if err := yaml.Unmarshal([]byte(text), &value); err != nil {
		return fmt.Errorf("invalid YAML value for %s", path)
	}
	if value == nil {
		return errors.New("--set requires a value; use YAML null in an overlay to unmanage")
	}
	return Set(v, path, value)
}
func Build(kind string, values Values, options ...Options) (Patch, error) {
	var opt Options
	if len(options) > 0 {
		opt = options[0]
	}
	p := Patch{Protocol: kind, Fields: []Field{}}
	if _, err := json.Marshal(values); err != nil {
		return p, errors.New("settings contain non-JSON-compatible values")
	}
	values = Clone(values)
	if kind != "hex" && kind != "json" {
		return p, errors.New("settings require explicit hex/json protocol or a known model")
	}
	definitions := map[string]Spec{}
	for _, s := range Catalog {
		definitions[s.Name] = s
	}
	var walk func(string, map[string]any) error
	walk = func(prefix string, m map[string]any) error {
		keys := sortedKeys(m)
		for _, key := range keys {
			path := key
			if prefix != "" {
				path = prefix + "." + key
			}
			value := m[key]
			if path == "raw" {
				continue
			}
			if path == "threshold_alarms" {
				if kind != "hex" {
					return errors.New("threshold_alarms currently requires HEX; JSON scene commands are a separate feature")
				}
				fields, e := encodeAlarms(value, opt)
				if e != nil {
					return e
				}
				p.Fields = append(p.Fields, fields...)
				continue
			}
			if s, ok := definitions[path]; ok {
				if !s.supported(kind) {
					return fmt.Errorf("%s has no understood %s encoding; use raw for experiments", path, kind)
				}
				f, e := s.encode(kind, value, opt)
				if e != nil {
					return fmt.Errorf("%s: %w", path, e)
				}
				p.Fields = append(p.Fields, f)
				continue
			}
			child, ok := value.(map[string]any)
			if !ok || !knownPrefix(path) {
				return fmt.Errorf("unknown setting %s", path)
			}
			if err := walk(path, child); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk("", map[string]any(values)); err != nil {
		return p, err
	}
	if raw, ok := values["raw"]; ok {
		fields, err := rawFields(kind, raw)
		if err != nil {
			return p, err
		}
		p.Fields = append(p.Fields, fields...)
	}
	seen := map[string]bool{}
	for _, f := range p.Fields {
		if seen[f.Key] {
			return p, fmt.Errorf("duplicate or typed/raw collision for wire property %s", f.Key)
		}
		seen[f.Key] = true
	}
	return p, nil
}
func knownPrefix(path string) bool {
	for _, s := range Catalog {
		if strings.HasPrefix(s.Name, path+".") {
			return true
		}
	}
	return false
}
func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
func integer(value any, scale, lo, hi int64) (int64, error) {
	switch value.(type) {
	case int, int64, uint64, uint16, float64, float32, json.Number:
	default:
		return 0, errors.New("expected a number")
	}
	r, ok := new(big.Rat).SetString(fmt.Sprint(value))
	if !ok {
		return 0, errors.New("invalid number")
	}
	r.Mul(r, new(big.Rat).SetInt64(scale))
	if !r.IsInt() || !r.Num().IsInt64() {
		return 0, fmt.Errorf("value must be exactly representable in steps of 1/%d", scale)
	}
	n := r.Num().Int64()
	if n < lo || n > hi {
		return 0, fmt.Errorf("encoded value outside %d..%d", lo, hi)
	}
	return n, nil
}
func minute(value any) (int64, error) {
	text, ok := value.(string)
	if !ok || len(text) != 5 {
		return 0, errors.New("expected quoted HH:MM")
	}
	t, err := time.Parse("15:04", text)
	if err != nil {
		return 0, errors.New("time must be 00:00..23:59")
	}
	return int64(t.Hour()*60 + t.Minute()), nil
}

// Experimental numeric forms are already in wire units. Named forms retain
// their normal unit conversions, without silently truncating or wrapping.
func clockMinute(value any, lo, hi int64, opt Options) (int64, error) {
	if opt.IgnoreOfficialLimits {
		if text, ok := value.(string); ok {
			sign := int64(1)
			if strings.HasPrefix(text, "-") {
				sign, text = -1, text[1:]
			}
			hours, minutes, found := strings.Cut(text, ":")
			h, eh := strconv.ParseUint(hours, 10, 32)
			m, em := strconv.ParseUint(minutes, 10, 8)
			if !found || len(hours) < 2 || len(minutes) != 2 || eh != nil || em != nil || m > 59 {
				return 0, errors.New("expected HH:MM or an integer count of minutes")
			}
			value = sign * int64(h*60+m)
		}
		return integer(value, 1, lo, hi)
	}
	return minute(value)
}

func timezone(value any, lo, hi int64, opt Options) (int64, error) {
	if _, ok := value.(string); !ok && opt.IgnoreOfficialLimits {
		return integer(value, 1, lo, hi)
	}
	text, ok := value.(string)
	if !ok || len(text) < 6 || (!opt.IgnoreOfficialLimits && len(text) != 6) || (text[0] != '+' && text[0] != '-') {
		return 0, errors.New("expected fixed UTC offset such as '+02:00'")
	}
	maxMagnitude := max(-lo, hi) * 6
	n, err := clockMinute(text[1:], 0, maxMagnitude, opt)
	if err != nil {
		return 0, err
	}
	if text[0] == '-' {
		n = -n
	}
	if n%6 != 0 {
		return 0, errors.New("offset must be exactly representable in 6-minute steps")
	}
	if !opt.IgnoreOfficialLimits && (n < -12*60 || n > 14*60) {
		return 0, errors.New("offset must be -12:00..+14:00")
	}
	return integer(n/6, 1, lo, hi)
}
func (s Spec) encode(kind string, value any, opt Options) (Field, error) {
	f := Field{Name: s.Name, Desired: value, Key: s.JSON}
	var n int64
	var data []byte
	var err error
	lo, hi := s.bounds(kind)
	if opt.IgnoreOfficialLimits {
		lo, hi = s.wireBounds(kind)
	}
	switch s.Kind {
	case "bool":
		v, ok := value.(bool)
		if !ok {
			if !opt.IgnoreOfficialLimits {
				return f, errors.New("expected true or false")
			}
			n, err = integer(value, 1, lo, hi)
		} else if v {
			n = 1
		}
		data = []byte{byte(n)}
		f.JSON = n
	case "enum":
		text, ok := value.(string)
		index := -1
		for i, v := range s.Enum {
			if ok && v == text {
				index = i
			}
		}
		if index < 0 {
			if !opt.IgnoreOfficialLimits {
				return f, fmt.Errorf("choose %s", strings.Join(s.Enum, ", "))
			}
			if kind == "json" && s.JSON == "temperature_unit" {
				if !ok {
					return f, errors.New("temperature unit must be a string")
				}
				f.JSON = text
				break
			}
			n, err = integer(value, 1, lo, hi)
		} else {
			n = int64(index)
		}
		data = []byte{byte(n)}
		f.JSON = n
		if kind == "json" && s.JSON == "temperature_unit" {
			f.JSON = []string{"C", "F"}[index]
		}
	case "duration":
		text, ok := value.(string)
		if !ok {
			return f, errors.New("expected duration such as 30s or 5m")
		}
		d, e := time.ParseDuration(text)
		unit := s.durationUnit(kind)
		if e != nil || d%unit != 0 {
			return f, fmt.Errorf("requires whole %s units representable as a duration", unit)
		}
		n = int64(d / unit)
		if n < lo || n > hi {
			return f, fmt.Errorf("requires %d..%d units of %s", lo, hi, unit)
		}
		if !opt.IgnoreOfficialLimits && s.JSON == "auto_slideing_time" && kind == "json" && n > 60 && n != 120 {
			return f, errors.New("choose 0..60s or the observed 120s compatibility value")
		}
		f.Desired = d.String()
		f.JSON = n
	case "number":
		n, err = integer(value, s.scale(kind), lo, hi)
		f.JSON = n
	case "time":
		n, err = clockMinute(value, -2147483648, 2147483647, opt)
		f.JSON = n
	case "timezone":
		wireLo, wireHi := s.wireBounds(kind)
		n, err = timezone(value, wireLo, wireHi, opt)
		f.JSON = n
	case "string":
		text, ok := value.(string)
		if !ok || len(text) > 65535 || (!opt.IgnoreOfficialLimits && (text == "" || strings.ContainsAny(text, "\x00\r\n"))) {
			return f, errors.New("expected a nonempty string without NUL/newlines, at most 65535 bytes")
		}
		data = []byte(text)
		f.JSON = text
	case "thresholds":
		list, ok := value.([]any)
		if !ok || (!opt.IgnoreOfficialLimits && len(list) == 0) {
			return f, errors.New("expected a nonempty ordered threshold list")
		}
		parts := []string{}
		var prev int64
		for i, v := range list {
			n, e := integer(v, s.JSONScale, lo, hi)
			if e != nil {
				return f, e
			}
			if !opt.IgnoreOfficialLimits && i > 0 && n <= prev {
				return f, errors.New("thresholds must be strictly increasing")
			}
			prev = n
			parts = append(parts, strconv.FormatInt(n, 10))
		}
		f.JSON = strings.Join(parts, ",")
	case "pages":
		list, ok := value.([]any)
		if !ok || (!opt.IgnoreOfficialLimits && len(list) == 0) {
			return f, errors.New("expected a nonempty list of sensor names")
		}
		pages := []string{}
		for _, v := range list {
			name, ok := v.(string)
			if !ok {
				return f, errors.New("page names must be strings")
			}
			switch name {
			case "temperature":
				name = "temp"
			case "humidity", "co2", "pm25", "pm10":
			default:
				if !opt.IgnoreOfficialLimits {
					return f, errors.New("pages: temperature, humidity, co2, pm25 or pm10")
				}
			}
			pages = append(pages, name)
		}
		f.JSON = strings.Join(pages, ",")
	case "range":
		m, ok := value.(map[string]any)
		if !ok || len(m) != 2 {
			return f, errors.New("requires yellow_min_ppm and yellow_max_ppm")
		}
		a, e := integer(m["yellow_min_ppm"], 1, 0, 65535)
		if e != nil {
			return f, e
		}
		b, e := integer(m["yellow_max_ppm"], 1, 0, 65535)
		if e != nil || (!opt.IgnoreOfficialLimits && a > b) {
			return f, errors.New("yellow endpoints must be 0..65535 and min <= max")
		}
		data = make([]byte, 4)
		binary.LittleEndian.PutUint16(data, uint16(a))
		binary.LittleEndian.PutUint16(data[2:], uint16(b))
	default:
		return f, errors.New("unsupported setting kind")
	}
	if err != nil {
		return f, err
	}
	if kind == "hex" {
		if data == nil {
			data = make([]byte, 2)
			binary.LittleEndian.PutUint16(data, uint16(n))
		}
		f.HEX = &protocol.TLV{Key: s.HEX, Value: data}
		f.Key = fmt.Sprintf("0x%02x", s.HEX)
	}
	return f, nil
}
func rawFields(kind string, value any) ([]Field, error) {
	m, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("raw must be a mapping")
	}
	out := []Field{}
	for k := range m {
		if k != kind {
			return nil, fmt.Errorf("raw.%s does not match device protocol %s", k, kind)
		}
	}
	if value, ok := m["hex"]; ok {
		list, ok := value.([]any)
		if !ok {
			return nil, errors.New("raw.hex must be an ordered list")
		}
		for _, v := range list {
			m, ok := v.(map[string]any)
			if !ok || len(m) != 2 {
				return nil, errors.New("raw HEX item requires key and value_hex")
			}
			key, ok := m["key"].(string)
			if !ok {
				return nil, errors.New("raw key must be quoted, e.g. '0x06'")
			}
			n, e := strconv.ParseUint(key, 0, 8)
			if e != nil {
				return nil, errors.New("raw HEX key must fit one byte")
			}
			text, ok := m["value_hex"].(string)
			if !ok {
				return nil, errors.New("value_hex must be quoted")
			}
			data, e := hex.DecodeString(text)
			if e != nil || len(data) > 65535 {
				return nil, errors.New("invalid raw HEX bytes or length")
			}
			key = fmt.Sprintf("0x%02x", n)
			out = append(out, Field{Name: "raw.hex." + key, Key: key, Raw: true, Desired: text, HEX: &protocol.TLV{Key: byte(n), Value: data}})
		}
	}
	if value, ok := m["json"]; ok {
		m, ok := value.(map[string]any)
		if !ok {
			return nil, errors.New("raw.json must be an object")
		}
		for _, key := range sortedKeys(m) {
			if key == "" {
				return nil, errors.New("empty raw JSON key")
			}
			if _, e := json.Marshal(m[key]); e != nil {
				return nil, fmt.Errorf("raw JSON %s is not JSON-compatible", key)
			}
			out = append(out, Field{Name: "raw.json." + key, Key: key, Raw: true, Desired: m[key], JSON: m[key]})
		}
	}
	return out, nil
}
func (p Patch) Encode(id int64, at time.Time) ([]byte, error) {
	if len(p.Fields) == 0 {
		return nil, errors.New("no settings supplied")
	}
	if p.Protocol == "hex" {
		tlvs := []protocol.TLV{}
		for _, f := range p.Fields {
			tlvs = append(tlvs, *f.HEX)
		}
		return protocol.EncodeHEX(0x32, tlvs)
	}
	obj := map[string]any{}
	for _, f := range p.Fields {
		obj[f.Key] = f.JSON
	}
	return json.Marshal(map[string]any{"type": "17", "id": id, "timestamp": at.Unix(), "setting": obj})
}
func (p Patch) HasHEX(key byte) bool {
	for _, f := range p.Fields {
		if f.HEX != nil && f.HEX.Key == key {
			return true
		}
	}
	return false
}

// Redacted deliberately hides all raw values. Unknown properties may hold secrets.
func (p Patch) Redacted() Patch {
	q := p
	q.Fields = append([]Field(nil), p.Fields...)
	for i := range q.Fields {
		if q.Fields[i].Raw {
			q.Fields[i].Desired = "[raw value omitted]"
		}
	}
	return q
}

// Merge overlays human settings, preserving the config loader's semantics.
func Merge(dst, src Values) error {
	if _, err := json.Marshal(src); err != nil {
		return errors.New("settings overlay contains non-JSON-compatible values")
	}
	maps.Merge(map[string]any(Clone(src)), map[string]any(dst))
	var clean func(map[string]any)
	clean = func(m map[string]any) {
		for k, v := range m {
			if v == nil {
				delete(m, k)
			} else if child, ok := v.(map[string]any); ok {
				clean(child)
			}
		}
	}
	clean(map[string]any(dst))
	return nil
}

// YAML's native encoder treats json.Number as a string. Build a YAML node from
// JSON syntax so exported raw numeric values retain their JSON type, including
// large integers. JSON syntax is valid YAML; this is not a custom YAML parser.
func (v Values) MarshalYAML() (any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var n yaml.Node
	if err = yaml.Unmarshal(b, &n); err != nil {
		return nil, err
	}
	var block func(*yaml.Node)
	block = func(n *yaml.Node) {
		n.Style = 0
		for _, c := range n.Content {
			block(c)
		}
	}
	block(&n)
	if len(n.Content) == 0 {
		return nil, nil
	}
	return n.Content[0], nil
}
