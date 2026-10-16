// These tests require scripts/diagnostics-smoke.sh's isolated loopback EMQX.
package diagnostictest

import (
	"context"
	"net"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/danielskowronski/qiotlctl/internal/config"
	"github.com/danielskowronski/qiotlctl/internal/diagnostic"
	"github.com/danielskowronski/qiotlctl/internal/output"
	"github.com/danielskowronski/qiotlctl/internal/transport"
	paho "github.com/eclipse/paho.mqtt.golang"
)

func TestEMQXAuthorization(t *testing.T) {
	address := os.Getenv("QIOTLCTL_TEST_EMQX")
	if address == "" {
		t.Skip("run scripts/diagnostics-smoke.sh")
	}
	u, err := url.Parse(address)
	if err != nil || !net.ParseIP(u.Hostname()).IsLoopback() {
		t.Fatal("test requires a literal loopback address")
	}
	c, err := config.Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	c.MQTT.URL = address
	c.MQTT.ConnectTimeout = config.Duration(time.Second)
	topic := "homeassistant/sensor/qiotlctl_test/co2/config"
	payload := []byte(`{"name":"CO2","unique_id":"qiotlctl_test_co2","state_topic":"qiotlctl/test/co2/state"}`)
	expected := []output.Publication{{Topic: topic, Payload: payload}}
	publish := func(user string, retain bool, body []byte) {
		t.Helper()
		mc := c.MQTT
		mc.Username = user
		o, err := transport.Options(mc)
		if err != nil {
			t.Fatal(err)
		}
		o.SetAutoReconnect(false).SetConnectRetry(false)
		client := paho.NewClient(o)
		defer client.Disconnect(100)
		ctx, stop := context.WithTimeout(context.Background(), 2*time.Second)
		defer stop()
		if err := transport.InspectToken(ctx, client.Connect(), "connect"); err != nil {
			t.Fatal(err)
		}
		if err := transport.InspectToken(ctx, client.Publish(topic, 1, retain, body), "publish"); err != nil {
			t.Fatal(err)
		}
	}
	inspect := func(user, inspectTopic string) (map[string]transport.Observed, error) {
		mc := c.MQTT
		mc.Username = user
		ctx, stop := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer stop()
		return transport.Inspect(ctx, mc, []string{inspectTopic}, "")
	}
	publish("good", true, nil)
	// With MQTT 3.1.1 and deny_action=ignore EMQX acknowledges the forbidden
	// publish. Only a new retained subscription can distinguish its absence.
	publish("publish-denied", true, payload)
	m, err := inspect("good", topic)
	if err != nil {
		t.Fatal(err)
	}
	if status, _, counts := output.CheckDiscovery(expected, m); status != diagnostic.Failed || counts["missing"] != 1 {
		t.Fatal("silently discarded publish appeared healthy", status, counts)
	}
	if _, err = inspect("readback-denied", topic); err == nil || !strings.Contains(err.Error(), "subscribe denied") {
		t.Fatal("missing readback denial", err)
	}
	if _, err = inspect("birth-denied", "homeassistant/status"); err == nil || !strings.Contains(err.Error(), "homeassistant/status") {
		t.Fatal("missing birth denial", err)
	}
	publish("good", true, payload)
	m, err = inspect("good", topic)
	if err != nil {
		t.Fatal(err)
	}
	if status, _, _ := output.CheckDiscovery(expected, m); status != diagnostic.OK {
		t.Fatal("retained recovery failed", status)
	}
	publish("good", true, nil)
	c.MQTT.Username = "good"
	ctx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if _, err = transport.Inspect(ctx, c.MQTT, nil, "qiotlctl/diagnostics/test"); err != nil {
		t.Fatal("explicit nonce", err)
	}
}
