// Package app runs several runners side by side under one context and stops
// them together.
//
// A runner is anything that occupies a goroutine until it is told to stop: an
// HTTP server, a gRPC server, a queue consumer, a scheduler.
//
// The ordinary way down is SIGTERM: main derives its context from
// signal.NotifyContext, every runner sees the cancellation at once and Run
// returns when the last of them has drained. Signals stay in main so an App
// started from a test, or under another process manager, does not fight for
// SIGTERM.
//
// The other way down is a runner returning by itself, which cancels the rest.
// So every runner here must be long-running: a one-shot task, a migration or a
// warm-up runs before Run, never as a runner, because its clean return would
// take the process with it.
package app

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"slices"
	"sync"
)

// Runner is one long-running part of the process.
type Runner interface {
	// Run occupies its goroutine until ctx is cancelled or it fails. It
	// returns nil when it stopped cleanly on that cancellation.
	Run(ctx context.Context) error
}

// RunnerFunc adapts a plain function to Runner.
type RunnerFunc func(ctx context.Context) error

// Run calls f.
func (f RunnerFunc) Run(ctx context.Context) error { return f(ctx) }

// RunnerFrom builds a Runner from a blocking start and a separate stop, which
// is the shape of everything written before context.Context: a grpc.Server's
// Serve and GracefulStop, a consumer's Consume and Close.
//
// stop must make start return, or the runner never releases its goroutine.
func RunnerFrom(start func() error, stop func()) Runner {
	return RunnerFunc(func(ctx context.Context) error {
		done := make(chan error, 1)
		go func() { done <- start() }()

		select {
		case err := <-done:
			return err
		case <-ctx.Done():
		}

		stop()
		return <-done
	})
}

// Option configures an App.
type Option func(*App)

// WithLogger sets the logger that records which runner stopped and why.
// Defaults to slog.Default().
func WithLogger(log *slog.Logger) Option {
	return func(a *App) { a.log = log }
}

// App supervises a fixed set of runners.
type App struct {
	runners map[string]Runner
	log     *slog.Logger
}

// New creates an App over runners, keyed by the name they are logged under.
// The set is fixed at construction: a runner added to the map afterwards
// would not be supervised.
func New(runners map[string]Runner, opts ...Option) *App {
	a := &App{runners: maps.Clone(runners), log: slog.Default()}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

// Run starts every runner and blocks until all of them have returned. The
// first return cancels the context the others were given, so one failure
// brings the process down rather than leaving it half-serving.
//
// It returns every runner's error joined, in name order, or nil when all
// stopped cleanly.
func (a *App) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	names := slices.Sorted(maps.Keys(a.runners))
	errs := make([]error, len(names))

	var wg sync.WaitGroup
	for i, name := range names {
		runner := a.runners[name]
		wg.Go(func() {
			// Any return, error or not, stops the rest: a runner that
			// exits cleanly on its own has still left the process unable
			// to do the job it was started for.
			defer cancel()

			err := runner.Run(ctx)
			errs[i] = err

			attrs := []any{"runner", name}
			if err != nil {
				attrs = append(attrs, "error", err)
			}
			a.log.Info("runner stopped", attrs...)
		})
	}
	wg.Wait()

	return errors.Join(errs...)
}
