package httpd_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/menems/go-tk/transport/http"
)

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

// start runs a server on a port the kernel picks and returns its base URL,
// the cancel that stops it, and a wait for Run's error.
func start(t *testing.T, h http.Handler, opts ...httpd.Option) (baseURL string, stop context.CancelFunc, wait func() error) {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	srv := httpd.New("", h, append([]httpd.Option{
		httpd.WithListener(ln),
		httpd.WithLogger(discard),
	}, opts...)...)

	go func() { errCh <- srv.Run(ctx) }()

	return "http://" + ln.Addr().String(), cancel, func() error { return <-errCh }
}

func TestRunServesTheHandler(t *testing.T) {
	t.Parallel()

	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	baseURL, stop, wait := start(t, h)

	resp, err := http.Get(baseURL + "/anything")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusTeapot {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusTeapot)
	}

	stop()
	if err := wait(); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

// TestRunDrainsInFlightRequest pins the point of the shutdown timeout: a
// request already being served survives the cancellation.
func TestRunDrainsInFlightRequest(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	release := make(chan struct{})
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		w.WriteHeader(http.StatusOK)
	})
	baseURL, stop, wait := start(t, h)

	type result struct {
		status int
		err    error
	}
	done := make(chan result, 1)
	go func() {
		resp, err := http.Get(baseURL + "/slow")
		if err != nil {
			done <- result{err: err}
			return
		}
		defer resp.Body.Close()
		done <- result{status: resp.StatusCode}
	}()

	<-started
	stop()
	close(release)

	got := <-done
	if got.err != nil {
		t.Fatalf("in-flight request failed: %v", got.err)
	}
	if got.status != http.StatusOK {
		t.Errorf("status = %d, want %d", got.status, http.StatusOK)
	}
	if err := wait(); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

// TestRunReportsShutdownTimeout pins the other half: a handler that outlasts
// the drain makes Run report it instead of returning a clean stop.
func TestRunReportsShutdownTimeout(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
	})
	baseURL, stop, wait := start(t, h, httpd.WithShutdownTimeout(10*time.Millisecond))

	go func() {
		resp, err := http.Get(baseURL + "/stuck")
		if err == nil {
			resp.Body.Close()
		}
	}()

	<-started
	stop()

	err := wait()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run error = %v, want one wrapping context.DeadlineExceeded", err)
	}
}

func TestRunReportsListenFailure(t *testing.T) {
	t.Parallel()

	// A port out of range: no kernel will bind it.
	srv := httpd.New("127.0.0.1:99999", http.NotFoundHandler(), httpd.WithLogger(discard))
	if err := srv.Run(context.Background()); err == nil {
		t.Fatal("expected a listen error, got nil")
	}
}
