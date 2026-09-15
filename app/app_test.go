package app_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"

	"github.com/menems/go-tk/app"
)

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

// An App is itself a Runner, so a group nests in a group.
var _ app.Runner = (*app.App)(nil)

var (
	errBoom = errors.New("boom")
	errBust = errors.New("bust")
)

// serving blocks until the app stops it, like a real server would.
func serving(started chan<- struct{}) app.RunnerFunc {
	return func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		return nil
	}
}

func failing(err error) app.RunnerFunc {
	return func(context.Context) error { return err }
}

func run(t *testing.T, ctx context.Context, runners map[string]app.Runner) error {
	t.Helper()
	return app.New(runners, app.WithLogger(discard)).Run(ctx)
}

func TestRunNoRunners(t *testing.T) {
	t.Parallel()

	if err := run(t, context.Background(), nil); err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
}

func TestRunStopsEveryRunnerOnCancel(t *testing.T) {
	t.Parallel()

	httpStarted, grpcStarted := make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- run(t, ctx, map[string]app.Runner{
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

// TestRunFailingRunnerStopsTheOthers is the point of the package: Run returns
// only once the runner that was still serving has been cancelled too.
func TestRunFailingRunnerStopsTheOthers(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	err := run(t, context.Background(), map[string]app.Runner{
		"http": serving(started),
		"grpc": failing(errBoom),
	})

	if !errors.Is(err, errBoom) {
		t.Fatalf("Run = %v, want an error wrapping errBoom", err)
	}
}

// TestRunCleanExitStopsTheOthers pins the half errgroup would miss: a runner
// that returns nil without being cancelled still brings the process down,
// rather than leaving a pod that passes its liveness probe and serves nothing.
// It is also why a one-shot task cannot be a runner.
func TestRunCleanExitStopsTheOthers(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	err := run(t, context.Background(), map[string]app.Runner{
		"http": serving(started),
		"grpc": app.RunnerFunc(func(context.Context) error { return nil }),
	})

	if err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
}

func TestRunJoinsEveryError(t *testing.T) {
	t.Parallel()

	err := run(t, context.Background(), map[string]app.Runner{
		"grpc": failing(errBoom),
		"http": failing(errBust),
	})

	if !errors.Is(err, errBoom) || !errors.Is(err, errBust) {
		t.Fatalf("Run = %v, want an error wrapping both errBoom and errBust", err)
	}
}

func TestRunnerFromStopsOnCancel(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	runner := app.RunnerFrom(
		func() error { <-release; return nil },
		func() { close(release) },
	)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := runner.Run(ctx); err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
}

// TestRunnerFromStartFailingAlone pins that a start returning on its own is
// reported without stop being called: stopping something already stopped is
// how a GracefulStop panics.
func TestRunnerFromStartFailingAlone(t *testing.T) {
	t.Parallel()

	var stopped atomic.Bool
	runner := app.RunnerFrom(
		func() error { return errBoom },
		func() { stopped.Store(true) },
	)

	if err := runner.Run(context.Background()); !errors.Is(err, errBoom) {
		t.Fatalf("Run = %v, want errBoom", err)
	}
	if stopped.Load() {
		t.Error("stop was called although start returned on its own")
	}
}

// TestAppNestsInAnApp pins what the shared Run signature buys: a subsystem
// with its own runners mounts as one runner of the group above it, and the
// outer cancellation reaches all of them.
func TestAppNestsInAnApp(t *testing.T) {
	t.Parallel()

	innerStarted, outerStarted := make(chan struct{}), make(chan struct{})
	inner := app.New(
		map[string]app.Runner{"http": serving(innerStarted)},
		app.WithLogger(discard),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- run(t, ctx, map[string]app.Runner{
			"grpc":      serving(outerStarted),
			"subsystem": inner,
		})
	}()

	<-innerStarted
	<-outerStarted
	cancel()

	if err := <-done; err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
}
