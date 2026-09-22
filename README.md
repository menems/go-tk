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
wrapped in `h2c` for ConnectRPC over cleartext HTTP/2. It can also be this
package's, below.

`Run` blocks until `ctx` is cancelled, then drains in-flight requests within
the shutdown timeout (10s, `WithShutdownTimeout`). Read, write and idle
timeouts all default to a non-zero value: `http.Server` reads a zero as no
limit, which is how a slow client holds a connection open forever.

`WithListener` serves an already-bound listener, which is how a port-zero bind
reports the address it got.

`NewRouter` is the route table: a service names each route it opens by its
method, its pattern and its handler, and gets back the `http.Handler` to serve.

```go
h, err := httpd.NewRouter(
    httpd.Route{Method: http.MethodGet, Pattern: "/things", Handler: list},
    httpd.Route{Method: http.MethodPost, Pattern: "/things", Handler: create},
    httpd.Route{Method: http.MethodGet, Pattern: "/things/{id}", Handler: show},
)
```

A request whose method and pattern one of them names reaches that handler, with
the pattern's values on the request (`r.PathValue("id")`). Every other request
is refused in the failure envelope below, whatever it carried: nothing of it is
read, and nothing of it comes back in the answer.

A path the table names, asked with a method no route names for it, is refused
`405 method_not_allowed` with an `Allow` header naming exactly the methods
named for that path, and nothing else of the table; the path's own handlers do
not run. Everything else is `404 no_such_route`, naming no method. A path
differing from a named one by a trailing slash, a repeated slash or a `..`
segment is one nobody named, so it gets that 404 too, where an `http.ServeMux`
would redirect towards the route beside it.

Patterns are `ServeMux`'s own, minus the method and the host, so there is one
syntax and each route is named once. A route with no method (it would answer
every method), a pattern `ServeMux` would read as a host, or a nil handler is
refused at wiring; a malformed pattern, or one named twice, panics there, as
`ServeMux` makes it.

`RequireBearer` covers the routes that need a credential. A service hands its
own context key and its own resolver from token to principal, then wraps the
handlers it covers, so what is covered is named where the route is.

```go
var userID = authctx.NewKey[uuid.UUID]("user_id")

auth := httpd.RequireBearer(userID, verify) // verify is yours, see authctx
h, err := httpd.NewRouter(
    httpd.Route{Method: http.MethodGet, Pattern: "/things", Handler: auth(list)},
    httpd.Route{Method: http.MethodGet, Pattern: "/status", Handler: status},
)
```

The handler reads the principal back with `userID.From(r.Context())`. A covered
request carrying no bearer credential, or one the resolver refuses, is answered
`401 unauthenticated` with a `WWW-Authenticate: Bearer` header, and the handler
does not run; nothing of the token, and nothing the resolver said about it,
comes back in that answer.

A resolver refuses by returning an error wrapping `httpd.ErrUnauthenticated`,
which is the verdict "this token resolves to no principal" and the only error
answered 401. Every other error is the resolver failing rather than judging:
`500 auth_unavailable`, no challenge header, handler still not run. So a client
tells a credential it should fix from an outage it should retry by the status
and the code alone, and each is a constant this package writes.

An error saying neither thing lands on the 500 too. Both answers deny the
request; what the default picks is which one the operator sees, and reporting a
store that is down as a client that was turned away hides the outage behind the
one status nobody investigates.

Coverage is exactly the set of handlers wrapped: `/status` above serves with no
principal in its context, and a handler finding none has to treat that as
unauthenticated. The wrap sitting on the handler is also what keeps the table's
404 and 405 first, so no resolver is asked about a request no route matched and
enumerating paths stays anonymous.

`RequireRight` closes a route on a right its caller must hold. The service
names one right per wrap and hands the check that answers whether a principal
holds it; the wrap sits on the handler, inside the bearer wrap.

```go
listing := httpd.RequireRight(userID, scopeList, allowed) // allowed is yours
h, err := httpd.NewRouter(
    httpd.Route{Method: http.MethodGet, Pattern: "/things", Handler: auth(listing(list))},
    httpd.Route{Method: http.MethodGet, Pattern: "/status", Handler: auth(status)},
)
```

The right is the service's own type, and this package never reads it: it holds
no right table and grants nothing, so a check that answers wrongly authorizes
wrongly. A caller the check judged and refused is answered `403 forbidden` and
the handler does not run.

The check answers a verdict, or an error saying it could not reach one. That
error is the check failing rather than judging: `500 auth_unavailable`, the
same code the resolver's own failure writes, handler still not run. A request
reaching the wrap with no principal lands on that same 500, nothing having
judged it either; that is a route wired without `RequireBearer` above it, and a
`403` there would tell a caller it lacks a right when what it met was a mistake
in the table, and leave an operator unable to tell that route from one closed
on purpose. Both answers deny the request, and a client tells the one no
credential of its own fixes from the one it should retry by the status and the
code alone.

Neither refusal carries a `WWW-Authenticate` header: a challenge invites a
retry, and no credential this caller can present makes the request go through.
Each is one fixed body under one code, the same whatever right was demanded and
whatever the caller does hold, naming neither the principal, nor the right, nor
anything the check said. So probing a table one route at a time tells a caller
it may not make the request, never which right would have let it.

The order a request meets is the table, then the bearer, then the right. So
nothing is asked about rights for a request no route matched or no credential
authenticated, `404`, `405` and `401` stay exactly what the two above left
them, and `/status` above is covered while demanding no right.

