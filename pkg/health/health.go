// Package health serves liveness and readiness probes over plain net/http.
//
// Live answers for the process itself; Ready answers for the dependencies it
// needs to serve traffic. Both are http.HandlerFunc, so they mount on whatever
// router the service already uses.
package health

import (
	"context"
	"encoding/json"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"sync"
	"time"
)

// DefaultTimeout bounds a readiness probe so a hung dependency turns into a
// 503 instead of a hung probe.
const DefaultTimeout = 2 * time.Second

// Checker reports whether one dependency can serve traffic.
type Checker interface {
	// Check returns a non-nil error when the dependency is unavailable.
	Check(ctx context.Context) error
}

// CheckerFunc adapts a plain function to Checker, so a client's own probe
// method mounts without a wrapper type: CheckerFunc(pool.Ping).
type CheckerFunc func(ctx context.Context) error

// Check calls f.
func (f CheckerFunc) Check(ctx context.Context) error { return f(ctx) }

// Option configures a readiness handler.
type Option func(*ready)

// WithTimeout bounds how long every check gets, together. Defaults to
// DefaultTimeout.
func WithTimeout(d time.Duration) Option {
	return func(r *ready) { r.timeout = d }
}

// WithLogger sets the logger that records which check failed and why.
// Defaults to slog.Default().
func WithLogger(log *slog.Logger) Option {
	return func(r *ready) { r.log = log }
}

// response is the probe body. Failed names the checks that reported an error;
// their error text stays in the log, since a probe endpoint is often reachable
// by more callers than the operator expects.
type response struct {
	Status string   `json:"status"`
	Failed []string `json:"failed,omitempty"`
}

// Live answers 200 as long as the process can serve HTTP. It takes no
// dependency: a liveness probe that fails on a broken database asks the
// orchestrator to restart a process that restarting will not fix.
func Live() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, response{Status: "ok"})
	}
}

// Ready answers 200 when every check passes, 503 otherwise, naming the checks
// that failed. Checks run concurrently under one deadline. The keys of checks
// name the dependencies in the response and in the log.
func Ready(checks map[string]Checker, opts ...Option) http.HandlerFunc {
	r := &ready{
		checks:  maps.Clone(checks),
		timeout: DefaultTimeout,
		log:     slog.Default(),
	}
	for _, opt := range opts {
		opt(r)
	}
	return r.serve
}

type ready struct {
	checks  map[string]Checker
	timeout time.Duration
	log     *slog.Logger
}

func (rd *ready) serve(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), rd.timeout)
	defer cancel()

	type outcome struct {
		name string
		err  error
	}
	results := make(chan outcome, len(rd.checks))

	var wg sync.WaitGroup
	for name, c := range rd.checks {
		wg.Go(func() {
			results <- outcome{name: name, err: c.Check(ctx)}
		})
	}
	wg.Wait()
	close(results)

	var failed []string
	for res := range results {
		if res.err == nil {
			continue
		}
		rd.log.LogAttrs(ctx, slog.LevelError, "readiness check failed",
			slog.String("check", res.name),
			slog.String("error", res.err.Error()),
		)
		failed = append(failed, res.name)
	}

	if len(failed) > 0 {
		slices.Sort(failed)
		writeJSON(w, http.StatusServiceUnavailable, response{Status: "unavailable", Failed: failed})
		return
	}
	writeJSON(w, http.StatusOK, response{Status: "ok"})
}

func writeJSON(w http.ResponseWriter, status int, body response) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
