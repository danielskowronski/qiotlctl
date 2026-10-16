package observer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/danielskowronski/qiotlctl/internal/config"
	"github.com/danielskowronski/qiotlctl/internal/domain"
	"github.com/danielskowronski/qiotlctl/internal/output"
	"github.com/danielskowronski/qiotlctl/internal/storage"
	"github.com/eclipse/paho.mqtt.golang/packets"
)

func TestBlockedVMDoesNotDelayHistoryAck(t *testing.T) {
	requestStarted := make(chan struct{}, 1)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case requestStarted <- struct{}{}:
		default:
		}
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer func() { close(release); server.Close() }()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	c := testConfig(t, listener.Addr().String())
	c.Storage.Path = filepath.Join(t.TempDir(), "queue.sqlite")
	c.Outputs.VictoriaMetrics.Enabled = true
	c.Outputs.VictoriaMetrics.ImportEndpoint = server.URL + "/api/v1/import"
	c.Outputs.VictoriaMetrics.Timeout = config.Duration(30 * time.Second)
	d := c.Devices["fixture"]
	enabled := true
	d.Responses.Enabled = &enabled
	c.Devices["fixture"] = d
	// Pending work starts an HTTP request before MQTT telemetry arrives.
	recorder, err := storage.OpenRecorder(c.Storage.Path, false)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	e := domain.Event{DeviceID: d.ID, Alias: d.Alias, Model: d.Model, Protocol: "json", Direction: domain.FromDevice, ReceivedAt: now, Samples: []domain.Sample{{Timestamp: now.Unix() - 60, Readings: []domain.Reading{{Sensor: "co2", Value: 500, Unit: "ppm", Quality: "normal"}}}}}
	_, err = recorder.Record(context.Background(), domain.Capture{ReceivedAt: now}, e, output.NewStore(recorder, c).CaptureHook(context.Background(), e))
	recorder.Close()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	peer := make(chan error, 1)
	go func() {
		conn, err := handshake(listener, false)
		if err != nil {
			peer <- err
			cancel()
			return
		}
		defer conn.Close()
		select {
		case <-requestStarted:
		case <-ctx.Done():
			peer <- ctx.Err()
			return
		}
		p := packets.NewControlPacket(packets.Publish).(*packets.PublishPacket)
		p.TopicName = "fixture/up"
		p.Payload = []byte(fmt.Sprintf(`{"type":"17","id":77,"need_ack":1,"sensorData":[{"timestamp":{"value":%d},"co2":{"value":650}}]}`, now.Unix()))
		if err = p.Write(conn); err != nil {
			peer <- err
			cancel()
			return
		}
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		packet, err := packets.ReadPacket(conn)
		if err == nil {
			pub, ok := packet.(*packets.PublishPacket)
			if !ok {
				err = fmt.Errorf("expected ACK publication, got %T", packet)
			} else {
				var fields map[string]any
				err = json.Unmarshal(pub.Payload, &fields)
				if err == nil && fields["ack_id"] != float64(77) {
					err = fmt.Errorf("wrong ACK: %s", pub.Payload)
				}
			}
		}
		peer <- err
		cancel()
	}()
	stats := &Stats{}
	if err = Run(ctx, c, stats, nil); err != nil {
		t.Fatal(err)
	}
	if err = <-peer; err != nil {
		t.Fatal(err)
	}
	if stats.Replies.Load() != 1 {
		t.Fatal("missing ACK")
	}
	db, err := storage.OpenReadOnly(c.Storage.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var pending int
	if err = db.QueryRow(`SELECT count(*) FROM vm_samples WHERE delivered=0`).Scan(&pending); err != nil || pending != 2 {
		t.Fatal(pending, err)
	}
}
