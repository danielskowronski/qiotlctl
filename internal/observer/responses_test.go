package observer

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/danielskowronski/qiotlctl/internal/config"
	"github.com/danielskowronski/qiotlctl/internal/device"
	"github.com/danielskowronski/qiotlctl/internal/domain"
	"github.com/eclipse/paho.mqtt.golang/packets"
)

func TestResponderOptInAndPersistence(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("enabled=%v", enabled), func(t *testing.T) {
			l, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer l.Close()
			c := testConfig(t, l.Addr().String())
			c.Storage.Path = filepath.Join(t.TempDir(), "journal.sqlite")
			d := c.Devices["fixture"]
			d.Responses.Enabled = &enabled
			d.Responses.Clock = "type18-heartbeat"
			c.Devices["fixture"] = d
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			peer := make(chan error, 1)
			go func() {
				conn, e := handshake(l, false)
				if e != nil {
					peer <- e
					return
				}
				defer conn.Close()
				send := func(raw string, retain, dup bool, topic string) error {
					p := packets.NewControlPacket(packets.Publish).(*packets.PublishPacket)
					p.Payload = []byte(raw)
					p.TopicName = topic
					p.Retain = retain
					p.Dup = dup
					return p.Write(conn)
				}
				for _, item := range []struct {
					raw, topic  string
					retain, dup bool
				}{
					{`{"type":"10","id":1}`, "fixture/up", true, false},
					{`{"type":"10","id":2}`, "fixture/down", false, false},
					{`{"type":"17","id":3,"need_ack":1,"sensorData":[{"timestamp":{"value":1},"co2":{"value":600}}]}`, "fixture/up", false, true},
					{`{"type":"13","timestamp":1}`, "fixture/up", false, false},
					{`{"type":"13","timestamp":1}`, "fixture/up", false, false},
				} {
					if e = send(item.raw, item.retain, item.dup, item.topic); e != nil {
						peer <- e
						return
					}
				}
				if !enabled {
					peer <- expectOnlyTransport(conn, false)
					return
				}
				p, e := packets.ReadPacket(conn)
				if e != nil {
					peer <- e
					return
				}
				pub, ok := p.(*packets.PublishPacket)
				if !ok || pub.Retain || pub.Qos != 0 || pub.TopicName != "fixture/down" {
					peer <- fmt.Errorf("unexpected reply: %T", p)
					return
				}
				var fields map[string]json.RawMessage
				if json.Unmarshal(pub.Payload, &fields) != nil {
					peer <- errors.New("bad reply JSON")
					return
				}
				id, valid := device.JSONInteger(fields["ack_id"])
				if !valid || id != 3 {
					peer <- errors.New("ACK does not match history request")
					return
				}
				stamp, _ := device.JSONInteger(fields["timestamp"])
				if stamp < time.Now().Unix()-2 || stamp > time.Now().Unix()+2 {
					peer <- errors.New("reply time is not current")
					return
				}
				// Independent reader observes the inbound history committed before ACK.
				db, e := sql.Open("sqlite", c.Storage.Path)
				if e != nil {
					peer <- e
					return
				}
				defer db.Close()
				var count int
				e = db.QueryRow("SELECT count(*) FROM captures WHERE direction='from-device' AND json_extract(event_json,'$.message_type')='17'").Scan(&count)
				if e != nil || count != 1 {
					peer <- fmt.Errorf("ACK preceded persistence: %d %v", count, e)
					return
				}
				// The ACK already carried time: immediately following heartbeats send nothing.
				peer <- expectOnlyTransport(conn, false)
			}()
			stats := &Stats{}
			inbound := 0
			err = Run(ctx, c, stats, func(e domain.Event) error {
				if e.Topic == "fixture/up" {
					inbound++
				}
				if inbound == 4 {
					cancel()
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if err = <-peer; err != nil {
				t.Fatal(err)
			}
			want := uint64(0)
			if enabled {
				want = 1
			}
			if stats.Replies.Load() != want {
				t.Fatal(stats.Replies.Load())
			}
		})
	}
}

func TestClockRateLimitAndHostJump(t *testing.T) {
	now := time.Unix(1792000000, 0)
	d := config.Device{ID: "fixture", Responses: config.Responses{Clock: "type18-heartbeat", MinInterval: config.Duration(5 * time.Minute)}}
	r := responder{}
	if !r.clockAllowed(d, now) {
		t.Fatal("initial clock sync suppressed")
	}
	r.lastClock = map[string]time.Time{d.ID: now}
	for _, elapsed := range []time.Duration{-time.Hour, 0, time.Minute} {
		if r.clockAllowed(d, now.Add(elapsed)) {
			t.Fatal("clock workaround not rate limited", elapsed)
		}
	}
	if !r.clockAllowed(d, now.Add(5*time.Minute)) {
		t.Fatal("clock workaround never becomes due")
	}
	d.Responses.Clock = "off"
	if r.clockAllowed(d, now.Add(time.Hour)) {
		t.Fatal("clock workaround enabled implicitly")
	}
}

func TestStartupReplyAndStorageFailure(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		enabled, storageFailure bool
	}{
		{name: "startup-default-off"},
		{name: "startup-enabled", enabled: true},
		{name: "history-storage-failure", enabled: true, storageFailure: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer l.Close()
			cfg := testConfig(t, l.Addr().String())
			if tc.enabled {
				cfg.Runtime.Mode = "respond"
			}
			cfg.Storage.Path = filepath.Join(t.TempDir(), "capture.sqlite")
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			peer := make(chan error, 1)
			go func() {
				c, e := handshake(l, false)
				if e != nil {
					peer <- e
					return
				}
				defer c.Close()
				raw := `{"type":"10","id":456}`
				if tc.storageFailure {
					db, e := sql.Open("sqlite", cfg.Storage.Path)
					if e != nil {
						peer <- e
						return
					}
					_, e = db.Exec("CREATE TRIGGER fail_capture BEFORE INSERT ON captures BEGIN SELECT RAISE(ABORT,'fixture storage failure'); END")
					db.Close()
					if e != nil {
						peer <- e
						return
					}
					raw = `{"type":"17","id":456,"need_ack":1,"sensorData":[{"timestamp":{"value":1792000000},"co2":{"value":600}}]}`
				}
				p := packets.NewControlPacket(packets.Publish).(*packets.PublishPacket)
				p.TopicName = "fixture/up"
				p.Payload = []byte(raw)
				if e = p.Write(c); e != nil {
					peer <- e
					return
				}
				if tc.enabled && !tc.storageFailure {
					received, e := packets.ReadPacket(c)
					if e != nil {
						peer <- e
						return
					}
					reply, ok := received.(*packets.PublishPacket)
					if !ok || reply.Retain {
						peer <- errors.New("missing startup reply")
						return
					}
					var fields map[string]any
					if json.Unmarshal(reply.Payload, &fields) != nil || fields["type"] != "11" || fields["code"] != float64(0) {
						peer <- errors.New("incorrect startup reply")
						return
					}
				}
				peer <- expectOnlyTransport(c, false)
			}()
			stats := &Stats{}
			err = Run(ctx, cfg, stats, func(e domain.Event) error {
				if e.Direction == domain.FromDevice {
					cancel()
				}
				return nil
			})
			if tc.storageFailure {
				if err == nil || stats.StorageOK.Load() || stats.Replies.Load() != 0 {
					t.Fatal(err, stats.StorageOK.Load(), stats.Replies.Load())
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if e := <-peer; e != nil {
				t.Fatal(e)
			}
		})
	}
}
