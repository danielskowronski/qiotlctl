package device

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/danielskowronski/qiotlctl/internal/protocol"
)

// JSONInteger accepts wire integer IDs without float64 precision loss.
func JSONInteger(raw json.RawMessage) (int64, bool) {
	var n int64
	if len(raw) == 0 || string(raw) == "null" || json.Unmarshal(raw, &n) != nil {
		return 0, false
	}
	return n, true
}

// IdentityMatches rejects an explicit, conflicting MAC. Missing identity is
// allowed: many responses identify the device only through its exact MQTT topic.
func IdentityMatches(m protocol.Message, mac string) bool {
	normalize := func(s string) string { return strings.ToUpper(strings.NewReplacer(":", "", "-", "").Replace(s)) }
	if mac == "" || m.Protocol != "json" {
		return true
	}
	for _, field := range []string{"mac", "wifi_mac"} {
		if raw, exists := m.Fields[field]; exists {
			var value string
			if json.Unmarshal(raw, &value) != nil || normalize(value) != normalize(mac) {
				return false
			}
		}
	}
	return true
}

type Reply struct {
	Payload []byte
	Kind    string
}

// AutomaticReply only knows documented CGDN1 exchanges and the explicitly
// selected, observed type-18 clock workaround. Transport eligibility is checked
// by the caller; offline decode/replay never invokes this function.
func AutomaticReply(m protocol.Message, model string, now time.Time, clockAllowed bool) Reply {
	if strings.ToUpper(model) != "CGDN1" || m.Protocol != "json" || now.Unix() < 1577836800 {
		return Reply{}
	}
	var fields map[string]any
	kind := ""
	switch stringField(m.Fields["type"]) {
	case "10":
		if id, ok := JSONInteger(m.Fields["id"]); !ok || id < 0 {
			return Reply{}
		}
		kind = "startup"
		fields = map[string]any{"type": "11", "code": 0, "desc": "", "dev_list": []string{"FFFFFFFFFFFF"}, "timestamp": now.Unix()}
	case "17":
		ack, _ := JSONInteger(m.Fields["need_ack"])
		id, valid := JSONInteger(m.Fields["id"])
		var groups []map[string]json.RawMessage
		if json.Unmarshal(m.Fields["sensorData"], &groups) != nil || len(groups) == 0 {
			return Reply{}
		}
		for _, group := range groups {
			if len(group) < 2 {
				return Reply{}
			}
			var stamp struct {
				Value json.RawMessage `json:"value"`
			}
			if json.Unmarshal(group["timestamp"], &stamp) != nil {
				return Reply{}
			}
			if n, ok := JSONInteger(stamp.Value); !ok || n < 0 {
				return Reply{}
			}
		}
		if ack != 1 || !valid || id < 0 {
			return Reply{}
		}
		kind = "history-ack"
		fields = map[string]any{"type": "18", "ack_id": id, "code": 0, "desc": "", "timestamp": now.Unix()}
	case "13":
		if !clockAllowed {
			return Reply{}
		}
		kind = "clock-compatibility"
		fields = map[string]any{"type": "18", "timestamp": now.Unix()}
	default:
		return Reply{}
	}
	payload, _ := json.Marshal(fields)
	return Reply{Payload: payload, Kind: kind}
}
