// Package domain contains transport-independent data contracts.
package domain

import (
	"github.com/danielskowronski/qiotlctl/internal/settings"
	"time"
)

type Direction string

const (
	FromDevice Direction = "from-device"
	ToDevice   Direction = "to-device"
)

func (d Direction) Valid() bool { return d == FromDevice || d == ToDevice }

type Reading struct {
	OriginalValue  *float64 `json:"original_value,omitempty"`
	OriginalUnit   string   `json:"original_unit,omitempty"`
	Sensor         string   `json:"sensor"`
	Value          float64  `json:"value"`
	Unit           string   `json:"unit"`
	Quality        string   `json:"quality"`
	Status         *int     `json:"status,omitempty"`
	ChargingStatus *int     `json:"charging_status,omitempty"`
}

type Sample struct {
	Timestamp   int64     `json:"timestamp"`
	TimeQuality string    `json:"time_quality"`
	Origin      string    `json:"origin"`
	Readings    []Reading `json:"readings"`
}

type Alarm struct {
	Sensor    string  `json:"sensor"`
	Condition string  `json:"condition"`
	Threshold float64 `json:"threshold"`
	Timestamp int64   `json:"timestamp"`
}

// Event can contain readings, settings, metadata and alarms simultaneously.
// Unknown fields preserve forward compatibility without assuming their meaning.
type Event struct {
	SchemaVersion    int                  `json:"schema_version"`
	RecordID         int64                `json:"record_id,omitempty"`
	DeviceID         string               `json:"device_id,omitempty"`
	Alias            string               `json:"alias,omitempty"`
	Model            string               `json:"model,omitempty"`
	Topic            string               `json:"topic,omitempty"`
	Direction        Direction            `json:"direction"`
	Protocol         string               `json:"protocol"`
	MessageType      string               `json:"message_type,omitempty"`
	ReceivedAt       time.Time            `json:"received_at"`
	ReportedAt       *int64               `json:"reported_at,omitempty"`
	Samples          []Sample             `json:"samples,omitempty"`
	Metadata         map[string]any       `json:"metadata,omitempty"`
	Settings         map[string]any       `json:"settings,omitempty"`
	SettingsConfig   settings.Values      `json:"settings_config,omitempty"`
	SettingsObserved settings.Observation `json:"-"`
	Alarms           []Alarm              `json:"alarms,omitempty"`
	Unknown          map[string]any       `json:"unknown,omitempty"`
	ResponseRequired bool                 `json:"response_required,omitempty"`
	Warnings         []string             `json:"warnings,omitempty"`
	Error            string               `json:"error,omitempty"`
}

func TimeQuality(ts int64, received time.Time) string {
	if ts <= 0 {
		return "missing"
	}
	if ts < 1577836800 {
		return "before-2020"
	}
	if !received.IsZero() && ts > received.Unix()+300 {
		return "future"
	}
	return "device"
}

type Capture struct {
	// StoredEvent is available when raw recording was disabled or credentials were excluded.
	StoredEvent *Event
	ID          int64
	ReceivedAt  time.Time
	Topic       string
	Payload     []byte
	Direction   Direction
	QoS         *byte
	Retained    *bool
	Duplicate   *bool
}

// CommandResult describes evidence, not an assumption that publication executed a command.
type CommandResult struct {
	SchemaVersion int        `json:"schema_version"`
	OperationID   string     `json:"operation_id"`
	DeviceID      string     `json:"device_id"`
	Alias         string     `json:"alias"`
	Operation     string     `json:"operation"`
	Status        string     `json:"status"`
	Evidence      string     `json:"evidence"`
	Detail        string     `json:"detail,omitempty"`
	RequestID     *int64     `json:"request_id,omitempty"`
	StartedAt     time.Time  `json:"started_at"`
	AttemptedAt   *time.Time `json:"attempted_at,omitempty"`
	PublishedAt   *time.Time `json:"published_at,omitempty"`
	FinishedAt    *time.Time `json:"finished_at,omitempty"`
	PayloadSHA256 string     `json:"payload_sha256"`
	PayloadSize   int        `json:"payload_size"`
	Response      *Event     `json:"response,omitempty"`
	Error         string     `json:"error,omitempty"`
}
