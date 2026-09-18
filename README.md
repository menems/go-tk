# go-tk

Go toolkit. Bricks that every service here rewrote, extracted once.

Transport-agnostic on purpose: nothing imports chi or ConnectRPC. A package
hands back stdlib types, and each service writes its own five-line adapter.

## Layout

```
app/                     supervise several runners under one context
transport/http/          run an http.Handler with timeouts and a graceful stop
authctx/                 read the bearer, carry the principal
health/                  liveness and readiness probes
config/                  read configuration at boot
storage/postgres/        a pgxpool.Pool from a DSN
storage/postgres/migrate embedded SQL migrations, applied out of band
telemetry/otel/          OpenTelemetry providers
telemetry/prometheus/    let Prometheus scrape them
```

A directory groups siblings, present or planned: `storage/mysql` and
`transport/grpc` land next to their peers without moving anything.

Package names match their directory, with one exception: `transport/http` is
`package httpd`, because `package http` would shadow `net/http` in every
consumer that uses both, which is all of them. `telemetry/otel` and
`telemetry/prometheus` do shadow their upstream namesakes, so a `main` that
needs both aliases one.

## Dependencies

Five modules, one per dependency set, so importing one package cannot drag
another's dependencies into your module graph.

| module | packages | outside the stdlib |
|---|---|---|
| `github.com/menems/go-tk` | `app`, `transport/http`, `authctx`, `health`, `config` | none |
| `github.com/menems/go-tk/storage/postgres` | `storage/postgres` | pgx |
| `github.com/menems/go-tk/storage/postgres/migrate` | `storage/postgres/migrate` | golang-migrate, pgx |
| `github.com/menems/go-tk/telemetry/otel` | `telemetry/otel` | OpenTelemetry |
| `github.com/menems/go-tk/telemetry/prometheus` | `telemetry/prometheus` | OpenTelemetry SDK, Prometheus |

```
go get github.com/menems/go-tk                        # app, transport/http, authctx, health, config
go get github.com/menems/go-tk/storage/postgres       # adds pgx, and nothing else
go get github.com/menems/go-tk/storage/postgres/migrate  # adds golang-migrate
go get github.com/menems/go-tk/telemetry/otel         # adds OpenTelemetry
go get github.com/menems/go-tk/telemetry/prometheus   # adds Prometheus
```

Import paths are the module paths, so nothing in your code changes when a
package moves module.

Measured on a service importing `go-tk/transport/http` alone: `go list -m all`
reports 2 modules, itself and the toolkit, and no `go.sum` is written at all.
As a single module it reported 39, otel, Prometheus and pgx among them, which
is what a vulnerability scanner reads even though none of it was ever
compiled.

No module here requires another, so taking one never pulls a second. What the
split costs is releases: each module carries its own tag, `v0.1.0`,
`storage/postgres/v0.1.0`, and so on. `go.work` is committed so an edit across
modules resolves locally without publishing anything, and `make check` runs
each module in turn.

## app

The application container: several runners side by side under one context,
stopped together.

```go
func main() {
    ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
    defer stop()

    pool, err := postgres.New(ctx, cfg.DatabaseURL)
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
So every runner must be long-running: a one-shot task, a migration or a
warm-up runs before `Run`, never as a runner, because its clean return would
take the process with it.

`*App` is itself a `Runner`, so a subsystem with its own runners mounts as one
entry of the group above it.

`RunnerFrom(start, stop)` builds a `Runner` from a blocking start and a
separate stop, the shape of everything written before `context.Context`.
`stop` must make `start` return.

Signals stay in `main`. A container that grabs SIGTERM behind the caller's back
fights whoever else wants it: a test, a parent process manager, an embedding
binary.

## transport/http

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

## config

Reads configuration from an environment at boot, collecting every problem
before it reports.

```go
func Load(getenv func(string) string) (Config, error) {
    env := config.New(getenv)
    cfg := Config{
        DatabaseURL: env.Required("DATABASE_URL"),
        Addr:        env.String("HTTP_ADDR", ":8080"),
        MaxConns:    env.Int("DB_MAX_CONNS", 10),
        Timeout:     env.Duration("REQUEST_TIMEOUT", 5*time.Second),
    }
    return cfg, env.Err()
}
```

The `Config` struct stays in the service, with its fields, its names and its
defaults. What the package owns is the reading.

`Err` reports every problem joined, so an operator fixing a deployment sees
three missing values at once instead of one per deploy.

`getenv` is a parameter, not a call to `os.Getenv`: pass `os.Getenv` in main
and a map's lookup in a test, and the test needs no process environment and
runs in parallel.

Error messages name the key and never the value, because a malformed
`DATABASE_URL` carries a password and a boot error gets logged.

Only a string can be `Required`. The values with no sensible default are DSNs,
endpoints and secrets; a port or a timeout that reaches production unset wants
a default, not a boot failure.

## storage/postgres

Opens a `*pgxpool.Pool` from a DSN. No wrapper type, no ping: pgxpool connects
lazily, and reachability is `health`'s question.

```go
pool, err := postgres.New(ctx, dsn, postgres.WithMaxConns(25))
if err != nil {
    return fmt.Errorf("db: %w", err)
}
defer pool.Close()
```

`New` owns the pool size. A `pool_max_conns` in the DSN is overwritten by
`DefaultMaxConns` (10) or by `WithMaxConns`.

## storage/postgres/migrate

Applies SQL migrations out of band, from a command of the consuming module.
The service never migrates at boot: N replicas would race on the same
migration, a binary rollback cannot roll the schema back, and the server would
carry the migration library.

The consumer owns the two things this package cannot: the files, embedded and
handed over as an `fs.FS`, and the DSN, read in its own `main`.

```go
//go:embed *.sql
var migrationsFS embed.FS

