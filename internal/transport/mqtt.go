// Package transport shares MQTT connection options between observers and commands.
package transport

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"github.com/danielskowronski/qiotlctl/internal/config"
	paho "github.com/eclipse/paho.mqtt.golang"
	"net"
	"net/url"
	"os"
	"time"
)

func Options(c config.MQTT) (*paho.ClientOptions, error) {
	u, err := url.Parse(c.URL)
	if err != nil {
		return nil, errors.New("invalid MQTT URL")
	}
	switch u.Scheme {
	case "mqtt":
		u.Scheme = "tcp"
	case "mqtts", "tls":
		u.Scheme = "ssl"
	}
	if u.Port() == "" {
		port := "1883"
		if u.Scheme == "ssl" {
			port = "8883"
		}
		if u.Scheme == "ws" {
			port = "80"
		}
		if u.Scheme == "wss" {
			port = "443"
		}
		u.Host = net.JoinHostPort(u.Hostname(), port)
	}
	// Fix MQTT 3.1.1 so a rejected CONNACK is not obscured by a second
	// automatic MQTT 3.1 negotiation attempt.
	options := paho.NewClientOptions().SetProtocolVersion(4).AddBroker(u.String()).SetUsername(c.Username).SetPassword(c.Password).SetCleanSession(true).SetAutoReconnect(true).SetConnectRetry(true).SetConnectRetryInterval(time.Second).SetConnectTimeout(c.ConnectTimeout.Duration()).SetMaxReconnectInterval(c.ReconnectMax.Duration()).SetKeepAlive(c.KeepAlive.Duration()).SetOrderMatters(true).SetWriteTimeout(5 * time.Second)
	clientID := c.ClientID
	if clientID == "" {
		var suffix [8]byte
		if _, err = rand.Read(suffix[:]); err != nil {
			return nil, err
		}
		clientID = "qiotl-" + hex.EncodeToString(suffix[:])
	}
	options.SetClientID(clientID)
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: c.TLS.ServerName}
	if c.TLS.CAFile != "" {
		b, err := os.ReadFile(c.TLS.CAFile)
		if err != nil {
			return nil, err
		}
		pool, err := x509.SystemCertPool()
		if err != nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(b) {
			return nil, errors.New("TLS CA file contains no certificates")
		}
		tlsCfg.RootCAs = pool
	}
	if c.TLS.CertFile != "" {
		cert, err := tls.LoadX509KeyPair(c.TLS.CertFile, c.TLS.KeyFile)
		if err != nil {
			return nil, errors.New("cannot load TLS client certificate/key")
		}
		tlsCfg.Certificates = []tls.Certificate{cert}
	}
	options.SetTLSConfig(tlsCfg)
	return options, nil
}

// Wait observes cancellation without exposing potentially credential-bearing broker errors.
func Wait(ctx context.Context, token paho.Token, operation string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-token.Done():
		if token.Error() != nil {
			return errors.New("MQTT " + operation + " failed")
		}
	}
	if sub, ok := token.(*paho.SubscribeToken); ok {
		for _, qos := range sub.Result() {
			if qos == 0x80 {
				return errors.New("broker denied a configured subscription")
			}
		}
	}
	return nil
}

// Publish bounds both Paho's synchronous enqueue and token completion. A timeout
// has an unknown delivery outcome; callers must never retry it automatically.
func Publish(ctx context.Context, client paho.Client, topic string, qos byte, payload []byte) error {
	done := make(chan error, 1)
	go func() { done <- Wait(ctx, client.Publish(topic, qos, false, payload), "publication") }()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-done:
		return err
	}
}
