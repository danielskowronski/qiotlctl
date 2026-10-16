package transport

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/danielskowronski/qiotlctl/internal/config"
	"github.com/eclipse/paho.mqtt.golang/packets"
)

func TestInspectionPermissionsAndPublishScope(t *testing.T) {
	for _, mode := range []string{"observe", "denied", "probe", "silent-drop", "auth", "timeout", "lost", "retained-and-live"} {
		t.Run(mode, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			peer := make(chan error, 1)
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					peer <- err
					return
				}
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
				err = func() error {
					p, e := packets.ReadPacket(conn)
					if e != nil {
						return e
					}
					connect, ok := p.(*packets.ConnectPacket)
					if !ok || connect.WillFlag || connect.ClientIdentifier == "daemon-fixed-id" {
						return errors.New("unsafe inspection identity/will")
					}
					if mode == "timeout" {
						_, _ = packets.ReadPacket(conn)
						return nil
					}
					ack := packets.NewControlPacket(packets.Connack).(*packets.ConnackPacket)
					if mode == "auth" {
						ack.ReturnCode = 5
					}
					if e = ack.Write(conn); e != nil {
						return e
					}
					if mode == "auth" {
						return nil
					}
					p, e = packets.ReadPacket(conn)
					if e != nil {
						return e
					}
					sub, ok := p.(*packets.SubscribePacket)
					if !ok {
						return errors.New("expected subscribe")
					}
					sa := packets.NewControlPacket(packets.Suback).(*packets.SubackPacket)
					sa.MessageID = sub.MessageID
					for range sub.Topics {
						if mode == "denied" {
							sa.ReturnCodes = append(sa.ReturnCodes, 0x80)
						} else {
							sa.ReturnCodes = append(sa.ReturnCodes, 1)
						}
					}
					if e = sa.Write(conn); e != nil {
						return e
					}
					if mode == "lost" {
						time.Sleep(20 * time.Millisecond)
						return nil
					}
					if mode == "retained-and-live" {
						for _, retained := range []bool{true, false} {
							p := packets.NewControlPacket(packets.Publish).(*packets.PublishPacket)
							p.TopicName, p.Payload, p.Retain = "fixture/up", []byte("discovery"), retained
							if e = p.Write(conn); e != nil {
								return e
							}
						}
					}
					for {
						p, e = packets.ReadPacket(conn)
						if e != nil {
							return nil
						}
						switch p := p.(type) {
						case *packets.PublishPacket:
							if (mode != "probe" && mode != "silent-drop") || p.TopicName != "qiotlctl/diagnostics/test" || p.Retain {
								return errors.New("unexpected application publication")
							}
							pa := packets.NewControlPacket(packets.Puback).(*packets.PubackPacket)
							pa.MessageID = p.MessageID
							if e = pa.Write(conn); e != nil {
								return e
							}
							if mode == "probe" {
								p.Qos = 0
								if e = p.Write(conn); e != nil {
									return e
								}
							}
						case *packets.DisconnectPacket:
							return nil
						default:
							return errors.New("unexpected transport packet")
						}
					}
				}()
				peer <- err
			}()
			c, _ := config.Load(nil)
			c.MQTT.URL, c.MQTT.ClientID = "tcp://"+listener.Addr().String(), "daemon-fixed-id"
			c.MQTT.ConnectTimeout = config.Duration(300 * time.Millisecond)
			ctx, stop := context.WithTimeout(context.Background(), 350*time.Millisecond)
			defer stop()
			probe := ""
			if mode == "probe" || mode == "silent-drop" {
				probe = "qiotlctl/diagnostics/test"
			}
			observed, err := Inspect(ctx, c.MQTT, []string{"fixture/up"}, probe)
			want := map[string]string{"denied": "subscribe denied for fixture/up", "silent-drop": "not observed", "auth": "authentication/authorization rejected", "timeout": "timed out", "lost": "connection lost"}[mode]
			if want == "" && err != nil || want != "" && (err == nil || !strings.Contains(err.Error(), want)) {
				t.Fatal(mode, err)
			}
			if mode == "retained-and-live" && !observed["fixture/up"].Retained {
				t.Fatal("live republish erased retained readback evidence")
			}
			if err = <-peer; err != nil {
				t.Fatal(err)
			}
		})
	}
}
