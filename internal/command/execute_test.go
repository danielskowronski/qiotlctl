package command

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danielskowronski/qiotlctl/internal/config"
	"github.com/danielskowronski/qiotlctl/internal/domain"
	"github.com/danielskowronski/qiotlctl/internal/storage"
	"github.com/eclipse/paho.mqtt.golang/packets"
)

func peerHandshake(l net.Listener, deny bool) (net.Conn, error) {
	c, err := l.Accept()
	if err != nil {
		return nil, err
	}
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	fail := func(e error) (net.Conn, error) { c.Close(); return nil, e }
	p, err := packets.ReadPacket(c)
	if err != nil {
		return fail(err)
	}
	connect, ok := p.(*packets.ConnectPacket)
	if !ok || connect.WillFlag || !connect.CleanSession {
		return fail(errors.New("unexpected CONNECT"))
	}
	if err = packets.NewControlPacket(packets.Connack).Write(c); err != nil {
		return fail(err)
	}
	p, err = packets.ReadPacket(c)
	if err != nil {
		return fail(err)
	}
	sub, ok := p.(*packets.SubscribePacket)
	if !ok || len(sub.Topics) != 1 || sub.Topics[0] != "fixture/up" {
		return fail(errors.New("expected target uplink subscription only"))
	}
	// Retained settings arrive before SUBACK and must never complete the command.
	if err = peerPublish(c, `{"type":"28","setting":{"report_interval":111}}`, true, false); err != nil {
		return fail(err)
	}
	_ = c.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	if _, e := packets.ReadPacket(c); e == nil {
		return fail(errors.New("client sent a packet before SUBACK"))
	} else if n, ok := e.(net.Error); !ok || !n.Timeout() {
		return fail(e)
	}
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	ack := packets.NewControlPacket(packets.Suback).(*packets.SubackPacket)
	ack.MessageID = sub.MessageID
	ack.ReturnCodes = []byte{1}
	if deny {
		ack.ReturnCodes[0] = 0x80
	}
	if err = ack.Write(c); err != nil {
		return fail(err)
	}
	return c, nil
}
func peerPublish(c net.Conn, payload string, retained, duplicate bool) error {
	p := packets.NewControlPacket(packets.Publish).(*packets.PublishPacket)
	p.TopicName = "fixture/up"
	p.Payload = []byte(payload)
	p.Retain = retained
	p.Dup = duplicate
	return p.Write(c)
}
func peerCommand(c net.Conn) (*packets.PublishPacket, error) {
	p, err := packets.ReadPacket(c)
	if err != nil {
		return nil, err
	}
	pub, ok := p.(*packets.PublishPacket)
	if !ok || pub.TopicName != "fixture/down" || pub.Retain {
		return nil, fmt.Errorf("unexpected command %T", p)
	}
	if pub.Qos == 1 {
		ack := packets.NewControlPacket(packets.Puback).(*packets.PubackPacket)
		ack.MessageID = pub.MessageID
		err = ack.Write(c)
	}
	return pub, err
}
func peerUntilDisconnect(c net.Conn) error {
	for {
		p, err := packets.ReadPacket(c)
		if err != nil {
			return err
		}
		switch p.(type) {
		case *packets.DisconnectPacket:
			return nil
		case *packets.PubackPacket:
		case *packets.PingreqPacket:
			if err = packets.NewControlPacket(packets.Pingresp).Write(c); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unexpected extra packet %T", p)
		}
	}
}
func TestExecuteSession(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		req                      Request
		response                 string
		status                   string
		code                     int
		deny, disconnect, strict bool
	}{
		{name: "settings", req: Request{Operation: "settings-get"}, response: `{"type":"28","setting":{"report_interval":900,"undocumented":7}}`, status: "verified"},
		{name: "ack", req: Request{Operation: "raw", Payload: []byte(`{"type":"17","id":12345,"setting":{}}`), WaitACK: true}, response: `{"type":"18","ack_id":12345,"code":0}`, status: "acknowledged"},
		{name: "rejected", req: Request{Operation: "raw", Payload: []byte(`{"type":"17","id":12345,"setting":{}}`), WaitACK: true}, response: `{"type":"18","ack_id":12345,"code":5}`, status: "rejected", code: 6},
		{name: "timeout", req: Request{Operation: "settings-get"}, status: "unknown", code: 5},
		{name: "lost", req: Request{Operation: "settings-get"}, status: "unknown", code: 4, disconnect: true},
		{name: "denied", req: Request{Operation: "settings-get"}, status: "failed", code: 4, deny: true},
		{name: "published", req: Request{Operation: "sync-time"}, status: "published"},
		{name: "strict", req: Request{Operation: "sync-time"}, status: "published", code: 7, strict: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer l.Close()
			c, err := config.Load(nil)
			if err != nil {
				t.Fatal(err)
			}
			d := fixture("CGDN1")
			c.Devices = map[string]config.Device{"F": d}
			c.MQTT.URL = "tcp://" + l.Addr().String()
			c.Storage.Path = filepath.Join(t.TempDir(), "journal.sqlite")
			c.MQTT.ConnectTimeout = config.Duration(time.Second)
			// Even an opted-in daemon config cannot turn a one-shot command into a responder.
			c.Runtime.Mode = "respond"
			peer := make(chan error, 1)
			go func() {
				conn, e := peerHandshake(l, tc.deny)
				if e != nil {
					peer <- e
					return
				}
				defer conn.Close()
				if tc.deny {
					peer <- peerUntilDisconnect(conn)
					return
				}
				_, e = peerCommand(conn)
				if e != nil {
					peer <- e
					return
				}
				if tc.disconnect {
					peer <- nil
					return
				}
				if tc.response != "" {
					// Other controller ACKs, startup, duplicate and retained replies precede ours.
					for _, raw := range []string{`{"type":"18","ack_id":54321,"code":0}`, `{"id":3,"type":"10"}`} {
						if e = peerPublish(conn, raw, false, false); e != nil {
							peer <- e
							return
						}
					}
					if e = peerPublish(conn, tc.response, true, false); e == nil {
						e = peerPublish(conn, tc.response, false, true)
					}
					if e == nil {
						time.Sleep(20 * time.Millisecond)
						e = peerPublish(conn, tc.response, false, false)
					}
					if e != nil {
						peer <- e
						return
					}
				}
				peer <- peerUntilDisconnect(conn)
			}()
			result, err := Execute(context.Background(), c, d, tc.req, Options{Timeout: 500 * time.Millisecond, RequireConfirmation: tc.strict})
			if result.Status != tc.status || ExitCode(err) != tc.code {
				t.Fatalf("%+v err=%v exit=%d", result, err, ExitCode(err))
			}
			if e := <-peer; e != nil {
				t.Fatal(e)
			}
			if result.FinishedAt == nil {
				t.Fatal("missing completion timestamp")
			}
			if tc.name == "settings" {
				if result.Response.Settings["upload_interval_seconds"] != float64(900) {
					t.Fatal(result.Response)
				}
				var outbound, inbound int
				err = storage.Replay(context.Background(), c.Storage.Path, storage.ReplayOptions{}, func(cap domain.Capture) error {
					if cap.Direction == domain.ToDevice {
						outbound++
					} else {
						inbound++
					}
					return nil
				})
				if err != nil || outbound != 1 || inbound < 3 {
					t.Fatal(outbound, inbound, err)
				}
				db, e := sql.Open("sqlite", c.Storage.Path)
				if e != nil {
					t.Fatal(e)
				}
				defer db.Close()
				var n int
				if e = db.QueryRow("SELECT count(*) FROM command_events WHERE operation_id=?", result.OperationID).Scan(&n); e != nil || n != 4 {
					t.Fatal(n, e)
				}
			}
		})
	}
}

