// Package app runs several engines side by side under one context and stops
// them together.
//
// An engine is anything that occupies a goroutine until it is told to stop: an
// HTTP server, a gRPC server, a queue consumer, a scheduler.
//
// The ordinary way down is SIGTERM: main derives its context from
// signal.NotifyContext, every engine sees the cancellation at once and Run
// returns when the last of them has drained. Signals stay in main so an App
// started from a test, or under another process manager, does not fight for
// SIGTERM.
//
// The other way down is an engine returning by itself, which cancels the rest.
// So every engine here must be long-running: a one-shot task, a migration or a
// warm-up runs before Run, never as an engine, because its clean return would
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

// Engine is one long-running part of the process.
type Engine interface {
	// Run occupies its goroutine until ctx is cancelled or it fails. It
	// returns nil when it stopped cleanly on that cancellation.
	Run(ctx context.Context) error
}

// EngineFunc adapts a plain function to Engine.
type EngineFunc func(ctx context.Context) error

// Run calls f.
func (f EngineFunc) Run(ctx context.Context) error { return f(ctx) }

// StartStop adapts an engine whose lifecycle is a blocking start and a
// separate stop, which is the shape of everything written before
// context.Context: a grpc.Server's Serve and GracefulStop, a consumer's
// Consume and Close.
//
// stop must make start return, or the engine never releases its goroutine.
func StartStop(start func() error, stop func()) Engine {
	return EngineFunc(func(ctx context.Context) error {
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

// WithLogger sets the logger that records which engine stopped and why.
// Defaults to slog.Default().
func WithLogger(log *slog.Logger) Option {
	return func(a *App) { a.log = log }
}

// App supervises a fixed set of engines.
type App struct {
	engines map[string]Engine
	log     *slog.Logger
}

// New creates an App over engines, keyed by the name they are logged under.
// The set is fixed at construction: an engine added to the map afterwards
// would not be supervised.
func New(engines map[string]Engine, opts ...Option) *App {
	a := &App{engines: maps.Clone(engines), log: slog.Default()}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

// Run starts every engine and blocks until all of them have returned. The
// first return cancels the context the others were given, so one failure
// brings the process down rather than leaving it half-serving.
//
// It returns every engine's error joined, in name order, or nil when all
// stopped cleanly.
func (a *App) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	names := slices.Sorted(maps.Keys(a.engines))
	errs := make([]error, len(names))

	var wg sync.WaitGroup
	for i, name := range names {
		engine := a.engines[name]
		wg.Go(func() {
			// Any return, error or not, stops the rest: an engine that
			// exits cleanly on its own has still left the process unable
			// to do the job it was started for.
			defer cancel()

			err := engine.Run(ctx)
			errs[i] = err

			attrs := []any{"engine", name}
			if err != nil {
				attrs = append(attrs, "error", err)
			}
			a.log.Info("engine stopped", attrs...)
		})
	}
	wg.Wait()

	return errors.Join(errs...)
}
