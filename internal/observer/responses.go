package observer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/danielskowronski/qiotlctl/internal/config"
	"github.com/danielskowronski/qiotlctl/internal/device"
	"github.com/danielskowronski/qiotlctl/internal/domain"
	"github.com/danielskowronski/qiotlctl/internal/protocol"
	"github.com/danielskowronski/qiotlctl/internal/storage"
	"github.com/danielskowronski/qiotlctl/internal/transport"
	paho "github.com/eclipse/paho.mqtt.golang"
	"github.com/google/uuid"
)

type responder struct{ lastClock map[string]time.Time }

// reply runs only after the inbound capture has committed. MQTT PUBACK is
// independent and can precede persistence; this governs the application ACK.
func (r *responder) reply(ctx context.Context, c config.Config, d config.Device, cap domain.Capture, event domain.Event, client paho.Client, recorder *storage.Recorder, stats *Stats) (*domain.Event, error) {
	if cap.Direction != domain.FromDevice || cap.StoredEvent != nil || cap.Retained == nil || *cap.Retained || event.Error != "" || !d.Respond(c.Runtime.Mode) || ctx.Err() != nil {
		return nil, nil
	}
	now := time.Now().UTC()
	if now.Sub(cap.ReceivedAt) > 30*time.Second || cap.ReceivedAt.After(now) || !client.IsConnectionOpen() || !stats.Subscribed.Load() {
		return nil, nil
	}
	msg, err := protocol.Decode(cap.Payload, d.Protocol)
	if err != nil || !device.IdentityMatches(msg, d.MAC) {
		return nil, nil
	}
	clockAllowed := r.clockAllowed(d, now)
	reply := device.AutomaticReply(msg, d.Model, now, clockAllowed)
	if len(reply.Payload) == 0 || (reply.Kind == "history-ack" && len(event.Warnings) > 0) {
		return nil, nil
	}
	replyCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	outgoing := domain.Capture{Topic: d.Topics.Commands, Payload: reply.Payload, Direction: domain.ToDevice, ReceivedAt: now}
	decoded := DecodeCapture(map[string]Route{d.Topics.Commands: {Device: d, Direction: domain.ToDevice}}, outgoing)
	sum := sha256.Sum256(reply.Payload)
	result := domain.CommandResult{SchemaVersion: 1, OperationID: uuid.NewString(), DeviceID: d.ID, Alias: d.Alias, Operation: reply.Kind, Status: "attempting", Evidence: "automatic response intent", StartedAt: now, AttemptedAt: &now, PayloadSHA256: hex.EncodeToString(sum[:]), PayloadSize: len(reply.Payload)}
	if decoded.Metadata == nil {
		decoded.Metadata = map[string]any{}
	}
	decoded.Metadata["operation_id"] = result.OperationID
	decoded.Metadata["transmission"] = "intent; consult command_events for outcome"
	if recorder != nil {
		id, e := recorder.Record(replyCtx, outgoing, decoded)
		if e != nil {
			return nil, fmt.Errorf("record reply intent: %w", e)
		}
		decoded.RecordID = id
		if e = recorder.RecordCommand(replyCtx, result); e != nil {
			return nil, fmt.Errorf("record reply attempt: %w", e)
		}
	}
	// QoS 0 avoids queuing or replaying a stale clock/ACK across daemon reconnects.
	// A retransmitted historical request can trigger a new, current ACK.
	generation := stats.Reconnects.Load()
	err = transport.Publish(replyCtx, client, d.Topics.Commands, 0, reply.Payload)
	if err == nil && (!client.IsConnectionOpen() || stats.Reconnects.Load() != generation) {
		err = fmt.Errorf("connection changed during automatic response; delivery unknown")
	}
	if err != nil {
		stats.ReplyErrors.Add(1)
		result.Status = "unknown"
		result.Error = err.Error()
		result.Evidence = "response attempted; delivery unknown"
		decoded.Warnings = append(decoded.Warnings, result.Error)
	} else {
		stats.Replies.Add(1)
		result.Status = "published"
		result.Evidence = "automatic response published; no device confirmation"
		stamp := time.Now().UTC()
		result.PublishedAt = &stamp
		if r.lastClock == nil {
			r.lastClock = map[string]time.Time{}
		}
		r.lastClock[d.ID] = time.Now()
	}
	finished := time.Now().UTC()
	result.FinishedAt = &finished
	decoded.Metadata["transmission"] = result.Status
	if recorder != nil {
		flush, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if e := recorder.RecordCommand(flush, result); e != nil {
			return nil, fmt.Errorf("record reply outcome: %w", e)
		}
	}
	return &decoded, nil
}

func (r *responder) clockAllowed(d config.Device, now time.Time) bool {
	return d.Responses.Clock == "type18-heartbeat" && now.Sub(r.lastClock[d.ID]) >= d.ClockInterval()
}
