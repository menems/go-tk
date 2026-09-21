// Package httpd runs an http.Handler with bounded timeouts and a graceful
// shutdown driven by a context.
//
// It takes any http.Handler, so the router is the caller's choice: a
// chi.Router, an http.ServeMux, or a mux wrapped in h2c for ConnectRPC over
// cleartext HTTP/2. Nothing here imports a router or a transport.
//
// That router can also be this package's: NewRouter serves the routes a
// service names, one method and one pattern at a time, and answers every other
// request 404 in the failure envelope below.
//
// RequireBearer covers the handlers of the routes that need a credential: the
// bearer token reaches the resolver the service hands in, the principal it
// answers rides in the request context, and a request it refuses is answered
// 401 in that same envelope without the handler running.
//
// It also holds the JSON envelope those handlers answer in: WriteJSON puts the
// payload under "data", WriteError puts a code and a message under "error", and
// no body carries both. DecodeJSON reads a request body bounded by the byte
// limit its caller names, and refuses through that same error envelope.
package httpd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"
)

// Defaults applied when the matching option is not given. ReadTimeout and
// WriteTimeout bound a slow client; IdleTimeout bounds an idle keep-alive
// connection. A zero value on http.Server means no limit, which is why none
// of these is left unset.
const (
	DefaultReadTimeout       = 5 * time.Second
	DefaultReadHeaderTimeout = 2 * time.Second
	DefaultWriteTimeout      = 10 * time.Second
	DefaultIdleTimeout       = 120 * time.Second
	DefaultShutdownTimeout   = 10 * time.Second
)

// Server serves one handler until its context is cancelled.
type Server struct {
	addr              string
	handler           http.Handler
	listener          net.Listener
	readTimeout       time.Duration
	readHeaderTimeout time.Duration
	writeTimeout      time.Duration
	idleTimeout       time.Duration
	shutdownTimeout   time.Duration
	log               *slog.Logger
}

// Option configures a Server.
type Option func(*Server)

// WithReadTimeout bounds reading the whole request, body included.
func WithReadTimeout(d time.Duration) Option {
	return func(s *Server) { s.readTimeout = d }
}

// WithReadHeaderTimeout bounds reading the request headers.
func WithReadHeaderTimeout(d time.Duration) Option {
	return func(s *Server) { s.readHeaderTimeout = d }
}

// WithWriteTimeout bounds writing the response. A handler that streams or
// long-polls needs this raised, or it will be cut off mid-response.
func WithWriteTimeout(d time.Duration) Option {
	return func(s *Server) { s.writeTimeout = d }
}

// WithIdleTimeout bounds how long a keep-alive connection waits for the next
// request.
func WithIdleTimeout(d time.Duration) Option {
	return func(s *Server) { s.idleTimeout = d }
}

// WithShutdownTimeout bounds how long Run waits for in-flight requests after
// the context is cancelled. Past it, Run returns and the connections drop.
func WithShutdownTimeout(d time.Duration) Option {
	return func(s *Server) { s.shutdownTimeout = d }
}

// WithLogger sets the logger for the two lifecycle events Run emits.
// Defaults to slog.Default().
func WithLogger(log *slog.Logger) Option {
	return func(s *Server) { s.log = log }
}

// WithListener serves an already-bound listener and ignores the addr passed to
// New. It is how a caller learns the address of a port-zero bind, and how a
// TLS or socket-activated listener gets in. Run owns the listener and closes
// it.
func WithListener(ln net.Listener) Option {
	return func(s *Server) { s.listener = ln }
}

// New creates a Server that serves h on addr.
func New(addr string, h http.Handler, opts ...Option) *Server {
	s := &Server{
		addr:              addr,
		handler:           h,
		readTimeout:       DefaultReadTimeout,
		readHeaderTimeout: DefaultReadHeaderTimeout,
		writeTimeout:      DefaultWriteTimeout,
		idleTimeout:       DefaultIdleTimeout,
		shutdownTimeout:   DefaultShutdownTimeout,
		log:               slog.Default(),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Run serves until ctx is cancelled or the listener fails, then drains
// in-flight requests within the shutdown timeout. It returns nil on a clean
// stop. Run blocks and is called once per Server.
func (s *Server) Run(ctx context.Context) error {
	ln := s.listener
	if ln == nil {
		var err error
		if ln, err = net.Listen("tcp", s.addr); err != nil {
			return fmt.Errorf("httpd: listen %s: %w", s.addr, err)
		}
	}

	srv := &http.Server{
		Handler:           s.handler,
		ReadTimeout:       s.readTimeout,
		ReadHeaderTimeout: s.readHeaderTimeout,
		WriteTimeout:      s.writeTimeout,
		IdleTimeout:       s.idleTimeout,
	}

	// Cancelled on every return, so the watcher below exits even when Serve
	// fails on its own rather than on a shutdown.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		wg          sync.WaitGroup
		shutdownErr error
	)
	wg.Go(func() {
		<-ctx.Done()
		// Not derived from ctx: the drain outlives the cancellation that
		// triggered it.
		drainCtx, stop := context.WithTimeout(context.Background(), s.shutdownTimeout)
		defer stop()
		shutdownErr = srv.Shutdown(drainCtx)
	})

	s.log.Info("http server listening", "addr", ln.Addr().String())
	serveErr := srv.Serve(ln)

	cancel()
	wg.Wait()

	if !errors.Is(serveErr, http.ErrServerClosed) {
		return fmt.Errorf("httpd: serve: %w", serveErr)
	}
	if shutdownErr != nil {
		return fmt.Errorf("httpd: shutdown: %w", shutdownErr)
	}
	s.log.Info("http server stopped")
	return nil
}
