package storage

import (
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
)

// Database shares the recorder's single SQLite writer with transactional outputs.
func (r *Recorder) Database() *sql.DB { return r.db }
func (r *Recorder) Size() int64 {
	var n int64
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if s, e := os.Stat(r.path + suffix); e == nil {
			n += s.Size()
		}
	}
	return n
}
func (r *Recorder) CheckCapacity() error {
	size := r.Size()
	if r.MaxBytes > 0 && size >= r.MaxBytes {
		var free, page int64
		if err := r.db.QueryRow("PRAGMA freelist_count").Scan(&free); err != nil {
			return err
		}
		if err := r.db.QueryRow("PRAGMA page_size").Scan(&page); err != nil {
			return err
		}
		if size-free*page < r.MaxBytes {
			return nil
		}
		return errors.New("storage.max_bytes reached; pending data preserved, increase capacity or explicitly prune delivered data")
	}
	return nil
}
func OpenReadOnly(path string) (*sql.DB, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: abs}
	q := u.Query()
	q.Set("mode", "ro")
	q.Set("_pragma", "query_only(1)")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err = db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}
func migrateOutputs(db *sql.DB) error {
	_, err := db.Exec(`BEGIN;
 CREATE TABLE IF NOT EXISTS vm_samples (
 id INTEGER PRIMARY KEY, target TEXT NOT NULL, identity TEXT NOT NULL,
 timestamp INTEGER NOT NULL, value REAL NOT NULL, payload TEXT NOT NULL,
 priority INTEGER NOT NULL DEFAULT 0, created INTEGER NOT NULL, delivered INTEGER NOT NULL DEFAULT 0,
 UNIQUE(target,identity));
 CREATE INDEX IF NOT EXISTS vm_pending ON vm_samples(target,delivered,priority,id);
 CREATE TABLE IF NOT EXISTS output_rejections (
 id INTEGER PRIMARY KEY, target TEXT NOT NULL, identity TEXT NOT NULL,
 reason TEXT NOT NULL, payload TEXT NOT NULL, created INTEGER NOT NULL,
 UNIQUE(target,identity,reason,payload));
 CREATE TABLE IF NOT EXISTS ha_state (
 device_id TEXT NOT NULL, sensor TEXT NOT NULL, timestamp INTEGER NOT NULL,
 reading TEXT NOT NULL, PRIMARY KEY(device_id,sensor));
 CREATE TABLE IF NOT EXISTS ha_discovery (
 target TEXT NOT NULL, topic TEXT NOT NULL, payload TEXT NOT NULL,
 PRIMARY KEY(target,topic));
 CREATE TABLE IF NOT EXISTS output_status (
 target TEXT PRIMARY KEY, last_success INTEGER NOT NULL DEFAULT 0, last_error_at INTEGER NOT NULL DEFAULT 0, last_error TEXT NOT NULL DEFAULT '');
 CREATE TABLE IF NOT EXISTS backfill_jobs (
 name TEXT PRIMARY KEY, fingerprint TEXT NOT NULL, upper_id INTEGER NOT NULL,
 cursor INTEGER NOT NULL DEFAULT 0, processed INTEGER NOT NULL DEFAULT 0);
 PRAGMA user_version=2;
 COMMIT;`)
	return err
}
