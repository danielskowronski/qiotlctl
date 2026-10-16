package output

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/danielskowronski/qiotlctl/internal/config"
	"github.com/danielskowronski/qiotlctl/internal/diagnostic"
	"github.com/danielskowronski/qiotlctl/internal/transport"
)

type VMSender struct {
	Config config.VM
	Client *http.Client
}

func NewVMSender(c config.VM) (*VMSender, error) {
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: c.InsecureSkipVerify}
	if c.CAFile != "" {
		b, err := os.ReadFile(c.CAFile)
		if err != nil {
			return nil, errors.New("cannot read VM CA file")
		}
		pool, err := x509.SystemCertPool()
		if err != nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(b) {
			return nil, errors.New("VM CA file contains no certificates")
		}
		tlsConfig.RootCAs = pool
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = tlsConfig
	client := &http.Client{Transport: transport, Timeout: c.Timeout.Duration(), CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &VMSender{c, client}, nil
}
func (s *VMSender) Close() { s.Client.CloseIdleConnections() }
func (s *VMSender) Send(ctx context.Context, payload []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.Config.ImportEndpoint, bytes.NewReader(payload))
	if err != nil {
		return errors.New("invalid VM endpoint")
	}
	req.Header.Set("Content-Type", "application/json")
	if s.Config.Username != "" || s.Config.Password != "" {
		req.SetBasicAuth(s.Config.Username, s.Config.Password)
	}
	resp, err := s.Client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("VM %s", transport.NetworkReason(err))
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != 200 && resp.StatusCode != 204 {
		return errors.New(HTTPReason(resp.StatusCode))
	}
	return nil
}

type VMWorker struct {
	Store  *Store
	Sender *VMSender
	Stats  *Stats
}

func (w *VMWorker) Flush(ctx context.Context) (count int, resultErr error) {
	defer func() {
		if ctx.Err() == nil && (count > 0 || resultErr != nil) {
			var safe error
			if resultErr != nil {
				safe = errors.New(safeDeliveryError(resultErr))
			}
			w.Store.DeliveryStatus(ctx, w.Store.Target, safe)
		}
	}()
	db := w.Store.Recorder.Database()
	rows, err := db.QueryContext(ctx, `SELECT id,payload FROM vm_samples WHERE target=? AND delivered=0 ORDER BY priority,id LIMIT ?`, w.Store.Target, w.Sender.Config.BatchSize)
	if err != nil {
		return 0, err
	}
	var ids []int64
	var payload bytes.Buffer
	for rows.Next() {
		var id int64
		var line string
		if err = rows.Scan(&id, &line); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
		payload.WriteString(line)
		payload.WriteByte('\n')
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(ids) == 0 {
		return 0, err
	}
	if err = w.Sender.Send(ctx, payload.Bytes()); err != nil {
		return 0, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	for _, id := range ids {
		if _, err = tx.ExecContext(ctx, `UPDATE vm_samples SET delivered=?,payload='' WHERE id=?`, time.Now().Unix(), id); err != nil {
			return 0, err
		}
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	w.Stats.VMSent.Add(uint64(len(ids)))
	return len(ids), nil
}
func (w *VMWorker) Run(ctx context.Context) {
	backoff := time.Second
	var lastEndpoint time.Time
	endpointOK, deliveryFailed := false, false
	r := w.Stats.Diagnostics
	r.Set("vm.delivery", diagnostic.Warning, "no sensor batch delivered in this process yet; endpoint acceptance is checked separately", "", nil)
	for ctx.Err() == nil {
		if time.Since(lastEndpoint) >= w.Store.Config.Health.CheckInterval.Duration() {
			probe, stop := context.WithTimeout(ctx, w.Store.Config.Health.CheckTimeout.Duration())
			err := w.Sender.Send(probe, nil)
			stop()
			lastEndpoint = time.Now()
			endpointOK = err == nil
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				r.Set("vm.endpoint", diagnostic.Failed, err.Error(), "check VM credentials, import route, TLS and connectivity; empty probe sends no samples", nil)
			} else {
				r.Set("vm.endpoint", diagnostic.OK, "zero-sample import accepted; this verifies endpoint access, not sensor ingestion", "", nil)
			}
		}
		n, err := w.Flush(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			deliveryFailed = true
			r.Set("vm.delivery", diagnostic.Failed, safeDeliveryError(err), "check VM route/authentication and capture database; batch remains pending", nil)
		} else if n > 0 {
			deliveryFailed = false
			endpointOK = true
			lastEndpoint = time.Now()
			r.Set("vm.delivery", diagnostic.OK, "sensor batch accepted and local delivery checkpoint committed", "", map[string]int64{"samples": int64(n)})
			r.Set("vm.endpoint", diagnostic.OK, "import endpoint accepted a sensor batch", "", nil)
		}
		backlogOK := w.Store.CheckBacklog(ctx, w.Stats)
		w.Stats.VMHealthy.Store(endpointOK && !deliveryFailed && backlogOK)
		delay := w.Sender.Config.FlushInterval.Duration()
		if err != nil {
			w.Stats.VMErrors.Add(1)
			delay = backoff
			backoff = min(backoff*2, w.Sender.Config.RetryMax.Duration())
			delay = delay/2 + time.Duration(rand.Int64N(int64(delay/2)+1))
		} else {
			backoff = time.Second
			if n > 0 {
				continue
			}
		}
		if !Wait(ctx, delay) {
			return
		}
	}
}

// SQLite and transport implementations can attach arbitrary details. Only our
// fixed transport messages are suitable for routine console/status output.
func safeDeliveryError(err error) string {
	var e *url.Error
	if errors.As(err, &e) {
		return "VM " + transport.NetworkReason(err)
	}
	s := err.Error()
	if strings.HasPrefix(s, "HTTP ") || strings.HasPrefix(s, "VM ") {
		return s
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "VM request timed out"
	}
	return "VM queue read or delivery checkpoint failed"
}
func Wait(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
