package observer

import (
	"context"
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

func testConfig(t *testing.T, address string) config.Config {
	t.Helper()
	c, err := config.Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	c.MQTT.URL = "mqtt://" + address
	c.MQTT.ConnectTimeout = config.Duration(time.Second)
	c.MQTT.ReconnectMax = config.Duration(time.Second)
	c.Devices = map[string]config.Device{"fixture": {ID: "fixture", Alias: "fixture", Model: "CGDN1", Protocol: "json", Topics: config.Topics{Telemetry: "fixture/up", Commands: "fixture/down"}}}
	return c
}
func handshake(l net.Listener, denied bool) (net.Conn, error) {
	c, err := l.Accept()
	if err != nil {
		return nil, err
	}
	_ = c.SetDeadline(time.Now().Add(8 * time.Second))
	fail := func(err error) (net.Conn, error) { c.Close(); return nil, err }
	p, err := packets.ReadPacket(c)
	if err != nil {
		return fail(err)
	}
	connect, ok := p.(*packets.ConnectPacket)
	if !ok || connect.WillFlag {
		return fail(errors.New("expected CONNECT without publish will"))
	}
	if err = packets.NewControlPacket(packets.Connack).Write(c); err != nil {
		return fail(err)
	}
	p, err = packets.ReadPacket(c)
	if err != nil {
		return fail(err)
	}
	sub, ok := p.(*packets.SubscribePacket)
	if !ok || len(sub.Topics) != 2 {
		return fail(errors.New("expected both exact device subscriptions"))
	}
	topics := map[string]bool{}
	for _, topic := range sub.Topics {
		topics[topic] = true
	}
	if !topics["fixture/up"] || !topics["fixture/down"] {
		return fail(errors.New("wrong topic filters"))
	}
	ack := packets.NewControlPacket(packets.Suback).(*packets.SubackPacket)
	ack.MessageID = sub.MessageID
	ack.ReturnCodes = []byte{1, 1}
	if denied {
		ack.ReturnCodes[0] = 0x80
	}
	if err = ack.Write(c); err != nil {
		return fail(err)
	}
	return c, nil
}
func publish(c net.Conn, id uint16, qos byte) error {
	p := packets.NewControlPacket(packets.Publish).(*packets.PublishPacket)
	p.Qos = qos
	p.Retain = true
	p.Dup = true
	p.MessageID = id
	p.TopicName = "fixture/up"
	p.Payload = []byte(`{"type":"17","need_ack":1,"sensorData":[{"timestamp":{"value":1792000000},"co2":{"value":600,"status":0}}]}`)
	return p.Write(c)
}
func expectOnlyTransport(c net.Conn, untilAck bool) error {
	for {
		p, err := packets.ReadPacket(c)
		if err != nil {
			if !untilAck {
				return nil
			}
			return err
		}
		switch p.(type) {
		case *packets.PubackPacket:
			if untilAck {
				return nil
			}
		case *packets.PingreqPacket:
			if err = packets.NewControlPacket(packets.Pingresp).Write(c); err != nil {
				return err
			}
		case *packets.DisconnectPacket:
			return nil
		default:
			return fmt.Errorf("observer sent unexpected %T (application PUBLISH is forbidden)", p)
		}
	}
}
func TestRunReconnectAndNoPublish(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	peer := make(chan error, 1)
	go func() {
		for i := 0; i < 2; i++ {
			c, e := handshake(l, false)
			if e != nil {
				peer <- e
				return
			}
			if e = publish(c, uint16(i+1), 1); e == nil {
				e = expectOnlyTransport(c, true)
			}
			if e != nil {
				c.Close()
				peer <- e
				return
			}
			if i == 0 {
				c.Close()
				continue
			}
			e = expectOnlyTransport(c, false)
			c.Close()
			peer <- e
		}
	}()
	c := testConfig(t, l.Addr().String())
	c.Storage.Path = filepath.Join(t.TempDir(), "capture.sqlite")
	s := &Stats{}
	count := 0
	err = Run(ctx, c, s, func(e domain.Event) error {
		count++
		if e.Error != "" || !e.ResponseRequired || len(e.Samples) != 1 {
			return fmt.Errorf("unexpected decoded event: %+v", e)
		}
		if count == 2 {
			cancel()
		}
		return nil
	})
	if err != nil || count != 2 || s.Reconnects.Load() != 2 || s.Recorded.Load() != 2 {
		t.Fatalf("run: count=%d reconnects=%d err=%v", count, s.Reconnects.Load(), err)
	}
	if err = <-peer; err != nil {
		t.Fatal(err)
	}
	if s.Ready() {
		t.Fatal("ready after disconnect")
	}
	n := 0
	err = storage.Replay(context.Background(), c.Storage.Path, storage.ReplayOptions{}, func(c domain.Capture) error {
		n++
		if c.QoS == nil || *c.QoS != 1 || c.Retained == nil || !*c.Retained || c.Duplicate == nil || !*c.Duplicate {
			return errors.New("delivery metadata lost")
		}
		return nil
	})
	if err != nil || n != 2 {
		t.Fatal(n, err)
	}
}
func TestSubscriptionDeniedRecovers(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c := testConfig(t, l.Addr().String())
	s := &Stats{}
	s.InitHealth(c)
	peer := make(chan error, 1)
	go func() {
		conn, e := handshake(l, true)
		if e != nil {
			peer <- e
			return
		}
		defer conn.Close()
		packet, e := packets.ReadPacket(conn)
		if e != nil {
			peer <- e
			return
		}
		sub, ok := packet.(*packets.SubscribePacket)
		if !ok {
			peer <- fmt.Errorf("expected subscription retry, got %T", packet)
			return
		}
		failed := false
		for _, check := range s.Diagnostics.Snapshot(time.Now()).Checks {
			if check.ID == "mqtt.subscriptions" && check.Blocking && strings.Contains(check.Reason, "denied") {
				failed = true
			}
		}
		if !failed {
			peer <- errors.New("missing subscription denial diagnostic")
			cancel()
			return
		}
		ack := packets.NewControlPacket(packets.Suback).(*packets.SubackPacket)
		ack.MessageID, ack.ReturnCodes = sub.MessageID, []byte{1, 1}
		if e = ack.Write(conn); e == nil {
			e = publish(conn, 1, 1)
		}
		if e == nil {
			e = expectOnlyTransport(conn, false)
		}
		peer <- e
	}()
	err = Run(ctx, c, s, func(domain.Event) error { cancel(); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if err = <-peer; err != nil {
		t.Fatal(err)
	}
	if s.Reconnects.Load() != 1 || s.Received.Load() != 1 {
		t.Fatal("ACL recovery did not reuse connection", s.Reconnects.Load(), s.Received.Load())
	}
}
func TestQueueOverflowStopsObserver(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s := &Stats{}
	entered := make(chan struct{})
	release := make(chan struct{})
	peer := make(chan error, 1)
	go func() {
		c, e := handshake(l, false)
		if e != nil {
			peer <- e
			return
		}
		defer c.Close()
		if e = publish(c, 0, 0); e != nil {
			peer <- e
			return
		}
		select {
		case <-entered:
		case <-ctx.Done():
			peer <- ctx.Err()
			return
		}
		for i := 0; i < 50; i++ {
			if e = publish(c, 0, 0); e != nil {
				break
			}
		}
		peer <- expectOnlyTransport(c, false)
	}()
	go func() {
		defer close(release)
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if s.QueueOverflows.Load() > 0 {
					return
				}
			}
		}
	}()
	c := testConfig(t, l.Addr().String())
	c.Runtime.QueueSize = 1
	first := true
	err = Run(ctx, c, s, func(domain.Event) error {
		if first {
			first = false
			close(entered)
			<-release
		}
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "overflow") || s.QueueOverflows.Load() == 0 {
		t.Fatal(err)
	}
	if err = <-peer; err != nil {
		t.Fatal(err)
	}
}
func TestCancelDuringInitialRetry(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	err = Run(ctx, testConfig(t, addr), &Stats{}, nil)
	if err != nil || time.Since(started) > 2*time.Second {
		t.Fatal("unbounded shutdown", err)
	}
}
