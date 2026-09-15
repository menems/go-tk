# got-tk

Go toolkit. Bricks that every service here rewrote, extracted once.

Transport-agnostic on purpose: nothing imports chi or ConnectRPC. A package
hands back stdlib types, and each service writes its own five-line adapter.

```
go get github.com/menems/got-tk
make check
```

## app

The application container: several runners side by side under one context,
stopped together.

```go
func main() {
    ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
    defer stop()

    pool, err := pg.New(ctx, cfg.DatabaseURL)
    ...

    mux := http.NewServeMux()
    mux.Handle("GET /healthz/live", health.Live())
    mux.Handle("GET /healthz/ready", health.Ready(map[string]health.Checker{
        "postgres": health.CheckerFunc(pool.Ping),
    }))

    grpcSrv := grpc.NewServer()
    lis, err := net.Listen("tcp", ":9090")
    ...

    err = app.New(map[string]app.Runner{
        "http": httpd.New(":8080", mux),
        "grpc": app.RunnerFrom(func() error { return grpcSrv.Serve(lis) }, grpcSrv.GracefulStop),
    }).Run(ctx)
}
```

A runner is anything that occupies a goroutine until told to stop: an HTTP
server, a gRPC server, a consumer, a scheduler.

The ordinary way down is SIGTERM: `signal.NotifyContext` cancels the context,
every runner sees it at once, and `Run` returns when the last one has drained.
It reports every error joined, in name order.

The other way down is a runner returning by itself, which cancels the rest.
So every runner must be long-running: a one-shot task, a migration or a warm-up
runs before `Run`, never as a runner, because its clean return would take the
process with it.

`*App` is itself a `Runner`, so a subsystem with its own runners mounts as one
entry of the group above it.

`RunnerFrom(start, stop)` builds a `Runner` from a blocking start and a
separate stop, the shape of everything written before `context.Context`.
`stop` must make `start` return.

Signals stay in `main`. A container that grabs SIGTERM behind the caller's back
fights whoever else wants it: a test, a parent process manager, an embedding
binary.

## httpd

Runs an `http.Handler` with bounded timeouts and a graceful shutdown driven by
a context.

```go
srv := httpd.New(":8080", router, httpd.WithShutdownTimeout(30*time.Second))
```

The router is the caller's: a `chi.Router`, an `http.ServeMux`, or a mux
wrapped in `h2c` for ConnectRPC over cleartext HTTP/2.

`Run` blocks until `ctx` is cancelled, then drains in-flight requests within
the shutdown timeout (10s, `WithShutdownTimeout`). Read, write and idle
timeouts all default to a non-zero value: `http.Server` reads a zero as no
limit, which is how a slow client holds a connection open forever.

`WithListener` serves an already-bound listener, which is how a port-zero bind
reports the address it got.

## authctx

Reads the bearer credential off a request, and carries what authenticating it
produced through the context.

```go
var userID = authctx.NewKey[uuid.UUID]("user_id")

func authenticate(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        token, ok := authctx.Bearer(r.Header)
        if !ok {
            http.Error(w, "unauthorized", http.StatusUnauthorized)
            return
        }
        id, err := verify(token) // yours: the algorithm and the keys are yours
        ...
        next.ServeHTTP(w, r.WithContext(userID.With(r.Context(), id)))
    })
}
```

It verifies nothing. A token's signature, claims and expiry are the service's
business, because that is where the algorithm and the key material differ.

`Bearer` takes an `http.Header`, which a net/http middleware and a
`connect.Request` both hold. The scheme is matched case-insensitively, as
RFC 7235 requires and as the four hand-rolled versions this replaces did not.

A key is a value, not a type: `NewKey` returns a distinct key per call, so a
user id and a tenant id that are both `uuid.UUID` do not overwrite each other.
Declare it at package level, next to the middleware that fills it.

## health

Liveness and readiness probes as `http.HandlerFunc`. Stdlib only.

```go
mux.Handle("GET /healthz/live", health.Live())
mux.Handle("GET /healthz/ready", health.Ready(map[string]health.Checker{
    "postgres": health.CheckerFunc(pool.Ping),
}))
```

`Live` takes no dependency: a liveness probe that fails on a broken database
asks the orchestrator to restart a process that restarting will not fix.

`Ready` runs every check concurrently under one deadline (2s, `WithTimeout`).
It answers `200 {"status":"ok"}`, or `503 {"status":"unavailable","failed":
["postgres"]}`. The failing check's error text goes to the logger
(`WithLogger`), never to the response body: a probe endpoint is usually
reachable by more callers than the operator expects.

## pg

Opens a `*pgxpool.Pool` from a DSN. No wrapper type, no ping: pgxpool connects
lazily, and reachability is `health`'s question.

```go
pool, err := pg.New(ctx, dsn, pg.WithMaxConns(25))
if err != nil {
    return fmt.Errorf("db: %w", err)
}
defer pool.Close()
```

`New` owns the pool size. A `pool_max_conns` in the DSN is overwritten by
`DefaultMaxConns` (10) or by `WithMaxConns`.
