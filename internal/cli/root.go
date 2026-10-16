// Package cli supplies both the operator CLI and the daemon entry point.
package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/danielskowronski/qiotlctl/internal/config"
	"github.com/danielskowronski/qiotlctl/internal/device"
	"github.com/danielskowronski/qiotlctl/internal/observer"
	"github.com/danielskowronski/qiotlctl/internal/settings"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

type app struct {
	paths           []string
	daemon          bool
	settingsOptions settings.Options
}

func (a *app) load() (config.Config, error) {
	paths, err := config.Paths(a.paths, a.daemon)
	if err != nil {
		return config.Config{}, err
	}
	return config.Load(paths, a.settingsOptions)
}

func New(daemon bool, version string) *cobra.Command {
	a := &app{daemon: daemon}
	name := "qiotlctl"
	if daemon {
		name = "qiotld"
	}
	root := &cobra.Command{Use: name, Short: "Qingping local MQTT observation, commands and offline protocol tools", Version: version, SilenceUsage: true, SilenceErrors: true}
	root.PersistentFlags().StringArrayVarP(&a.paths, "config", "c", nil, "YAML configuration file; repeat in override order")
	root.PersistentFlags().BoolVar(&a.settingsOptions.IgnoreOfficialLimits, "ignore-official-limits", false, "Allow device values outside documented ranges; still enforce wire types, widths and exact units")
	root.AddCommand(a.configCommand(), a.devicesCommand(), decodeCommand(), a.encodeCommand(), a.replayCommand())
	if daemon {
		configureWatch(root, a)
	} else {
		watch := &cobra.Command{Use: "watch", Aliases: []string{"subscribe"}, Short: "Subscribe and decode messages (automatic replies disabled by default)"}
		configureWatch(watch, a)
		root.AddCommand(watch, a.settingsCommand(), a.commandCommand(), a.applySettingsCommand(), a.backfillCommand(), a.outputsCommand(), a.haCommand(), a.doctorCommand())
	}
	return root
}
func (a *app) configCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "config", Short: "Validate and inspect configuration"}
	cmd.AddCommand(&cobra.Command{Use: "validate", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		c, err := a.load()
		if err != nil {
			return err
		}
		if err = c.Validate(true); err != nil {
			return err
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "configuration valid (mode=%s; per-device response overrides apply)\n", c.Runtime.Mode)
		return err
	}})
	cmd.AddCommand(&cobra.Command{Use: "show", Args: cobra.NoArgs, Short: "Show effective configuration with profiles expanded and credentials redacted", RunE: func(cmd *cobra.Command, _ []string) error {
		c, err := a.load()
		if err != nil {
			return err
		}
		return writeYAML(cmd.OutOrStdout(), c.Redacted())
	}})
	cmd.AddCommand(&cobra.Command{Use: "explain ALIAS", Args: cobra.ExactArgs(1), Short: "Show resolved settings, profile ancestry and source files (raw values redacted)", RunE: func(cmd *cobra.Command, args []string) error {
		c, err := a.load()
		if err != nil {
			return err
		}
		e, err := c.Explain(args[0])
		if err != nil {
			return err
		}
		return writeJSON(cmd.OutOrStdout(), e)
	}})
	cmd.AddCommand(&cobra.Command{Use: "env", Args: cobra.NoArgs, Short: "List environment overrides generated from cleanenv tags", RunE: func(cmd *cobra.Command, _ []string) error {
		s, err := config.EnvironmentHelp()
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), "QIOTLCTL_CONFIG / QIOTLD_CONFIG: config paths separated by the OS path-list separator\n"+s)
		return err
	}})
	return cmd
}
func (a *app) devicesCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "devices", Short: "Inspect configured devices and model capabilities"}
	cmd.AddCommand(&cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		c, err := a.load()
		if err != nil {
			return err
		}
		return writeJSON(cmd.OutOrStdout(), observer.DeviceList(c))
	}})
	cmd.AddCommand(&cobra.Command{Use: "capabilities ALIAS_OR_MODEL", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		c, err := a.load()
		if err != nil {
			return err
		}
		model := args[0]
		for alias, d := range c.Devices {
			if strings.EqualFold(alias, args[0]) {
				model = d.Model
				break
			}
		}
		return writeJSON(cmd.OutOrStdout(), device.Lookup(model))
	}})
	return cmd
}
func writeYAML(w io.Writer, v any) error {
	if _, err := fmt.Fprintln(w, "---"); err != nil {
		return err
	}
	enc := yaml.NewEncoder(w)
	enc.SetIndent(2)
	if err := enc.Encode(v); err != nil {
		_ = enc.Close()
		return err
	}
	return enc.Close()
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}
func selectDevices(c *config.Config, aliases []string) error {
	if len(aliases) == 0 {
		return nil
	}
	selected := map[string]config.Device{}
	for _, alias := range aliases {
		found := false
		for key, d := range c.Devices {
			if strings.EqualFold(key, alias) {
				selected[key] = d
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("unknown device alias %q", alias)
		}
	}
	c.Devices = selected
	return nil
}
func input(cmd *cobra.Command, path string, args []string, max int64) ([]byte, error) {
	var r io.Reader = cmd.InOrStdin()
	if len(args) > 0 {
		if path != "-" {
			return nil, errors.New("choose a payload argument or --file")
		}
		r = strings.NewReader(args[0])
	} else if path != "-" {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		r = f
	}
	b, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, errors.New("input too large")
	}
	return b, nil
}
