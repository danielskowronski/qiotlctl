package doctor

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/danielskowronski/qiotlctl/internal/config"
	"github.com/danielskowronski/qiotlctl/internal/diagnostic"
	"github.com/danielskowronski/qiotlctl/internal/output"
	"github.com/danielskowronski/qiotlctl/internal/storage"
)

func TestReadOnlyStorageInspection(t *testing.T) {
	c, err := config.Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	c.Storage.Path = filepath.Join(t.TempDir(), "journal.sqlite")
	c.Outputs.VictoriaMetrics.Enabled = true
	c.Outputs.VictoriaMetrics.ImportEndpoint = "http://127.0.0.1:1/api/v1/import"
	r := diagnostic.New()
	inspectStorage(context.Background(), c, r)
	if _, err := os.Stat(c.Storage.Path); !os.IsNotExist(err) {
		t.Fatal("doctor created journal", err)
	}
	recorder, err := storage.OpenRecorder(c.Storage.Path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer recorder.Close()
	db := recorder.Database()
	target := output.VMTarget(c.Outputs.VictoriaMetrics)
	_, err = db.Exec(`INSERT INTO vm_samples(target,identity,timestamp,value,payload,created) VALUES(?, 'test', 1, 600, 'private payload', ?)`, target, time.Now().Add(-time.Hour).Unix())
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO output_status(target,last_error_at,last_error) VALUES(?,1,'password=never-print')`, target)
	if err != nil {
		t.Fatal(err)
	}
	inspectStorage(context.Background(), c, r)
	for _, check := range r.Snapshot(time.Now()).Checks {
		if check.ID == "vm.backlog" && (check.Status != diagnostic.Failed || check.Counts["pending"] != 1) {
			t.Fatal(check)
		}
		if check.ID == "vm.delivery" && check.Reason != "last sensor delivery failed" {
			t.Fatal(check)
		}
	}
	var payload string
	if err = db.QueryRow("SELECT payload FROM vm_samples WHERE delivered=0").Scan(&payload); err != nil || payload != "private payload" {
		t.Fatal("doctor altered queue", err)
	}
}
