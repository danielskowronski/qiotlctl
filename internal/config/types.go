package config

import (
	"errors"
	"time"

	"github.com/danielskowronski/qiotlctl/internal/settings"
)

type Duration time.Duration

func (d Duration) Duration() time.Duration   { return time.Duration(d) }
func (d Duration) MarshalYAML() (any, error) { return time.Duration(d).String(), nil }
func (d *Duration) SetValue(s string) error {
	v, err := time.ParseDuration(s)
	if err != nil {
		return errors.New("invalid duration; use 30s or 5m")
	}
	*d = Duration(v)
	return nil
}
func (d *Duration) UnmarshalText(b []byte) error { return d.SetValue(string(b)) }

type TLS struct {
	CAFile     string `yaml:"ca_file" env:"QIOTLCTL_MQTT_TLS_CA_FILE"`
	CertFile   string `yaml:"cert_file" env:"QIOTLCTL_MQTT_TLS_CERT_FILE"`
	KeyFile    string `yaml:"key_file" env:"QIOTLCTL_MQTT_TLS_KEY_FILE"`
	ServerName string `yaml:"server_name" env:"QIOTLCTL_MQTT_TLS_SERVER_NAME"`
}
type MQTT struct {
	URL            string   `yaml:"url" env:"QIOTLCTL_MQTT_URL"`
	Username       string   `yaml:"username" env:"QIOTLCTL_MQTT_USERNAME"`
	Password       string   `yaml:"password" env:"QIOTLCTL_MQTT_PASSWORD"`
	UsernameEnv    string   `yaml:"username_env" env:"QIOTLCTL_MQTT_USERNAME_ENV"`
	PasswordEnv    string   `yaml:"password_env" env:"QIOTLCTL_MQTT_PASSWORD_ENV"`
	PasswordFile   string   `yaml:"password_file" env:"QIOTLCTL_MQTT_PASSWORD_FILE"`
	ConnectionFile string   `yaml:"connection_file" env:"QIOTLCTL_MQTT_CONNECTION_FILE"`
	ClientID       string   `yaml:"client_id" env:"QIOTLCTL_MQTT_CLIENT_ID"`
	KeepAlive      Duration `yaml:"keepalive" env:"QIOTLCTL_MQTT_KEEPALIVE"`
	ConnectTimeout Duration `yaml:"connect_timeout" env:"QIOTLCTL_MQTT_CONNECT_TIMEOUT"`
	ReconnectMax   Duration `yaml:"reconnect_max" env:"QIOTLCTL_MQTT_RECONNECT_MAX"`
	QoS            byte     `yaml:"qos" env:"QIOTLCTL_MQTT_QOS"`
	TLS            TLS      `yaml:"tls"`
}
type Topics struct {
	Telemetry string `yaml:"telemetry"`
	Commands  string `yaml:"commands"`
}
type Device struct {
	ID            string          `yaml:"id"`
	Alias         string          `yaml:"alias"`
	MAC           string          `yaml:"mac"`
	Model         string          `yaml:"model"`
	Protocol      string          `yaml:"protocol"`
	Topics        Topics          `yaml:"topics"`
	Enabled       *bool           `yaml:"enabled,omitempty"`
	Responses     Responses       `yaml:"responses"`
	Settings      settings.Values `yaml:"settings,omitempty"`
	HomeAssistant HADevice        `yaml:"home_assistant,omitempty"`
}

func (d Device) Active() bool { return d.Enabled == nil || *d.Enabled }

// Enabled inherits runtime.mode; clock compatibility is a separate per-device opt-in.
type Responses struct {
	Enabled     *bool    `yaml:"enabled,omitempty"`
	Clock       string   `yaml:"clock,omitempty"`
	MinInterval Duration `yaml:"min_interval,omitempty"`
}

func (d Device) Respond(mode string) bool {
	if d.Responses.Enabled != nil {
		return *d.Responses.Enabled
	}
	return mode == "respond"
}
func (d Device) ClockInterval() time.Duration {
	if d.Responses.MinInterval == 0 {
		return 15 * time.Minute
	}
	return d.Responses.MinInterval.Duration()
}

type Commands struct {
	Timeout Duration `yaml:"timeout" env:"QIOTLCTL_COMMAND_TIMEOUT"`
}
type Runtime struct {
	QueueSize       int    `yaml:"queue_size" env:"QIOTLCTL_QUEUE_SIZE"`
	ObserveCommands bool   `yaml:"observe_commands" env:"QIOTLCTL_OBSERVE_COMMANDS"`
	Mode            string `yaml:"mode" env:"QIOTLCTL_MODE"`
}
type Storage struct {
	Path       string `yaml:"path" env:"QIOTLCTL_STORAGE_PATH"`
	CaptureRaw bool   `yaml:"capture_raw" env:"QIOTLCTL_STORAGE_CAPTURE_RAW"`
	MaxBytes   int64  `yaml:"max_bytes" env:"QIOTLCTL_STORAGE_MAX_BYTES"`
}
type HTTP struct {
	Listen string `yaml:"listen" env:"QIOTLCTL_HTTP_LISTEN"`
}
type Config struct {
	// Validation policy belongs to this load only, never to YAML or environment.
	settingsOptions settings.Options
	explanations    map[string]Explanation
	APIVersion      string            `yaml:"api_version"`
	MQTT            MQTT              `yaml:"mqtt"`
	Devices         map[string]Device `yaml:"devices"`
	Runtime         Runtime           `yaml:"runtime"`
	Storage         Storage           `yaml:"storage"`
	HTTP            HTTP              `yaml:"http"`
	Health          Health            `yaml:"health"`
	Commands        Commands          `yaml:"commands"`
	Outputs         Outputs           `yaml:"outputs"`
	Sources         []string          `yaml:"-"`
}