func main() {
    logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

    dsn := os.Getenv("DATABASE_URL")
    if dsn == "" {
        logger.Error("missing required environment variable DATABASE_URL")
        os.Exit(1)
    }

    if err := migrate.Run(logger, dsn, migrationsFS, os.Args[1:]); err != nil {
        if errors.Is(err, migrate.ErrUsage) {
            fmt.Fprintln(os.Stderr, migrate.Usage)
            os.Exit(2)
        }
        logger.Error("migrate failed", "error", err)
        os.Exit(1)
    }
}
```

Register that command as a `tool` in the consumer's `go.mod`
(`go get -tool ./cmd/migrate`, run as `go tool migrate up`) and call it from
the task runner. Its own module is what keeps golang-migrate out of the server
binary.

`Run` parses `up`, `down <N>` and `version` before it opens anything, so a
typo costs no connection. It takes no `context.Context`: golang-migrate's API
predates one and a migration in flight is not cancellable, so an ignored
parameter would be worse than its absence.

## telemetry/otel

Wires the OpenTelemetry trace and metric providers at boot and flushes them at
shutdown.

```go
tel, err := otel.Setup(ctx, otel.Config{
    ServiceName:    "users",
    ServiceVersion: build.Version,
    OTLPEndpoint:   cfg.OTLPEndpoint, // "" reports nowhere
})
if err != nil {
    return fmt.Errorf("telemetry: %w", err)
}
defer func() {
    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()
    _ = tel.Shutdown(ctx)
}()
```

It ships **no instrumentation**. `Setup` installs the providers as
OpenTelemetry's globals, which is how a maintained library finds them:

```go
srv := httpd.New(":8080", otelhttp.NewHandler(mux, "server"))
```

Writing that middleware by hand is how the versions this replaces each grew a
hundred lines reimplementing `otelhttp`.

`Setup` also sets the W3C trace context propagator, which all of them forgot.
Without it a trace stops at the first service boundary, and a test pins it.

`OTLPEndpoint` empty installs providers with no exporter, so the instrumented
code runs unchanged on a laptop and in a test.

Shutdown is deliberately **not** an `app.Runner`. Telemetry has to outlive the
servers it observes or their last spans never leave the process, and an
`app.App` stops every runner at once. It belongs in a `defer` in main, which
runs after `app.Run` has returned.

## telemetry/prometheus

Lets a Prometheus server scrape those same OpenTelemetry metrics, for a
service pulled rather than pushed.

```go
reg := promclient.NewRegistry()
reader, err := prometheus.Reader(reg)
...
tel, err := otel.Setup(ctx, otel.Config{
    ServiceName:   "users",
    MetricReaders: []sdkmetric.Reader{reader},
})
...
mux.Handle("GET /metrics", prometheus.Handler(reg))
```

The instruments stay OpenTelemetry's, so a service switches between scraping
and pushing by changing this wiring in main and nothing else.

It is a separate module from `telemetry/otel` and requires neither it nor the
reverse: a pushed service never sees the Prometheus dependency, and a scraped
one plugs in through the SDK's own `Reader` interface.
