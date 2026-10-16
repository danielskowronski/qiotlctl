package config

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/danielskowronski/qiotlctl/internal/settings"
	"github.com/knadh/koanf/maps"
)

// A layer keeps values and their winning source in identically shaped trees.
// Lists are atomic in both trees. Nulls survive until profile/device expansion.
// koanf owns merging and copying; no alternate YAML or merge engine is used.
type layer struct {
	values, origins map[string]any
}

func newLayer() layer { return layer{map[string]any{}, map[string]any{}} }
func (l layer) merge(src layer) {
	merge(l.values, src.values)
	merge(l.origins, src.origins)
}
func (l layer) clone() layer { return layer{maps.Copy(l.values), maps.Copy(l.origins)} }

// Source identifies the authored normalized YAML path, never its value.
type Source struct {
	File     string `json:"file"`
	Document int    `json:"document"`
	Path     string `json:"path"`
	Removed  bool   `json:"removed,omitempty"`
}

func sourceTree(m map[string]any, file string, doc int, prefix string) map[string]any {
	out := map[string]any{}
	for k, v := range m {
		path := k
		if prefix != "" {
			path = prefix + "." + k
		}
		if child, ok := v.(map[string]any); ok {
			out[k] = sourceTree(child, file, doc, path)
		} else {
			out[k] = Source{File: file, Document: doc, Path: path, Removed: v == nil}
		}
	}
	return out
}

// Explanation describes resolved config settings, before command-specific
// --settings-file/--set overrides. Raw setting values are redacted by Explain.
type Explanation struct {
	Alias         string            `json:"alias"`
	Profile       string            `json:"profile,omitempty"`
	ProfileChain  []string          `json:"profile_chain"`
	ProfileSource *Source           `json:"profile_source,omitempty"`
	Settings      settings.Values   `json:"settings"`
	Sources       map[string]Source `json:"sources"`
}

func (c Config) Explain(alias string) (Explanation, error) {
	for key, d := range c.Redacted().Devices {
		if strings.EqualFold(key, alias) {
			e := c.explanations[key]
			e.Alias, e.Settings = key, d.Settings
			return e, nil
		}
	}
	return Explanation{}, fmt.Errorf("unknown device alias %q", alias)
}

type resolvedProfile struct {
	layer
	chain []string
}

// expandProfiles resolves the authored-only keys before cleanenv loads the
// runtime contract. Runtime code sees fully expanded Device.Settings only.
func expandProfiles(l layer) (map[string]Explanation, error) {
	profiles, ok := l.values["profiles"].(map[string]any)
	if !ok && l.values["profiles"] != nil {
		return nil, errors.New("profiles must be a mapping of names to settings profiles")
	}
	cache := map[string]resolvedProfile{}
	active := map[string]bool{}
	var resolveProfile func(string, []string) (resolvedProfile, error)
	resolveProfile = func(name string, path []string) (resolvedProfile, error) {
		if cached, ok := cache[name]; ok {
			return cached, nil
		}
		path = append(append([]string(nil), path...), name)
		if active[name] {
			return resolvedProfile{}, fmt.Errorf("profile inheritance cycle: %s", strings.Join(path, " -> "))
		}
		if len(path) > 32 {
			return resolvedProfile{}, errors.New("profile inheritance depth exceeds 32")
		}
		v, exists := profiles[name]
		if !exists || v == nil {
			return resolvedProfile{}, fmt.Errorf("unknown settings profile %q", name)
		}
		if name == "" || strings.TrimSpace(name) != name {
			return resolvedProfile{}, errors.New("profile names must be nonempty without surrounding whitespace")
		}
		m, ok := v.(map[string]any)
		if !ok {
			return resolvedProfile{}, fmt.Errorf("profile %s must be a mapping", name)
		}
		for k := range m {
			if k != "extends" && k != "settings" {
				return resolvedProfile{}, fmt.Errorf("profile %s: unknown field %s; only extends and settings are allowed", name, k)
			}
		}
		if _, ok := m["settings"].(map[string]any); !ok && m["settings"] != nil {
			return resolvedProfile{}, fmt.Errorf("profile %s: settings must be a mapping or null", name)
		}
		parent, ok := m["extends"].(string)
		if !ok && m["extends"] != nil {
			return resolvedProfile{}, fmt.Errorf("profile %s: extends must name one parent profile", name)
		}
		active[name] = true
		defer delete(active, name)
		out := resolvedProfile{layer: newLayer()}
		if parent != "" {
			base, err := resolveProfile(parent, path)
			if err != nil {
				return resolvedProfile{}, err
			}
			out.layer = base.clone()
			out.chain = append(out.chain, base.chain...)
		}
		out.chain = append(out.chain, name)
		if len(out.chain) > 32 {
			return resolvedProfile{}, errors.New("profile inheritance depth exceeds 32")
		}
		if value, present := m["settings"]; present {
			source := maps.Search(l.origins, []string{"profiles", name, "settings"})
			out.merge(layer{map[string]any{"settings": value}, map[string]any{"settings": source}})
		}
		cache[name] = out
		return out, nil
	}
	// Check the entire graph, including unused definitions, in a stable order.
	names := make([]string, 0, len(profiles))
	for name, value := range profiles {
		if value != nil {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		if _, err := resolveProfile(name, nil); err != nil {
			return nil, err
		}
	}
	explanations := map[string]Explanation{}
	devices, _ := l.values["devices"].(map[string]any)
	for alias, value := range devices {
		if value == nil { // A removed device must not resurrect its profile.
			continue
		}
		d := value.(map[string]any) // normalizeDevices already checked the shape.
		name, ok := d["profile"].(string)
		if !ok && d["profile"] != nil {
			return nil, fmt.Errorf("device %s: profile must name one settings profile", alias)
		}
		e := Explanation{Alias: alias, Profile: name, ProfileChain: []string{}, Sources: map[string]Source{}}
		if src, ok := maps.Search(l.origins, []string{"devices", alias, "profile"}).(Source); ok {
			e.ProfileSource = &src
		}
		out := newLayer()
		if name != "" {
			p, err := resolveProfile(name, nil)
			if err != nil {
				return nil, fmt.Errorf("device %s: %w", alias, err)
			}
			out = p.clone()
			e.ProfileChain = append(e.ProfileChain, p.chain...)
		}
		if value, present := d["settings"]; present {
			src := maps.Search(l.origins, []string{"devices", alias, "settings"})
			out.merge(layer{map[string]any{"settings": value}, map[string]any{"settings": src}})
		}
		if value, present := out.values["settings"]; present {
			d["settings"] = value
		}
		origins, _ := maps.Flatten(out.origins, nil, ".")
		for path, value := range origins {
			if src, ok := value.(Source); ok {
				e.Sources[path] = src
			}
		}
		delete(d, "profile")
		explanations[alias] = e
	}
	delete(l.values, "profiles")
	return explanations, nil
}
