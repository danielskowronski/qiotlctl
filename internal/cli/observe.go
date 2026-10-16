package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/danielskowronski/qiotlctl/internal/diagnostic"
	"github.com/danielskowronski/qiotlctl/internal/domain"
	"github.com/danielskowronski/qiotlctl/internal/health"
	"github.com/danielskowronski/qiotlctl/internal/observer"
	"github.com/spf13/cobra"
)

func configureWatch(cmd *cobra.Command, a *app) {
	var duration time.Duration
	var respond bool
	var aliases []string
	var record, listen, format string
	cmd.Args = cobra.NoArgs
	cmd.Flags().BoolVar(&respond, "respond", false, "Enable automatic replies for selected devices; --respond=false force-disables all replies")
	cmd.Flags().DurationVar(&duration, "duration", 0, "Stop after this time; zero means until interrupted")
	cmd.Flags().StringSliceVar(&aliases, "device", nil, "Only these device aliases (comma separated or repeat)")
	cmd.Flags().StringVar(&record, "record", "", "SQLite capture destination; overrides storage.path")
	cmd.Flags().StringVar(&listen, "listen", "", "HTTP probes and metrics address; empty disables")
	cmd.Flags().StringVar(&format, "format", "json", "Log format: json or text")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		c, err := a.load()
		if err != nil {
			return err
		}
		if err = selectDevices(&c, aliases); err != nil {
			return err
		}
		if cmd.Flags().Changed("respond") {
			c.Runtime.Mode = "observe"
			if respond {
				c.Runtime.Mode = "respond"
			}
			for key, d := range c.Devices {
				d.Responses.Enabled = &respond
				c.Devices[key] = d
			}
		}
		if duration < 0 {
			return errors.New("duration must be nonnegative")
		}
		if format != "json" && format != "text" {
			return errors.New("format must be json or text")
		}
		if cmd.Flags().Changed("record") {
			c.Storage.Path = record
		}
		if a.daemon && c.HTTP.Listen == "" {
			c.HTTP.Listen = ":9090"
		}
		if cmd.Flags().Changed("listen") {
			c.HTTP.Listen = listen
		}
		if err = c.Validate(true); err != nil {
			return err
		}
		ctx, cancel := context.WithCancel(cmd.Context())
		defer cancel()
		if duration > 0 {
			var stop context.CancelFunc
			ctx, stop = context.WithTimeout(ctx, duration)
			defer stop()
		}
		stats := &observer.Stats{}
		stats.InitHealth(c)
		var handler slog.Handler = slog.NewTextHandler(cmd.ErrOrStderr(), nil)
		if format == "json" {
			handler = slog.NewJSONHandler(cmd.ErrOrStderr(), nil)
		}
		reporter := &diagnostic.Reporter{Logger: slog.New(handler), Interval: c.Health.LogInterval.Duration()}
		logCtx, stopLog := context.WithCancel(ctx)
		logDone := make(chan struct{})
		go func() { defer close(logDone); reporter.Run(logCtx, stats.Diagnostics) }()
		defer func() { stopLog(); <-logDone }()
		serverErrors := make(chan error, 1)
		if c.HTTP.Listen != "" {
			listener, e := net.Listen("tcp", c.HTTP.Listen)
			if e != nil {
				return e
			}
			server := &http.Server{Handler: health.Handler(stats), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
			go func() {
				e := server.Serve(listener)
				if e != nil && !errors.Is(e, http.ErrServerClosed) {
					serverErrors <- e
					cancel()
				}
			}()
			defer func() {
				shutdown, stop := context.WithTimeout(context.Background(), 3*time.Second)
				defer stop()
				_ = server.Shutdown(shutdown)
			}()
		}
		err = observer.Run(ctx, c, stats, func(e domain.Event) error { return emitEvent(cmd, e, format) })
		select {
		case serverErr := <-serverErrors:
			if err == nil {
				err = serverErr
			}
		default:
		}
		stopLog()
		<-logDone
		reporter.Emit(stats.Diagnostics.Snapshot(time.Now()))
		reporter.Logger.Info("observer stopped:", "received", stats.Received.Load(), "decoded", stats.Decoded.Load(), "errors", stats.DecodeErrors.Load(), "recorded", stats.Recorded.Load())
		return err
	}
}
func emitEvent(cmd *cobra.Command, e domain.Event, format string) error {
	if format == "json" {
		return writeJSON(cmd.OutOrStdout(), e)
	}
	readings := 0
	for _, s := range e.Samples {
		readings += len(s.Readings)
	}
	_, err := fmt.Fprintf(cmd.OutOrStdout(), "%s %-12s %s %s type=%s samples=%d readings=%d", e.ReceivedAt.Format(time.RFC3339), e.Alias, e.Direction, e.Protocol, e.MessageType, len(e.Samples), readings)
	if err != nil {
		return err
	}
	if e.Error != "" {
		_, err = fmt.Fprintf(cmd.OutOrStdout(), " error=%q", e.Error)
	}
	if err != nil {
		return err
	}
	if len(e.Warnings) > 0 {
		_, err = fmt.Fprintf(cmd.OutOrStdout(), " warnings=%q", e.Warnings)
	}
	if err != nil {
		return err
	}
	for _, s := range e.Samples {
		for _, r := range s.Readings {
			if _, err = fmt.Fprintf(cmd.OutOrStdout(), " %s=%g%s", r.Sensor, r.Value, r.Unit); err != nil {
				return err
			}
		}
	}
	_, err = fmt.Fprintln(cmd.OutOrStdout())
	return err
}