func TestWaitForUplinkAndLocalSerialization(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	cfg, err := config.Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	d := fixture("CGDN1")
	cfg.Devices = map[string]config.Device{"F": d}
	cfg.MQTT.URL = "tcp://" + l.Addr().String()
	ready := make(chan struct{})
	release := make(chan struct{})
	peer := make(chan error, 1)
	go func() {
		c, e := peerHandshake(l, false)
		if e != nil {
			peer <- e
			return
		}
		defer c.Close()
		close(ready)
		<-release
		// No publication is allowed while waiting for a fresh uplink.
		_ = c.SetReadDeadline(time.Now().Add(30 * time.Millisecond))
		if _, e = packets.ReadPacket(c); e == nil {
			peer <- errors.New("published before fresh uplink")
			return
		}
		_ = c.SetDeadline(time.Now().Add(5 * time.Second))
		if e = peerPublish(c, `{"type":"13","timestamp":1792000000}`, false, false); e != nil {
			peer <- e
			return
		}
		if _, e = peerCommand(c); e != nil {
			peer <- e
			return
		}
		peer <- peerUntilDisconnect(c)
	}()
	result := make(chan error, 1)
	go func() {
		r, e := Execute(context.Background(), cfg, d, Request{Operation: "sync-time"}, Options{Timeout: 2 * time.Second, WaitForUplink: true})
		if e == nil && r.Status != "published" {
			e = fmt.Errorf("unexpected status %s", r.Status)
		}
		result <- e
	}()
	select {
	case <-ready:
	case <-time.After(3 * time.Second):
		t.Fatal("session never subscribed")
	}
	r, e := Execute(context.Background(), cfg, d, Request{Operation: "settings-get"}, Options{Timeout: time.Second})
	close(release)
	if ExitCode(e) != 8 || r.AttemptedAt != nil {
		t.Fatal(r, e)
	}
	if e = <-result; e != nil {
		t.Fatal(e)
	}
	if e = <-peer; e != nil {
		t.Fatal(e)
	}
}

func TestCommandIgnoresDaemonExportersAndHonorsJournalCapacity(t *testing.T) {
	for _, limited := range []bool{false, true} {
		t.Run(fmt.Sprint(limited), func(t *testing.T) {
			l, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer l.Close()
			c, err := config.Load(nil)
			if err != nil {
				t.Fatal(err)
			}
			d := fixture("CGDN1")
			c.Devices = map[string]config.Device{"F": d}
			c.MQTT.URL = "tcp://" + l.Addr().String()
			c.Outputs.HomeAssistant.Enabled = true
			c.Outputs.VictoriaMetrics.Enabled = true
			c.Storage.Path = ""
			if limited {
				c.Storage.Path = filepath.Join(t.TempDir(), "journal.sqlite")
				c.Storage.MaxBytes = 1
			}
			peer := make(chan error, 1)
			go func() {
				conn, e := peerHandshake(l, false)
				if e != nil {
					peer <- e
					return
				}
				defer conn.Close()
				if !limited {
					if _, e = peerCommand(conn); e != nil {
						peer <- e
						return
					}
				}
				peer <- peerUntilDisconnect(conn)
			}()
			r, err := Execute(context.Background(), c, d, Request{Operation: "sync-time"}, Options{Timeout: time.Second})
			if limited {
				if err == nil || !strings.Contains(err.Error(), "capacity") || r.AttemptedAt != nil {
					t.Fatal(r, err)
				}
			} else if err != nil || r.Status != "published" {
				t.Fatal(r, err)
			}
			if err := <-peer; err != nil {
				t.Fatal(err)
			}
		})
	}
}
