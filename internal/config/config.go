// Package config loads deterministic YAML layers, resolves settings profiles,
// and uses cleanenv for typed configuration and environment overrides.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/danielskowronski/qiotlctl/internal/settings"
	"github.com/ilyakaznacheev/cleanenv"
	"gopkg.in/yaml.v3"
)

func DefaultPath(daemon bool) string {
	if daemon {
		return "/etc/qiotld/config.yaml"
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".config", "qiotlctl", "config.yaml")
}
func Paths(explicit []string, daemon bool) ([]string, error) {
	if len(explicit) > 0 {
		return explicit, nil
	}
	key := "QIOTLCTL_CONFIG"
	if daemon {
		key = "QIOTLD_CONFIG"
	}
	if value, ok := os.LookupEnv(key); ok {
		if value == "" {
			return nil, fmt.Errorf("%s is empty", key)
		}
		return filepath.SplitList(value), nil
	}
	p := DefaultPath(daemon)
	if _, err := os.Stat(p); err == nil {
		return []string{p}, nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return nil, nil
}

func Load(paths []string, options ...settings.Options) (Config, error) {
	layers := newLayer()
	l := loader{active: map[string]bool{}}
	for _, p := range paths {
		m, err := l.file(p, 0)
		if err != nil {
			return Config{}, err
		}
		layers.merge(m)
	}
	root := layers.values
	explanations, err := expandProfiles(layers)
	if err != nil {
		return Config{}, err
	}
	removeNulls(root)
	b, err := yaml.Marshal(root)
	if err != nil {
		return Config{}, errors.New("cannot represent merged configuration")
	}
	c := Config{
		APIVersion: "qiotl/v1alpha1",
		MQTT:       MQTT{KeepAlive: Duration(time.Minute), ConnectTimeout: Duration(15 * time.Second), ReconnectMax: Duration(30 * time.Second), QoS: 1},
		Runtime:    Runtime{Mode: "observe", QueueSize: 1024, ObserveCommands: true},
		Commands:   Commands{Timeout: Duration(30 * time.Second)},
		Outputs:    DefaultOutputs(),
		Health:     DefaultHealth(),
		Storage:    Storage{CaptureRaw: true, MaxBytes: 10 << 30}, Devices: map[string]Device{},
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err = dec.Decode(&c); err != nil {
		return Config{}, fmt.Errorf("invalid merged configuration: %s", redactError(err.Error(), root))
	}
	// cleanenv owns typed YAML loading and all environment conversion. The
	// preceding KnownFields pass adds strict schema checking to its parser.
	if err = cleanenv.ParseYAML(bytes.NewReader(b), &c); err != nil {
		return Config{}, errors.New("invalid typed YAML configuration")
	}
	if err = cleanenv.ReadEnv(&c); err != nil {
		return Config{}, errors.New("invalid environment override; see config env for names and types")
	}
	if err = resolveVMCredentials(&c.Outputs.VictoriaMetrics); err != nil {
		return Config{}, err
	}
	c.Sources = l.sources
	c.explanations = explanations
	if err = resolve(&c, root, os.Environ()); err != nil {
		return Config{}, err
	}
	// Direct environment credentials deliberately take precedence over files.
	if v, ok := os.LookupEnv("QIOTLCTL_MQTT_USERNAME"); ok {
		c.MQTT.Username = v
	}
	if v, ok := os.LookupEnv("QIOTLCTL_MQTT_PASSWORD"); ok {
		c.MQTT.Password = v
	}
	if v, ok := os.LookupEnv("QIOTLCTL_MQTT_URL"); ok {
		c.MQTT.URL = v
	}
	if len(options) > 0 {
		c.settingsOptions = options[0]
	}
	if err = c.Validate(false); err != nil {
		return Config{}, err
	}
	// Normalize YAML's named nested maps once at the runtime boundary. Validation
	// above has already checked JSON compatibility; each device keeps its own copy.
	for alias, d := range c.Devices {
		if d.Settings != nil {
			d.Settings = settings.Clone(d.Settings)
			c.Devices[alias] = d
		}
	}
	return c, nil
}

// EnvironmentHelp is generated from cleanenv tags, keeping docs and parsing aligned.
func EnvironmentHelp() (string, error) { return cleanenv.GetDescription(&Config{}, nil) }
