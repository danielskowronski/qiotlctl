package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/danielskowronski/qiotlctl/internal/domain"
	"github.com/danielskowronski/qiotlctl/internal/observer"
	"github.com/danielskowronski/qiotlctl/internal/storage"
	"github.com/spf13/cobra"
)

type replaySummary struct {
	Captures     uint64            `json:"captures"`
	Errors       uint64            `json:"errors"`
	Warnings     uint64            `json:"messages_with_warnings"`
	Samples      uint64            `json:"samples"`
	LastID       int64             `json:"last_id"`
	Devices      map[string]uint64 `json:"devices"`
	MessageTypes map[string]uint64 `json:"message_types"`
	Readings     map[string]uint64 `json:"readings"`
	TimeQuality  map[string]uint64 `json:"sample_time_quality"`
}

func (a *app) replayCommand() *cobra.Command {
	var path, since, until, checkpoint, messageType, format string
	var aliases []string
	var after int64
	var limit int
	var summaryOnly bool
	cmd := &cobra.Command{Use: "replay", Short: "Decode a recorder or legacy SQLite database read-only; no broker connection", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if path == "" {
			return errors.New("--sqlite is required")
		}
		c, err := a.load()
		if err != nil {
			return err
		}
		if err = selectDevices(&c, aliases); err != nil {
			return err
		}
		routes := observer.Routes(c)
		if len(routes) == 0 {
			return errors.New("replay requires configured device topics")
		}
		if format != "json" && format != "text" {
			return errors.New("format must be json or text")
		}
		opts := storage.ReplayOptions{AfterID: after, Limit: limit}
		if since != "" {
			opts.Since, err = time.Parse(time.RFC3339Nano, since)
			if err != nil {
				return errors.New("--since requires RFC3339")
			}
		}
		if until != "" {
			opts.Until, err = time.Parse(time.RFC3339Nano, until)
			if err != nil {
				return errors.New("--until requires RFC3339")
			}
		}
		if !opts.Since.IsZero() && !opts.Until.IsZero() && !opts.Since.Before(opts.Until) {
			return errors.New("--since must precede --until")
		}
		if checkpoint != "" && !cmd.Flags().Changed("after-id") {
			b, e := os.ReadFile(checkpoint)
			if e == nil {
				var saved struct {
					LastID int64  `json:"last_id"`
					Source string `json:"source"`
				}
				if json.Unmarshal(b, &saved) != nil {
					return errors.New("invalid replay checkpoint")
				}
				abs, _ := filepath.Abs(path)
				if saved.Source != abs {
					return errors.New("checkpoint belongs to another database")
				}
				opts.AfterID = saved.LastID
			} else if !os.IsNotExist(e) {
				return e
			}
		}
		for topic := range routes {
			opts.Topics = append(opts.Topics, topic)
		}
		s := replaySummary{LastID: opts.AfterID, Devices: map[string]uint64{}, MessageTypes: map[string]uint64{}, Readings: map[string]uint64{}, TimeQuality: map[string]uint64{}}
		err = storage.Replay(cmd.Context(), path, opts, func(capture domain.Capture) error {
			e := observer.DecodeCapture(routes, capture)
			s.Captures++
			s.LastID = capture.ID
			s.Devices[e.Alias]++
			s.MessageTypes[e.Protocol+":"+e.MessageType+":"+string(e.Direction)]++
			if e.Error != "" {
				s.Errors++
			}
			if len(e.Warnings) > 0 {
				s.Warnings++
			}
			s.Samples += uint64(len(e.Samples))
			for _, sample := range e.Samples {
				s.TimeQuality[sample.TimeQuality]++
				for _, r := range sample.Readings {
					s.Readings[r.Sensor]++
				}
			}
			if !summaryOnly && (messageType == "" || messageType == e.MessageType) {
				return emitEvent(cmd, e, format)
			}
			return nil
		})
		// Checkpoints advance only after a completed, error-free decode pass.
		if err == nil && s.Errors == 0 && checkpoint != "" {
			abs, _ := filepath.Abs(path)
			err = saveCheckpoint(checkpoint, struct {
				Source string `json:"source"`
				LastID int64  `json:"last_id"`
			}{abs, s.LastID})
		}
		if summaryOnly {
			if e := writeJSON(cmd.OutOrStdout(), s); err == nil {
				err = e
			}
		}
		if err != nil {
			return err
		}
		if s.Errors > 0 {
			return fmt.Errorf("%d captures could not be decoded", s.Errors)
		}
		return nil
	}}
	cmd.Flags().StringVar(&path, "sqlite", "", "SQLite file (new or Python recorder schema)")
	cmd.Flags().StringSliceVar(&aliases, "device", nil, "Only these aliases")
	cmd.Flags().Int64Var(&after, "after-id", 0, "Resume after this raw capture ID")
	cmd.Flags().IntVar(&limit, "limit", 0, "Maximum matching captures to process; zero means all")
	cmd.Flags().StringVar(&since, "since", "", "Inclusive receive time, RFC3339")
	cmd.Flags().StringVar(&until, "until", "", "Exclusive receive time, RFC3339")
	cmd.Flags().StringVar(&messageType, "type", "", "Only display this message type; summary/checkpoint still include all processed captures")
	cmd.Flags().StringVar(&checkpoint, "checkpoint", "", "Read/write an atomic progress file; reuse with identical filters")
	cmd.Flags().BoolVar(&summaryOnly, "summary", false, "Print counts instead of each decoded event")
	cmd.Flags().StringVar(&format, "format", "json", "Event format: json or text")
	return cmd
}
func saveCheckpoint(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".qiotl-checkpoint-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(append(b, '\n')); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
