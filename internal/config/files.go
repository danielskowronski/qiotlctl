package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/knadh/koanf/maps"
	"gopkg.in/yaml.v3"
)

type loader struct {
	active  map[string]bool
	sources []string
}

func (l *loader) file(path string, depth int) (layer, error) {
	if depth > 32 {
		return layer{}, errors.New("include depth exceeds 32")
	}
	p, err := expandPath(path)
	if err != nil {
		return layer{}, err
	}
	p, err = filepath.Abs(p)
	if err != nil {
		return layer{}, err
	}
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return layer{}, fmt.Errorf("config file %s: %w", p, err)
	}
	if l.active[real] {
		return layer{}, fmt.Errorf("config include cycle at %s", p)
	}
	l.active[real] = true
	defer delete(l.active, real)
	f, err := os.Open(p)
	if err != nil {
		return layer{}, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, (4<<20)+1))
	if err != nil {
		return layer{}, err
	}
	if len(data) > 4<<20 {
		return layer{}, errors.New("configuration file exceeds 4 MiB")
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	out := newLayer()
	for doc := 1; ; doc++ {
		var n yaml.Node
		err = dec.Decode(&n)
		if err == io.EOF {
			break
		}
		if err != nil {
			return layer{}, fmt.Errorf("%s document %d: invalid YAML", p, doc)
		}
		var m map[string]any
		if n.Decode(&m) != nil {
			return layer{}, fmt.Errorf("%s document %d: expected mapping with unique string keys", p, doc)
		}
		if m == nil {
			continue
		}
		if inc, ok := m["include"]; ok {
			var names []string
			switch v := inc.(type) {
			case string:
				names = []string{v}
			case []any:
				for _, v := range v {
					v, ok := v.(string)
					if !ok {
						return layer{}, errors.New("include must contain paths")
					}
					names = append(names, v)
				}
			default:
				return layer{}, errors.New("include must be a path or list of paths")
			}
			for _, name := range names {
				if !filepath.IsAbs(name) && !strings.HasPrefix(name, "~") {
					name = filepath.Join(filepath.Dir(p), name)
				}
				name, err = expandPath(name)
				if err != nil {
					return layer{}, err
				}
				matches, err := filepath.Glob(name)
				if err != nil || len(matches) == 0 {
					return layer{}, fmt.Errorf("include pattern has no valid matches: %s", name)
				}
				sort.Strings(matches)
				for _, match := range matches {
					base, err := l.file(match, depth+1)
					if err != nil {
						return layer{}, err
					}
					out.merge(base)
				}
			}
			delete(m, "include")
		}
		if err = normalizeDevices(m); err != nil {
			return layer{}, fmt.Errorf("%s document %d: %w", p, doc, err)
		}
		if err = resolveFilePaths(m, filepath.Dir(p), ""); err != nil {
			return layer{}, err
		}
		out.merge(layer{values: m, origins: sourceTree(m, p, doc, "")})
	}
	l.sources = append(l.sources, p)
	return out, nil
}

func normalizeDevices(root map[string]any) error {
	v, ok := root["devices"]
	if !ok || v == nil {
		return nil
	}
	var devices map[string]any
	switch v := v.(type) {
	case map[string]any:
		devices = v
	case []any:
		devices = map[string]any{}
		for _, item := range v {
			m, ok := item.(map[string]any)
			if !ok {
				return errors.New("devices must contain mappings")
			}
			alias, ok := m["alias"].(string)
			if !ok || alias == "" {
				return errors.New("device list item requires alias")
			}
			if _, exists := devices[alias]; exists {
				return fmt.Errorf("duplicate device alias %s", alias)
			}
			devices[alias] = m
		}
	default:
		return errors.New("devices must be a mapping or a list")
	}
	for alias, v := range devices {
		if v == nil {
			continue
		}
		d, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("device %s must be a mapping", alias)
		}
		if supplied, ok := d["alias"]; ok && supplied != alias {
			return fmt.Errorf("device key and alias disagree for %s", alias)
		}
		// Accept the previous tool's device excerpt; normalize before merging so
		// old and new files override each other in the same predictable order.
		for old, newKey := range map[string]string{"topic_up": "telemetry", "topic_down": "commands"} {
			if value, ok := d[old]; ok {
				topics, ok := d["topics"].(map[string]any)
				if !ok {
					topics = map[string]any{}
					d["topics"] = topics
				}
				if _, exists := topics[newKey]; exists {
					return fmt.Errorf("device %s supplies both legacy and new topic", alias)
				}
				topics[newKey] = value
				delete(d, old)
			}
		}
	}
	root["devices"] = devices
	return nil
}

// Keep null tombstones until every layer is merged; otherwise a removal in
// an included file could accidentally resurrect a device from an earlier layer.
func merge(dst, src map[string]any) { maps.Merge(src, dst) }
func removeNulls(m map[string]any) {
	for k, v := range m {
		if v == nil {
			delete(m, k)
		} else if child, ok := v.(map[string]any); ok {
			removeNulls(child)
		}
	}
}

func expandPath(p string) (string, error) {
	if p == "~" || strings.HasPrefix(p, "~/") {
		h, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if p == "~" {
			return h, nil
		}
		return filepath.Join(h, strings.TrimPrefix(p, "~/")), nil
	}
	return p, nil
}
func resolveFilePaths(m map[string]any, base, prefix string) error {
	for k, v := range m {
		path := k
		if prefix != "" {
			path = prefix + "." + k
		}
		if child, ok := v.(map[string]any); ok {
			if err := resolveFilePaths(child, base, path); err != nil {
				return err
			}
			continue
		}
		if path == "storage.path" || (strings.HasPrefix(path, "mqtt.") || strings.HasPrefix(path, "outputs.victoriametrics.")) && strings.HasSuffix(k, "_file") {
			s, ok := v.(string)
			if !ok || s == "" {
				continue
			}
			s, err := expandPath(s)
			if err != nil {
				return err
			}
			if !filepath.IsAbs(s) {
				s = filepath.Join(base, s)
			}
			m[k] = s
		}
	}
	return nil
}
