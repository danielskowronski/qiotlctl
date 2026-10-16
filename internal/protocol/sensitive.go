package protocol

import (
	"bytes"
	"encoding/json"
	"strings"
)

func secretKey(key string) bool {
	k := strings.ToLower(key)
	return strings.Contains(k, "password") || strings.Contains(k, "secret") || strings.Contains(k, "token") || k == "pass" || k == "passwd" || k == "hk_salt" || k == "mqtt_cfg" || k == "wifi_cfg" || k == "host"
}

// RedactJSON replaces credential fields even inside undocumented nested objects.
// It does not change the original wire representation retained by the codec.
func RedactJSON(raw json.RawMessage) (json.RawMessage, bool) {
	var value any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if d.Decode(&value) != nil {
		return raw, false
	}
	changed := false
	var walk func(any)
	walk = func(v any) {
		switch v := v.(type) {
		case map[string]any:
			for key, child := range v {
				if secretKey(key) {
					v[key] = "[redacted]"
					changed = true
				} else {
					walk(child)
				}
			}
		case []any:
			for _, child := range v {
				walk(child)
			}
		}
	}
	walk(value)
	if !changed {
		return raw, false
	}
	b, _ := json.Marshal(value)
	return b, true
}

// ContainsCredentials identifies documented credential-bearing packets and JSON
// fields. Unknown binary vendor fields cannot be classified reliably.
func ContainsCredentials(raw []byte) bool {
	m, err := Decode(raw, "auto")
	if err != nil {
		return false
	}
	if m.Protocol == "json" {
		_, found := RedactJSON(raw)
		return found
	}
	for _, f := range m.Entries {
		switch f.Key {
		case 0x01, 0x20, 0x25, 0x28:
			return true
		}
	}
	return false
}
