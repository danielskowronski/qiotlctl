package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/danielskowronski/qiotlctl/internal/device"
)

func resolve(c *Config, root map[string]any, environ []string) error {
	env := map[string]string{}
	for _, s := range environ {
		k, v, ok := strings.Cut(s, "=")
		if ok {
			env[k] = v
		}
	}
	m, _ := root["mqtt"].(map[string]any)
	if c.MQTT.ConnectionFile != "" {
		b, err := os.ReadFile(c.MQTT.ConnectionFile)
		if err != nil {
			return fmt.Errorf("read MQTT connection file: %w", err)
		}
		var creds struct {
			User string `json:"user"`
			Pass string `json:"pass"`
			URI  string `json:"mqtt_uri"`
		}
		d := json.NewDecoder(bytes.NewReader(b))
		d.DisallowUnknownFields()
		if d.Decode(&creds) != nil {
			return errors.New("MQTT connection file must contain user, pass and mqtt_uri strings")
		}
		var extra any
		if d.Decode(&extra) != io.EOF {
			return errors.New("trailing content in MQTT connection file")
		}
		if _, ok := m["url"]; !ok {
			c.MQTT.URL = creds.URI
		}
		if _, ok := m["username"]; !ok {
			c.MQTT.Username = creds.User
		}
		if _, ok := m["password"]; !ok {
			c.MQTT.Password = creds.Pass
		}
	}
	if c.MQTT.PasswordEnv != "" && c.MQTT.PasswordFile != "" {
		return errors.New("choose password_env or password_file")
	}
	for _, item := range []struct {
		name string
		dst  *string
	}{{c.MQTT.UsernameEnv, &c.MQTT.Username}, {c.MQTT.PasswordEnv, &c.MQTT.Password}} {
		if item.name != "" {
			v, ok := env[item.name]
			if !ok {
				return fmt.Errorf("required credential environment variable %s is unset", item.name)
			}
			*item.dst = v
		}
	}
	if c.MQTT.PasswordFile != "" {
		b, err := os.ReadFile(c.MQTT.PasswordFile)
		if err != nil {
			return fmt.Errorf("read MQTT password file: %w", err)
		}
		c.MQTT.Password = strings.TrimSuffix(strings.TrimSuffix(string(b), "\n"), "\r")
	}
	for alias, d := range c.Devices {
		d.Alias = alias
		if d.ID == "" {
			if d.MAC != "" {
				d.ID = strings.ToUpper(strings.ReplaceAll(d.MAC, ":", ""))
			} else {
				d.ID = alias
			}
		}
		d.Model = strings.ToUpper(d.Model)
		p := device.Lookup(d.Model)
		if d.Protocol == "" {
			d.Protocol = p.Protocol
		}
		c.Devices[alias] = d
	}
	return nil
}
