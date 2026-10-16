package transport

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"

	paho "github.com/eclipse/paho.mqtt.golang"
	"github.com/eclipse/paho.mqtt.golang/packets"
)

// NetworkReason classifies errors without exposing URLs, credentials or remote bodies.
func NetworkReason(err error) string {
	var dns *net.DNSError
	var op *net.OpError
	var cert *tls.CertificateVerificationError
	var unknown x509.UnknownAuthorityError
	var hostname x509.HostnameError
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "request timed out"
	case errors.Is(err, context.Canceled):
		return "request canceled"
	case errors.As(err, &cert), errors.As(err, &unknown), errors.As(err, &hostname):
		return "TLS certificate verification failed"
	case errors.As(err, &dns):
		return "DNS lookup failed"
	case errors.As(err, &op):
		if op.Timeout() {
			return "network request timed out"
		}
		return "network connection failed"
	}
	return "connection or protocol negotiation failed"
}

// InspectToken retains denied topic names but never includes the broker's raw error.
func InspectToken(ctx context.Context, token paho.Token, operation string) error {
	select {
	case <-ctx.Done():
		return fmt.Errorf("%s: %s", operation, NetworkReason(ctx.Err()))
	case <-token.Done():
	}
	if err := token.Error(); err != nil {
		// Paho's CONNACK errors have stable sentinel identities.
		if errors.Is(err, packets.ErrorRefusedNotAuthorised) || errors.Is(err, packets.ErrorRefusedBadUsernameOrPassword) {
			return fmt.Errorf("%s: MQTT authentication/authorization rejected", operation)
		}
		return fmt.Errorf("%s: %s", operation, NetworkReason(err))
	}
	if sub, ok := token.(*paho.SubscribeToken); ok {
		var denied []string
		for topic, qos := range sub.Result() {
			if qos == 0x80 {
				denied = append(denied, topic)
			}
		}
		if len(denied) > 0 {
			sort.Strings(denied)
			return fmt.Errorf("subscribe denied for %s", strings.Join(denied, ", "))
		}
	}
	return nil
}