`GrantOrigins` lets a browser at an origin the service listed read the answers
this handler gives. The service names one policy at wiring, the origins it
grants and the request headers it allows, and that one value has two halves:
`Wrap`, in front of the table, and `Router`, the table itself.

```go
grant, err := httpd.GrantOrigins(
    []string{"https://app.example.test"},
    []string{"Content-Type"},
)
h, err := grant.Router(
    httpd.Route{Method: http.MethodGet, Pattern: "/things", Handler: list},
    httpd.Route{Method: http.MethodPost, Pattern: "/things", Handler: create},
)
srv := httpd.New(":8080", grant.Wrap(h))
```

`Wrap` answers nothing of its own: it adds headers to whatever the table
answered, so a listed origin reads the table's `404` and `405`, and a covered
route's `401` and `403`, as it reads a handler's own answer. Listing an origin
is trusting it with what those refusals say.

`Router` is `NewRouter`'s table, answering besides it the preflight a browser
sends before a request it may not send blind. A preflight is an answer and not
a header, which is why the table gives it: one asked on a path no route names
meets that table's own `404`, and one on a covered route is answered before any
wrap on a handler runs, so no resolver is asked and no `401` is given to a
request that carries no credential by construction. A table built with
`NewRouter` and wrapped with `Wrap` answers every preflight `405`, which is
every browser broken on anything but a simple request.

An `OPTIONS` naming the method it means to send, on a path the table names, is
answered `204` with no body, `Access-Control-Allow-Methods` naming exactly the
methods that path names and `Access-Control-Allow-Headers` naming exactly the
request headers the service listed. The method it asked about is not read: it
is told the list and decides for itself, which is the same set the `405` above
already names to anyone. An `OPTIONS` carrying no such question is no
preflight, so it is refused `405` like any method nobody named, with `OPTIONS`
in the `Allow` header beside them, that path now answering it.

An origin is granted by being equal to an entry of the list, whole. Nothing
splits it, lowercases it or matches a suffix of it, so a listed origin under
another scheme or another port, or one carrying a trailing slash, is an origin
nobody listed. The answer then names that one origin and nothing else of the
list, and never a wildcard.

A request whose origin is on the list nowhere, and one carrying no `Origin` at
all, get the answer they would have got without that header: the same status,
the same body, no grant. Its preflight gets the same `204`, naming no method,
no header and no origin. `Origin` is a browser's own statement about itself,
which anything that is not a browser forges in one header, so refusing on it
gates nobody, while a same-origin `POST` sends one too and would meet that
refusal from the service's own page. The cost is that a caller whose origin was
not listed is told so by an absent header alone.

Every answer carries `Vary: Origin`, granted or not: the headers depend on
which origin asked, and without it a shared cache hands one origin's grant to
the next caller. No answer carries `Access-Control-Allow-Credentials` either, so
a cookie or a credential the browser holds is never granted to a page on the
strength of this list.

An entry that is not exactly a scheme and a host is refused at wiring, naming
that entry, and nothing is served: a wildcard, an empty entry, a path, a query,
a trailing slash or a missing scheme all name something no `Origin` header
equals, so listing one grants nobody while reading as if it did. A request
header entry that is not one header name is refused there too: the list is
written back as one comma-separated value, so an entry carrying a comma, a
blank or a line break would name headers the service never listed. The two
rules also catch the two lists handed over in the wrong order.

The same package holds the JSON envelope the handlers answer in. Every body
carries one member: the payload under `data`, or a machine-readable code and a
human message under `error`. They are two types, so no body can hold both.

```go
httpd.WriteJSON(w, http.StatusOK, record)
// {"data":{...}}

httpd.WriteError(w, http.StatusConflict, "already_exists", "a record with that key exists")
// {"error":{"code":"already_exists","message":"a record with that key exists"}}
```

A code is an `httpd.ErrorCode`, not a bare string, so it and the message beside
it cannot be passed in the wrong order. The codes are the service's own;
declare them next to the handlers that write them.

```go
var body createRequest
if err := httpd.DecodeJSON(w, r, 64*1024, &body); err != nil {
    log.Error("decode request", "error", err) // the answer is already written
    return
}
```

`DecodeJSON` reads one JSON value into a declared type, past no more bytes than
the caller names, and refuses through that same error envelope: `413
payload_too_large` over the limit, `400 invalid_json` for a malformed body, an
empty one, or a second value after the first. The message is a constant per
code, so nothing the client sent comes back to it; the error returned carries
the cause for the one log line the handler writes.

## authctx

Reads the bearer credential off a request, and carries what authenticating it
produced through the context.

```go
var userID = authctx.NewKey[uuid.UUID]("user_id")

token, ok := authctx.Bearer(r.Header) // or of a connect.Request's header
ctx = userID.With(ctx, id)            // id is what verifying token produced
id, ok := userID.From(r.Context())    // what the handler reads back
```

It verifies nothing. A token's signature, claims and expiry are the service's
business, because that is where the algorithm and the key material differ.

`Bearer` takes an `http.Header`, which a net/http middleware and a
`connect.Request` both hold. The scheme is matched case-insensitively, as
RFC 7235 requires and as the four hand-rolled versions this replaces did not.

A key is a value, not a type: `NewKey` returns a distinct key per call, so a
user id and a tenant id that are both `uuid.UUID` do not overwrite each other.
Declare it at package level, next to the middleware that fills it.

Over net/http that middleware is already written: `httpd.RequireBearer` wires
the two ends together and picks the status code, which this package does not,
so that a `connect.Request` caller reaches the same two ends.

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
