package command

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/danielskowronski/qiotlctl/internal/config"
	"github.com/danielskowronski/qiotlctl/internal/protocol"
	"github.com/danielskowronski/qiotlctl/internal/settings"
)

func TestApplyJSONWorkflow(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		force, missing, reject bool
		experimental           bool
		writes                 int
		status                 string
		exit                   int
	}{
		{name: "unchanged", status: "unchanged"},
		{name: "change", writes: 1, status: "verified"},
		{name: "experimental", experimental: true, writes: 1, status: "verified"},
		{name: "force", force: true, writes: 1, status: "verified"},
		{name: "missing", writes: 1, missing: true, status: "partial", exit: 7},
		{name: "reject", writes: 1, reject: true, status: "rejected", exit: 6},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l, e := net.Listen("tcp", "127.0.0.1:0")
			if e != nil {
				t.Fatal(e)
			}
			defer l.Close()
			limits := settings.Options{IgnoreOfficialLimits: tc.experimental}
			c, e := config.Load(nil, limits)
			if e != nil {
				t.Fatal(e)
			}
			d := fixture("CGDN1")
			var desired any = "24h"
			wire := 0
			if tc.experimental {
				desired, wire = 2, 2
			}
			d.Settings = settings.Values{"display": map[string]any{"clock_format": desired}}
			c.Devices = map[string]config.Device{"F": d}
			c.MQTT.URL = "tcp://" + l.Addr().String()
			patch, e := settings.Build("json", d.Settings, limits)
			if e != nil {
				t.Fatal(e)
			}
			sessions := 1
			if tc.writes > 0 {
				sessions = 3
			}
			if tc.force || tc.reject {
				sessions--
			}
			peer := make(chan error, 1)
			go func() {
				writes := 0
				for i := 0; i < sessions; i++ {
					conn, e := peerHandshake(l, false)
					if e != nil {
						peer <- e
						return
					}
					pub, e := peerCommand(conn)
					if e != nil {
						conn.Close()
						peer <- e
						return
					}
					var msg map[string]json.RawMessage
					if e = json.Unmarshal(pub.Payload, &msg); e != nil {
						conn.Close()
						peer <- e
						return
					}
					var typ string
					_ = json.Unmarshal(msg["type"], &typ)
					response := fmt.Sprintf(`{"type":"28","setting":{"is_12_hour_mode":%d}}`, wire)
					switch typ {
					case "28":
						if writes == 0 && tc.name != "unchanged" {
							response = `{"type":"28","setting":{"is_12_hour_mode":1}}`
						}
						if tc.missing && writes > 0 {
							response = `{"type":"28","setting":{"report_interval":60}}`
						}
					case "17":
						var actual map[string]int
						if e = json.Unmarshal(msg["setting"], &actual); e != nil || actual["is_12_hour_mode"] != wire {
							conn.Close()
							peer <- errors.New("wrong setting value published")
							return
						}
						writes++
						var id int64
						_ = json.Unmarshal(msg["id"], &id)
						code := 0
						if tc.reject {
							code = 4
						}
						b, _ := json.Marshal(map[string]any{"type": "18", "ack_id": id, "code": code})
						response = string(b)
					default:
						conn.Close()
						peer <- errors.New("unexpected message")
						return
					}
					if e = peerPublish(conn, response, false, false); e == nil {
						e = peerUntilDisconnect(conn)
					}
					conn.Close()
					if e != nil {
						peer <- e
						return
					}
				}
				if writes != tc.writes {
					peer <- errors.New("incorrect write count")
					return
				}
				peer <- nil
			}()
			r, e := Apply(context.Background(), c, d, patch, ApplyOptions{Options: Options{Timeout: time.Second, RequireConfirmation: true}, Force: tc.force, Readback: true})
			if r.Status != tc.status || ExitCode(e) != tc.exit {
				t.Fatalf("%+v %v", r, e)
			}
			if e = <-peer; e != nil {
				t.Fatal(e)
			}
		})
	}
}

