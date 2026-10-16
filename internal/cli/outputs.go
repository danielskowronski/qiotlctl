package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/danielskowronski/qiotlctl/internal/backfill"
	"github.com/danielskowronski/qiotlctl/internal/config"
	"github.com/danielskowronski/qiotlctl/internal/device"
	"github.com/danielskowronski/qiotlctl/internal/output"
	"github.com/danielskowronski/qiotlctl/internal/storage"
	"github.com/danielskowronski/qiotlctl/internal/transport"
	paho "github.com/eclipse/paho.mqtt.golang"
	"github.com/spf13/cobra"
)

func (a *app) backfillCommand() *cobra.Command {
	parent := &cobra.Command{Use: "backfill", Short: "Resume historical exports without connecting to MQTT"}
	var o backfill.Options
	var statePath, since, until string
	var aliases []string
	var enqueueOnly bool
	cmd := &cobra.Command{Use: "vm", Args: cobra.NoArgs, Short: "Import a SQLite snapshot into VictoriaMetrics", RunE: func(cmd *cobra.Command, _ []string) error {
		c, err := a.load()
		if err != nil {
			return err
		}
		if err = selectDevices(&c, aliases); err != nil {
			return err
		}
		for _, item := range []struct {
			text string
			dest *time.Time
		}{{since, &o.Since}, {until, &o.Until}} {
			if item.text != "" {
				*item.dest, err = time.Parse(time.RFC3339Nano, item.text)
				if err != nil {
					return errors.New("time filters require RFC3339")
				}
			}
		}
		if o.DryRun {
			p, err := backfill.Run(cmd.Context(), c, nil, o, func(line output.VMLine) error { return writeJSON(cmd.OutOrStdout(), line) }, nil)
			_ = writeJSON(cmd.ErrOrStderr(), p)
			return err
		}
		if c.Outputs.VictoriaMetrics.ImportEndpoint == "" {
			return errors.New("configure outputs.victoriametrics.import_endpoint")
		}
		if statePath == "" {
			statePath = c.Storage.Path
		}
		if statePath == "" {
			return errors.New("configure storage.path or --state-db")
		}
		if src, e := os.Stat(o.Path); e == nil {
			if dst, e := os.Stat(statePath); e == nil && os.SameFile(src, dst) {
				return errors.New("source snapshot and state database must be different files")
			}
		}
		recorder, err := storage.OpenRecorder(statePath, false)
		if err != nil {
			return err
		}
		defer recorder.Close()
		recorder.MaxBytes = c.Storage.MaxBytes
		store := output.NewStore(recorder, c)
		store.Backfill = true
		var worker *output.VMWorker
		if !enqueueOnly {
			sender, err := output.NewVMSender(c.Outputs.VictoriaMetrics)
			if err != nil {
				return err
			}
			defer sender.Close()
			worker = &output.VMWorker{Store: store, Sender: sender, Stats: &output.Stats{}}
		}
		progress := func(p backfill.Progress) error {
			// One batch per source page bounds pending work and leaves other drainers free.
			if worker != nil {
				if _, err := worker.Flush(cmd.Context()); err != nil {
					return err
				}
			}
			return writeJSON(cmd.ErrOrStderr(), p)
		}
		p, err := backfill.Run(cmd.Context(), c, store, o, nil, progress)
		_ = writeJSON(cmd.ErrOrStderr(), p)
		if err != nil {
			return err
		}
		if worker != nil {
			return drainVM(cmd.Context(), worker)
		}
		return nil
	}}
	cmd.Flags().StringVar(&o.Path, "sqlite", "", "Read-only source snapshot")
	cmd.Flags().StringVar(&o.Source, "source", "auto", "auto: Python readings when present, otherwise raw; or readings/raw")
	cmd.Flags().StringVar(&o.Job, "job", "history", "Persistent job name; reuse to resume with identical source/filters")
	cmd.Flags().StringVar(&statePath, "state-db", "", "Queue/checkpoint database (defaults to storage.path)")
	cmd.Flags().StringSliceVar(&aliases, "device", nil, "Only these device aliases")
	cmd.Flags().StringVar(&since, "since", "", "Inclusive measurement time, RFC3339")
	cmd.Flags().StringVar(&until, "until", "", "Exclusive measurement time, RFC3339")
	cmd.Flags().StringVar(&o.MessageType, "type", "", "Raw-source message type filter")
	cmd.Flags().Int64Var(&o.AfterID, "after-id", 0, "Start after this source row ID (part of job identity)")
	cmd.Flags().IntVar(&o.Limit, "limit", 0, "Maximum source rows this run; zero means all")
	cmd.Flags().BoolVar(&o.DryRun, "dry-run", false, "Print eligible VM JSON lines; no writes or network")
	cmd.Flags().BoolVar(&enqueueOnly, "enqueue-only", false, "Only queue samples; deliver later with outputs drain-vm or the daemon")
	parent.AddCommand(cmd)
	return parent
}
func drainVM(ctx context.Context, w *output.VMWorker) error {
	for ctx.Err() == nil {
		n, err := w.Flush(ctx)
		if err != nil {
			return err
		}
		if n == 0 {
			return nil
		}
	}
	return ctx.Err()
}
func (a *app) outputsCommand() *cobra.Command {
	parent := &cobra.Command{Use: "outputs", Short: "Inspect durable output work and deliver pending VM samples"}
	var path string
	parent.PersistentFlags().StringVar(&path, "sqlite", "", "Output state database; defaults to storage.path")
	load := func() (config.Config, string, error) {
		c, err := a.load()
		p := path
		if p == "" {
			p = c.Storage.Path
		}
		if err == nil && p == "" {
			err = errors.New("configure storage.path or --sqlite")
		}
		return c, p, err
	}
	parent.AddCommand(&cobra.Command{Use: "status", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		c, p, err := load()
		if err != nil {
			return err
		}
		db, err := storage.OpenReadOnly(p)
		if err != nil {
			return err
		}
		defer db.Close()
		rows, err := db.QueryContext(cmd.Context(), `SELECT target,count(*),sum(delivered=0),coalesce(min(CASE WHEN delivered=0 THEN created END),0) FROM vm_samples GROUP BY target`)
		if err != nil {
			return err
		}
		var targets []map[string]any
		for rows.Next() {
			var target string
			var total, pending, oldest int64
			if err = rows.Scan(&target, &total, &pending, &oldest); err != nil {
				rows.Close()
				return err
			}
			targets = append(targets, map[string]any{"target": target, "samples": total, "pending": pending, "oldest_pending_unix": oldest})
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		var rejected int64
		if err = db.QueryRowContext(cmd.Context(), `SELECT count(*) FROM output_rejections`).Scan(&rejected); err != nil {
			return err
		}
		rows, err = db.QueryContext(cmd.Context(), `SELECT target,last_success,last_error_at,last_error FROM output_status ORDER BY target`)
		if err != nil {
			return err
		}
		var delivery []map[string]any
		for rows.Next() {
			var target, message string
			var success, failure int64
			if err = rows.Scan(&target, &success, &failure, &message); err != nil {
				rows.Close()
				return err
			}
			delivery = append(delivery, map[string]any{"target": target, "last_success": success, "last_error_at": failure, "last_error": message})
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		rows, err = db.QueryContext(cmd.Context(), `SELECT name,cursor,upper_id,processed FROM backfill_jobs ORDER BY name`)
		if err != nil {
			return err
		}
		defer rows.Close()
		var jobs []map[string]any
		for rows.Next() {
			var name string
			var cursor, upper, processed int64
			if err = rows.Scan(&name, &cursor, &upper, &processed); err != nil {
				return err
			}
			jobs = append(jobs, map[string]any{"name": name, "cursor": cursor, "upper_id": upper, "processed": processed, "complete": cursor >= upper})
		}
		if err = rows.Err(); err != nil {
			return err
		}
		return writeJSON(cmd.OutOrStdout(), map[string]any{"victoriametrics": targets, "rejections": rejected, "backfill_jobs": jobs, "delivery": delivery, "configured_vm_target": output.VMTarget(c.Outputs.VictoriaMetrics)})
	}})
	parent.AddCommand(&cobra.Command{Use: "drain-vm", Args: cobra.NoArgs, Short: "Deliver pending samples for the configured VM endpoint/prefix; stop on first failure", RunE: func(cmd *cobra.Command, _ []string) error {
		c, p, err := load()
		if err != nil {
			return err
		}
		if c.Outputs.VictoriaMetrics.ImportEndpoint == "" {
			return errors.New("VM endpoint required")
		}
		r, err := storage.OpenRecorder(p, false)
		if err != nil {
			return err
		}
		defer r.Close()
		sender, err := output.NewVMSender(c.Outputs.VictoriaMetrics)
		if err != nil {
			return err
		}
		defer sender.Close()
		stats := &output.Stats{}
		err = drainVM(cmd.Context(), &output.VMWorker{Store: output.NewStore(r, c), Sender: sender, Stats: stats})
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "VM samples accepted: %d\n", stats.VMSent.Load())
		return err
	}})
	var after int64
	var limit int
	rejected := &cobra.Command{Use: "rejections", Args: cobra.NoArgs, Short: "Inspect invalid/conflicting samples; never silently discarded", RunE: func(cmd *cobra.Command, _ []string) error {
		if after < 0 || limit < 1 || limit > 10000 {
			return errors.New("after-id must be nonnegative; limit must be 1..10000")
		}
		_, p, err := load()
		if err != nil {
			return err
		}
		db, err := storage.OpenReadOnly(p)
		if err != nil {
			return err
		}
		defer db.Close()
		rows, err := db.QueryContext(cmd.Context(), `SELECT id,target,reason,payload FROM output_rejections WHERE id>? ORDER BY id LIMIT ?`, after, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			var target, reason string
			var payload json.RawMessage
			if err = rows.Scan(&id, &target, &reason, &payload); err != nil {
				return err
			}
			if err = writeJSON(cmd.OutOrStdout(), map[string]any{"id": id, "target": target, "reason": reason, "sample": payload}); err != nil {
				return err
			}
		}
		return rows.Err()
	}}
	rejected.Flags().Int64Var(&after, "after-id", 0, "Resume after rejection ID")
	rejected.Flags().IntVar(&limit, "limit", 100, "Maximum rejection rows")
	parent.AddCommand(rejected)
	var before string
	prune := &cobra.Command{Use: "prune-vm", Args: cobra.NoArgs, Short: "Delete delivered VM rows before a delivery time; preserves pending work, captures and rejections", RunE: func(cmd *cobra.Command, _ []string) error {
		t, err := time.Parse(time.RFC3339, before)
		if err != nil {
			return errors.New("--before requires RFC3339 delivery time")
		}
		_, p, err := load()
		if err != nil {
			return err
		}
		r, err := storage.OpenRecorder(p, false)
		if err != nil {
			return err
		}
		defer r.Close()
		res, err := r.Database().ExecContext(cmd.Context(), `DELETE FROM vm_samples WHERE delivered>0 AND delivered<?`, t.Unix())
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Removed %d delivered rows; deduplication history for those rows was also removed. SQLite can reuse freed pages.\n", n)
		return err
	}}
	prune.Flags().StringVar(&before, "before", "", "Exclusive delivery time, RFC3339")
	parent.AddCommand(prune)
	return parent
}
func (a *app) haCommand() *cobra.Command {
	parent := &cobra.Command{Use: "ha", Short: "Preview Home Assistant discovery and manage owned discovery topics"}
	var aliases []string
	preview := &cobra.Command{Use: "preview", Args: cobra.NoArgs, Short: "Render discovery JSON without connecting or publishing", RunE: func(cmd *cobra.Command, _ []string) error {
		c, err := a.load()
		if err != nil {
			return err
		}
		if err = selectDevices(&c, aliases); err != nil {
			return err
		}
		c.Outputs.HomeAssistant.Enabled = true
		if err = c.Validate(false); err != nil {
			return err
		}
		return writeJSON(cmd.OutOrStdout(), output.Preview(c))
	}}
	preview.Flags().StringSliceVar(&aliases, "device", nil, "Only these aliases")
	parent.AddCommand(preview)
	var path string
	var publish, all bool
	cleanup := &cobra.Command{Use: "cleanup", Args: cobra.NoArgs, Short: "Preview obsolete owned discovery removal; --publish explicitly sends empty retained payloads", RunE: func(cmd *cobra.Command, _ []string) error {
		c, err := a.load()
		if err != nil {
			return err
		}
		if path == "" {
			path = c.Storage.Path
		}
		if path == "" {
			return errors.New("storage.path or --sqlite required")
		}
		db, err := storage.OpenReadOnly(path)
		if err != nil {
			return err
		}
		defer db.Close()
		current := map[string]bool{}
		for _, p := range output.Preview(c) {
			current[p.Topic] = true
		}
		rows, err := db.QueryContext(cmd.Context(), `SELECT device_id,sensor FROM ha_state`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id, sensor string
			if err = rows.Scan(&id, &sensor); err != nil {
				rows.Close()
				return err
			}
			for _, d := range c.Devices {
				if d.ID == id && d.HAActive() && (len(device.Lookup(d.Model).Sensors) == 0 || output.Allowed(d.Model, sensor)) {
					if p, ok := output.Discovery(c, d, sensor); ok {
						current[p.Topic] = true
					}
				}
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		rows, err = db.QueryContext(cmd.Context(), `SELECT topic FROM ha_discovery WHERE target=? ORDER BY topic`, output.HATarget(c))
		if err != nil {
			return err
		}
		var topics []string
		for rows.Next() {
			var topic string
			if err = rows.Scan(&topic); err != nil {
				rows.Close()
				return err
			}
			if all || !current[topic] {
				topics = append(topics, topic)
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if !publish {
			for _, topic := range topics {
				if err = writeJSON(cmd.OutOrStdout(), map[string]any{"topic": topic, "retain": true, "qos": 1, "payload": ""}); err != nil {
					return err
				}
			}
			return nil
		}
		if len(topics) == 0 {
			return nil
		}
		opts, err := transport.Options(c.MQTT)
		if err != nil {
			return err
		}
		opts.SetClientID(opts.ClientID + "-ha-cleanup").SetConnectRetry(false)
		client := paho.NewClient(opts)
		defer client.Disconnect(100)
		if err = transport.Wait(cmd.Context(), client.Connect(), "HA cleanup connection"); err != nil {
			return err
		}
		r, err := storage.OpenRecorder(path, false)
		if err != nil {
			return err
		}
		defer r.Close()
		pub := output.MQTTPublisher{Client: client}
		for _, topic := range topics {
			if err = pub.Publish(cmd.Context(), topic, 1, true, nil); err != nil {
				return err
			}
			if _, err = r.Database().ExecContext(cmd.Context(), `DELETE FROM ha_discovery WHERE target=? AND topic=?`, output.HATarget(c), topic); err != nil {
				return err
			}
		}
		return writeJSON(cmd.OutOrStdout(), map[string]any{"removed": len(topics)})
	}}
	cleanup.Flags().StringVar(&path, "sqlite", "", "Manifest database; defaults to storage.path")
	cleanup.Flags().BoolVar(&all, "all", false, "Remove all discovery owned by this broker/prefix/bridge, including current entities")
	cleanup.Flags().BoolVar(&publish, "publish", false, "Send the previewed removals to the broker")
	parent.AddCommand(cleanup)
	return parent
}
