package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

type Outputs struct {
	VictoriaMetrics VM `yaml:"victoriametrics"`
	HomeAssistant   HA `yaml:"home_assistant"`
}
type VM struct {
	Enabled            bool     `yaml:"enabled" env:"QIOTLCTL_VM_ENABLED"`
	ImportEndpoint     string   `yaml:"import_endpoint" env:"QIOTLCTL_VM_IMPORT_ENDPOINT"`
	MetricsPrefix      string   `yaml:"metrics_prefix" env:"QIOTLCTL_VM_METRICS_PREFIX"`
	Username           string   `yaml:"username" env:"QIOTLCTL_VM_USERNAME"`
	Password           string   `yaml:"password" env:"QIOTLCTL_VM_PASSWORD"`
	PasswordEnv        string   `yaml:"password_env" env:"QIOTLCTL_VM_PASSWORD_ENV"`
	PasswordFile       string   `yaml:"password_file" env:"QIOTLCTL_VM_PASSWORD_FILE"`
	CAFile             string   `yaml:"ca_file" env:"QIOTLCTL_VM_CA_FILE"`
	InsecureSkipVerify bool     `yaml:"insecure_skip_verify" env:"QIOTLCTL_VM_INSECURE_SKIP_VERIFY"`
	Timeout            Duration `yaml:"timeout" env:"QIOTLCTL_VM_TIMEOUT"`
	BatchSize          int      `yaml:"batch_size" env:"QIOTLCTL_VM_BATCH_SIZE"`
	FlushInterval      Duration `yaml:"flush_interval" env:"QIOTLCTL_VM_FLUSH_INTERVAL"`
	RetryMax           Duration `yaml:"retry_max" env:"QIOTLCTL_VM_RETRY_MAX"`
	IncludeInvalidTime bool     `yaml:"include_invalid_time" env:"QIOTLCTL_VM_INCLUDE_INVALID_TIME"`
	NormalOnly         bool     `yaml:"normal_only" env:"QIOTLCTL_VM_NORMAL_ONLY"`
}
type HA struct {
	Enabled         bool     `yaml:"enabled" env:"QIOTLCTL_HA_ENABLED"`
	DiscoveryPrefix string   `yaml:"discovery_prefix" env:"QIOTLCTL_HA_DISCOVERY_PREFIX"`
	StatePrefix     string   `yaml:"state_prefix" env:"QIOTLCTL_HA_STATE_PREFIX"`
	BirthTopic      string   `yaml:"birth_topic" env:"QIOTLCTL_HA_BIRTH_TOPIC"`
	BridgeID        string   `yaml:"bridge_id" env:"QIOTLCTL_HA_BRIDGE_ID"`
	ExpireAfter     Duration `yaml:"expire_after" env:"QIOTLCTL_HA_EXPIRE_AFTER"`
}
type HADevice struct {
	ID                string              `yaml:"id,omitempty"`
	Name              string              `yaml:"name,omitempty"`
	Enabled           *bool               `yaml:"enabled,omitempty"`
	ExpireAfter       Duration            `yaml:"expire_after,omitempty"`
	SensorExpireAfter map[string]Duration `yaml:"sensor_expire_after,omitempty"`
}

func (d Device) HAActive() bool {
	return d.Active() && (d.HomeAssistant.Enabled == nil || *d.HomeAssistant.Enabled)
}
func (d Device) HAID() string {
	if d.HomeAssistant.ID != "" {
		return d.HomeAssistant.ID
	}
	return strings.ToLower(d.ID)
}
func (d Device) HAExpiry(c HA, sensor string) time.Duration {
	if v := d.HomeAssistant.SensorExpireAfter[sensor]; v != 0 {
		return v.Duration()
	}
	if d.HomeAssistant.ExpireAfter != 0 {
		return d.HomeAssistant.ExpireAfter.Duration()
	}
	return c.ExpireAfter.Duration()
}
func DefaultOutputs() Outputs {
	return Outputs{VM{MetricsPrefix: "qingping_iot_", Timeout: Duration(5 * time.Second), BatchSize: 1000, FlushInterval: Duration(time.Second), RetryMax: Duration(time.Minute)}, HA{DiscoveryPrefix: "homeassistant", StatePrefix: "qiotlctl", BirthTopic: "homeassistant/status", BridgeID: "qiotlctl", ExpireAfter: Duration(10 * time.Minute)}}
}

var haIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)
var metricPattern = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*$`)

func (c Config) validateOutputs(online bool) error {
	v, h := c.Outputs.VictoriaMetrics, c.Outputs.HomeAssistant
	if c.Storage.MaxBytes < 0 {
		return errors.New("storage.max_bytes must be nonnegative")
	}
	if online && (v.Enabled || h.Enabled) && c.Storage.Path == "" {
		return errors.New("outputs require storage.path (or --record) for durable recovery")
	}
	if v.Enabled || v.ImportEndpoint != "" {
		u, err := url.Parse(v.ImportEndpoint)
		if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Fragment != "" {
			return errors.New("outputs.victoriametrics.import_endpoint must be an HTTP(S) URL without embedded credentials or fragment")
		}
		if !metricPattern.MatchString(v.MetricsPrefix + "temperature") {
			return errors.New("invalid VM metrics_prefix")
		}
		if v.Timeout <= 0 || v.FlushInterval <= 0 || v.RetryMax < Duration(time.Second) || v.BatchSize < 1 || v.BatchSize > 10000 {
			return errors.New("VM timeouts must be positive, retry_max >= 1s and batch_size 1..10000")
		}
	}
	if h.Enabled {
		for _, p := range []string{h.DiscoveryPrefix, h.StatePrefix, h.BirthTopic} {
			if p == "" || strings.ContainsAny(p, "+#\x00") || strings.HasSuffix(p, "/") {
				return errors.New("HA topics/prefixes must be exact, nonempty topics without trailing slash")
			}
		}
		if h.DiscoveryPrefix == h.StatePrefix || !haIDPattern.MatchString(h.BridgeID) {
			return errors.New("HA needs distinct discovery/state prefixes and a lowercase bridge_id")
		}
		if h.ExpireAfter < Duration(time.Second) || h.ExpireAfter.Duration()%time.Second != 0 {
			return errors.New("HA expire_after must be positive integral seconds")
		}
	}
	ids := map[string]bool{}
	for alias, d := range c.Devices {
		x := d.HomeAssistant
		if x.ID != "" && !haIDPattern.MatchString(x.ID) {
			return fmt.Errorf("device %s: home_assistant.id must contain lowercase letters, digits, underscores or hyphens", alias)
		}
		for _, duration := range append([]Duration{x.ExpireAfter}, mapDurations(x.SensorExpireAfter)...) {
			if duration < 0 || duration.Duration()%time.Second != 0 {
				return fmt.Errorf("device %s: HA expiry must use integral seconds", alias)
			}
		}
		if !h.Enabled || !d.HAActive() {
			continue
		}
		if !haIDPattern.MatchString(d.HAID()) || ids[d.HAID()] {
			return fmt.Errorf("device %s: set a unique home_assistant.id", alias)
		}
		ids[d.HAID()] = true
	}
	return nil
}
func mapDurations(m map[string]Duration) []Duration {
	var a []Duration
	for _, v := range m {
		a = append(a, v)
	}
	return a
}
func resolveVMCredentials(v *VM) error {
	if v.PasswordEnv != "" && v.PasswordFile != "" {
		return errors.New("choose VM password_env or password_file")
	}
	if v.PasswordEnv != "" {
		s, ok := os.LookupEnv(v.PasswordEnv)
		if !ok {
			return errors.New("VM password environment variable is unset")
		}
		v.Password = s
	}
	if v.PasswordFile != "" {
		b, err := os.ReadFile(v.PasswordFile)
		if err != nil {
			return fmt.Errorf("read VM password file: %w", err)
		}
		v.Password = strings.TrimSuffix(strings.TrimSuffix(string(b), "\n"), "\r")
	}
	if s, ok := os.LookupEnv("QIOTLCTL_VM_PASSWORD"); ok {
		v.Password = s
	}
	return nil
}
