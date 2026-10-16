package device

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/danielskowronski/qiotlctl/internal/protocol"
)

func TestAutomaticReplies(t *testing.T) {
	now := time.Unix(1792000000, 0)
	for _, tc := range []struct {
		raw, model, kind string
		clock            bool
	}{
		{`{"type":"10","id":null}`, "CGDN1", "", false},
		{`{"type":"17","need_ack":1,"id":123,"sensorData":[{}]}`, "CGDN1", "", false},
		{`{"type":"10","id":123}`, "CGDN1", "startup", false},
		{`{"type":17,"need_ack":1,"id":123,"sensorData":[{"timestamp":{"value":1},"co2":{"value":600}}]}`, "CGDN1", "history-ack", false},
		{`{"type":"17","need_ack":1,"id":123,"sensorData":"invalid"}`, "CGDN1", "", false},
		{`{"type":"17","need_ack":0,"id":123}`, "CGDN1", "", false},
		{`{"type":"17","need_ack":1}`, "CGDN1", "", false},
		{`{"type":"17","need_ack":1,"id":1.2}`, "CGDN1", "", false},
		{`{"type":"13","timestamp":1}`, "CGDN1", "clock-compatibility", true},
		{`{"type":"13","timestamp":1}`, "CGDN1", "", false},
		{`{"type":"12","timestamp":1}`, "CGDN1", "", true},
		{`{"type":"18","timestamp":1}`, "CGDN1", "", true},
		{`{"type":"25","id":1}`, "CGDN1", "", true},
		{`{"type":"10","id":1}`, "generic", "", true},
	} {
		m, err := protocol.Decode([]byte(tc.raw), "json")
		if err != nil {
			t.Fatal(err)
		}
		r := AutomaticReply(m, tc.model, now, tc.clock)
		if r.Kind != tc.kind {
			t.Fatal(tc, r)
		}
		if tc.kind != "" {
			var fields map[string]json.RawMessage
			if json.Unmarshal(r.Payload, &fields) != nil {
				t.Fatal(r)
			}
			stamp, _ := JSONInteger(fields["timestamp"])
			if stamp != now.Unix() {
				t.Fatal("fabricated time", r)
			}
			if tc.kind == "history-ack" {
				id, _ := JSONInteger(fields["ack_id"])
				if id != 123 {
					t.Fatal(r)
				}
			}
		}
		if len(AutomaticReply(m, tc.model, time.Unix(1, 0), true).Payload) > 0 {
			t.Fatal("sent epoch host time")
		}
	}
}
