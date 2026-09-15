package health_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/menems/got-tk/pkg/health"
)

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

// blocking waits for the deadline the handler set, as a real client would.
func blocking(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

func failing(msg string) health.CheckerFunc {
	return func(context.Context) error { return errors.New(msg) }
}

func passing(context.Context) error { return nil }

func TestLive(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	health.Live()(rec, httptest.NewRequest(http.MethodGet, "/healthz/live", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want %q", got, "application/json")
	}
	if status, _ := decode(t, rec); status != "ok" {
		t.Errorf("status field = %q, want %q", status, "ok")
	}
}

func TestReady(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		checks     map[string]health.Checker
		timeout    time.Duration
		wantStatus int
		wantBody   string
		wantFailed []string
	}{
		{
			name:       "no checks",
			checks:     nil,
			wantStatus: http.StatusOK,
			wantBody:   "ok",
		},
		{
			name:       "all pass",
			checks:     map[string]health.Checker{"postgres": health.CheckerFunc(passing), "redis": health.CheckerFunc(passing)},
			wantStatus: http.StatusOK,
			wantBody:   "ok",
		},
		{
			name:       "one fails",
			checks:     map[string]health.Checker{"postgres": failing("connection refused"), "redis": health.CheckerFunc(passing)},
			wantStatus: http.StatusServiceUnavailable,
			wantBody:   "unavailable",
			wantFailed: []string{"postgres"},
		},
		{
			name:       "several fail, named in order",
			checks:     map[string]health.Checker{"redis": failing("down"), "postgres": failing("down"), "kafka": failing("down")},
			wantStatus: http.StatusServiceUnavailable,
			wantBody:   "unavailable",
			wantFailed: []string{"kafka", "postgres", "redis"},
		},
		{
			name:       "check exceeding the deadline fails",
			checks:     map[string]health.Checker{"postgres": health.CheckerFunc(blocking)},
			timeout:    time.Millisecond,
			wantStatus: http.StatusServiceUnavailable,
			wantBody:   "unavailable",
			wantFailed: []string{"postgres"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			opts := []health.Option{health.WithLogger(discard)}
			if tt.timeout != 0 {
				opts = append(opts, health.WithTimeout(tt.timeout))
			}

			rec := httptest.NewRecorder()
			health.Ready(tt.checks, opts...)(rec, httptest.NewRequest(http.MethodGet, "/healthz/ready", nil))

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			status, failed := decode(t, rec)
			if status != tt.wantBody {
				t.Errorf("status field = %q, want %q", status, tt.wantBody)
			}
			if !slices.Equal(failed, tt.wantFailed) {
				t.Errorf("failed = %v, want %v", failed, tt.wantFailed)
			}
		})
	}
}

// TestReadyIgnoresLaterMapWrites pins that Ready captures the checks it was
// given: a caller that keeps the map cannot add a dependency behind the probe.
func TestReadyIgnoresLaterMapWrites(t *testing.T) {
	t.Parallel()

	checks := map[string]health.Checker{"postgres": health.CheckerFunc(passing)}
	handler := health.Ready(checks, health.WithLogger(discard))
	checks["redis"] = failing("down")

	rec := httptest.NewRecorder()
	handler(rec, httptest.NewRequest(http.MethodGet, "/healthz/ready", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) (status string, failed []string) {
	t.Helper()

	var body struct {
		Status string   `json:"status"`
		Failed []string `json:"failed"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	return body.Status, body.Failed
}
