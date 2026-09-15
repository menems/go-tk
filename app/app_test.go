package app_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"

	"github.com/menems/got-tk/app"
)

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

var (
	errBoom = errors.New("boom")
	errBust = errors.New("bust")
)

// serving blocks until the app stops it, like a real server would.
func serving(started chan<- struct{}) app.EngineFunc {
	return func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		return nil
	}
}

func failing(err error) app.EngineFunc {
	return func(context.Context) error { return err }
}

func run(t *testing.T, ctx context.Context, engines map[string]app.Engine) error {
	t.Helper()
	return app.New(engines, app.WithLogger(discard)).Run(ctx)
}

func TestRunNoEngines(t *testing.T) {
	t.Parallel()

	if err := run(t, context.Background(), nil); err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
}

func TestRunStopsEveryEngineOnCancel(t *testing.T) {
	t.Parallel()

	httpStarted, grpcStarted := make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- run(t, ctx, map[string]app.Engine{
			"http": serving(httpStarted),
			"grpc": serving(grpcStarted),
		})
	}()

	<-httpStarted
	<-grpcStarted
	cancel()

	if err := <-done; err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
}

// TestRunFailingEngineStopsTheOthers is the point of the package: Run returns
// only once the engine that was still serving has been cancelled too.
func TestRunFailingEngineStopsTheOthers(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	err := run(t, context.Background(), map[string]app.Engine{
		"http": serving(started),
		"grpc": failing(errBoom),
	})

	if !errors.Is(err, errBoom) {
		t.Fatalf("Run = %v, want an error wrapping errBoom", err)
	}
}

// TestRunCleanExitStopsTheOthers pins the half errgroup would miss: an engine
// returning nil on its own also brings the process down.
func TestRunCleanExitStopsTheOthers(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	err := run(t, context.Background(), map[string]app.Engine{
		"http":   serving(started),
		"oneoff": app.EngineFunc(func(context.Context) error { return nil }),
	})

	if err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
}

func TestRunJoinsEveryError(t *testing.T) {
	t.Parallel()

	err := run(t, context.Background(), map[string]app.Engine{
		"grpc": failing(errBoom),
		"http": failing(errBust),
	})

	if !errors.Is(err, errBoom) || !errors.Is(err, errBust) {
		t.Fatalf("Run = %v, want an error wrapping both errBoom and errBust", err)
	}
}

func TestStartStopStopsOnCancel(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	engine := app.StartStop(
		func() error { <-release; return nil },
		func() { close(release) },
	)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := engine.Run(ctx); err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
}

// TestStartStopStartFailingAlone pins that a start returning on its own is
// reported without stop being called: stopping something already stopped is
// how a GracefulStop panics.
func TestStartStopStartFailingAlone(t *testing.T) {
	t.Parallel()

	var stopped atomic.Bool
	engine := app.StartStop(
		func() error { return errBoom },
		func() { stopped.Store(true) },
	)

	if err := engine.Run(context.Background()); !errors.Is(err, errBoom) {
		t.Fatalf("Run = %v, want errBoom", err)
	}
	if stopped.Load() {
		t.Error("stop was called although start returned on its own")
	}
}