func TestApplyHEXSilentThenExplicitProbe(t *testing.T) {
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer l.Close()
	c, e := config.Load(nil)
	if e != nil {
		t.Fatal(e)
	}
	d := fixture("CGP22C")
	c.Devices = map[string]config.Device{"F": d}
	c.MQTT.URL = "tcp://" + l.Addr().String()
	p, e := settings.Build("hex", settings.Values{"display": map[string]any{"clock_format": "24h"}})
	if e != nil {
		t.Fatal(e)
	}
	peer := make(chan error, 1)
	go func() {
		for i := 0; i < 2; i++ {
			conn, e := peerHandshake(l, false)
			if e != nil {
				peer <- e
				return
			}
			pub, e := peerCommand(conn)
			if e != nil {
				conn.Close()
				peer <- e
				return
			}
			m, e := protocol.Decode(pub.Payload, "hex")
			if e != nil || len(m.Entries) != 1 {
				conn.Close()
				peer <- errors.New("not a single-property write")
				return
			}
			want := byte(0x3e)
			if i == 1 {
				want = 0x04
			}
			if m.Entries[0].Key != want {
				conn.Close()
				peer <- errors.New("wrong property/order")
				return
			}
			if i == 1 {
				b, e := hex.DecodeString("434739090004020005003e0100001601")
				if e != nil {
					panic(e)
				}
				e = peerPublish(conn, string(b), false, false)
				if e != nil {
					conn.Close()
					peer <- e
					return
				}
			}
			e = peerUntilDisconnect(conn)
			conn.Close()
			if e != nil {
				peer <- e
				return
			}
		}
		peer <- nil
	}()
	r, e := Apply(context.Background(), c, d, p, ApplyOptions{Options: Options{Timeout: time.Second, RequireConfirmation: true}, ReadbackUpload: 5 * time.Minute})
	if e != nil || r.Status != "verified" || len(r.Operations) != 2 || r.Operations[0].Status != "published" || r.Fields[0].Status != "matched" {
		t.Fatalf("%+v %v", r, e)
	}
	if e = <-peer; e != nil {
		t.Fatal(e)
	}
}

func TestCheckNeverAppliesPatch(t *testing.T) {
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer l.Close()
	c, e := config.Load(nil)
	if e != nil {
		t.Fatal(e)
	}
	d := fixture("CGDN1")
	c.Devices = map[string]config.Device{"F": d}
	c.MQTT.URL = "tcp://" + l.Addr().String()
	p, e := settings.Build("json", settings.Values{"display": map[string]any{"clock_format": "12h"}})
	if e != nil {
		t.Fatal(e)
	}
	peer := make(chan error, 1)
	go func() {
		conn, e := peerHandshake(l, false)
		if e != nil {
			peer <- e
			return
		}
		defer conn.Close()
		pub, e := peerCommand(conn)
		if e != nil {
			peer <- e
			return
		}
		if string(pub.Payload) != `{"type":"28"}` {
			peer <- errors.New("check published a settings write")
			return
		}
		if e = peerPublish(conn, `{"type":"28","setting":{"is_12_hour_mode":0}}`, false, false); e == nil {
			e = peerUntilDisconnect(conn)
		}
		peer <- e
	}()
	r, e := Check(context.Background(), c, d, p, ApplyOptions{Options: Options{Timeout: time.Second}})
	if ExitCode(e) != 7 || r.Status != "drift_or_unknown" || r.Fields[0].Status != "different" {
		t.Fatal(r, e)
	}
	if e = <-peer; e != nil {
		t.Fatal(e)
	}
}

func TestCheckProbeUsesCurrentInterval(t *testing.T) {
	p, e := settings.Build("hex", settings.Values{"reporting": map[string]any{"upload_interval": "10m"}})
	if e != nil {
		t.Fatal(e)
	}
	opt := ApplyOptions{ReadbackUpload: 5 * time.Minute}
	if e = ValidateCheck(p, opt); e != nil {
		t.Fatal("checking desired 10m using known current 5m should be possible", e)
	}
	if e = ValidateApply(p, opt); e == nil {
		t.Fatal("applying 10m then probing with 5m would undo the requested setting")
	}
	opt = ApplyOptions{ReadbackUploadSet: true}
	if e = ValidateCheck(p, opt); e == nil {
		t.Fatal("zero probe requires opt-in")
	}
	opt.SettingsOptions.IgnoreOfficialLimits = true
	if e = ValidateCheck(p, opt); e != nil {
		t.Fatal("explicit zero probe should be representable with opt-in", e)
	}
	if e = ValidateApply(p, opt); e == nil {
		t.Fatal("experimental probe must not undo requested interval")
	}
	probe, e := ProbePatch(0, opt.SettingsOptions)
	if e != nil || hex.EncodeToString(probe.Fields[0].HEX.Value) != "0000" {
		t.Fatal("zero probe lost", probe, e)
	}
}
