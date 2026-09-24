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
sortid/                  mint ids that sort in creation order
crypto/password/         hold a password, hash it and verify it behind a seam
crypto/token/            issue an opaque bearer token, store a value that replays nothing
storage/keyset/          serve a list page by page, newest first, from a cursor
storage/postgres/        a bounded pgxpool.Pool, Classify and its sentinels
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
| `github.com/menems/go-tk` | `app`, `transport/http`, `authctx`, `health`, `config`, `sortid`, `crypto/password`, `crypto/token`, `storage/keyset` | none |
| `github.com/menems/go-tk/storage/postgres` | `storage/postgres` | pgx |
| `github.com/menems/go-tk/storage/postgres/migrate` | `storage/postgres/migrate` | golang-migrate, pgx |
| `github.com/menems/go-tk/telemetry/otel` | `telemetry/otel` | OpenTelemetry |
| `github.com/menems/go-tk/telemetry/prometheus` | `telemetry/prometheus` | OpenTelemetry SDK, Prometheus |

```
go get github.com/menems/go-tk                        # app, transport/http, authctx, health, config, sortid, crypto/password, crypto/token, storage/keyset
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

`LimitRate` meters a route on the cadence of the caller it belongs to. The
service hands in two functions: the one naming that caller, and the counter
taking one call from its budget.

```go
meter := httpd.LimitRate(callerOf, count) // both are yours
h, err := httpd.NewRouter(
    httpd.Route{Method: http.MethodGet, Pattern: "/things", Handler: auth(meter(list))},
    httpd.Route{Method: http.MethodGet, Pattern: "/status", Handler: auth(status)},
)
```

The key is the service's own because every honest one is a fact this package
holds nothing of: an address means deciding which forwarded header to trust,
and a principal means a key only the service names. So a key a caller can forge
is a key with which it spends another caller's budget, and a service keying on
a forwarded address trusts exactly its own proxy or meters that proxy as one
caller. A request the keying function names no caller for is served with no
call taken, which is the price of that: a route reachable with no credential
and keyed on its principal is metered for nobody.

A caller with no call left is answered `429 too_many_requests` with a
`Retry-After` header, and the handler does not run. The header carries a whole
number of seconds, rounded up from the delay the counter gave and never below
one: coming back on the second it names finds the call it promised, and a zero
would invite the caller back at once.

The counter answers a verdict, or an error saying it could not reach one. That
error is the counter failing rather than answering: `500 rate_unavailable`, no
`Retry-After`, handler still not run. A code of its own, not the
`auth_unavailable` above: what failed is a budget and not a credential. Both
refusals are one fixed body under one code, the same whatever caller was
metered, whatever its ceiling and whatever its delay, and the one number either
carries is that caller's own delay, in a header. So a ceiling is measured by
probing and is not a secret, and a client tells the refusal it should wait out
from the one it should report by the status and the code alone.

The counter is a seam rather than a map fixed here, so a shared store, its
module and its dependency stay the service's decision; a counter of a single
process gives each replica a budget of its own. The wrap sits on the handler,
so the order a request meets is the table, then the bearer, then the right,
then the rate: no budget is spent on the `404` of a path nobody named, and the
preflight `Grant.Router` answers below is answered before any wrap on a handler
runs. A service whose key is readable from the request alone can place it
outside the bearer wrap instead, the one position where a flood of unresolvable
tokens is metered before the resolver is asked.

`CountWithin` is the counter this package ships, the one of a single process: a
ceiling of calls per caller key inside a window, with the clock a parameter, so
a service passes `time.Now` and a test passes its own.

```go
count, err := httpd.CountWithin[callerKey](100, time.Minute, time.Now)
meter := httpd.LimitRate(callerOf, count) // callerOf is yours
```

A key's window opens on that key's own first call, not on a boundary the
package picks: the caller is served the ceiling of the window, refused what is
left of it, and told how much that is. Its budgets live in the memory of the
process serving the requests, so three replicas meter three cadences of that
ceiling and not one; a budget shared across them is a store, its module and its
dependency, and which one stays the service's decision behind the `Counter`
seam. What it holds grows with the distinct keys seen inside two windows at
most, an entry being forgotten by a sweep that runs at most once per window
rather than at the instant that key's own window passed, so a caller rotating
its key faster than the window drives that growth and a service needing a
harder bound hands in a counter of its own. A ceiling below one call, and a
window of zero or less, are refused at wiring under an error naming the value:
the first meters a route into a closed one, the second names no moment to send
a refused caller back to.

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

`IntBetween` reads an integer the service bounds, both ends included:
`env.IntBetween("WORK_SLOTS", 1, 64, 4)`. A value outside the bounds is a
problem naming the key and the bounds, and the fallback is returned. `Int`
keeps its signature: bounds are a second method, not a change every consumer
already calling `Int` would have to absorb. Bounds with the lower end above the
upper, or a fallback outside them, are the code's defect, not the operator's:
the call records it whatever the environment holds and returns the lower bound
without reading the key.

`List` reads a comma-separated list, each element trimmed of the spaces around
it: `env.List("ALLOWED_ORIGINS", nil)`. An empty or blank element (`a,,b`, a
trailing comma, a value made only of spaces) is a problem naming the key and
no element, since one of them may be an API key, and the fallback is returned.
Dropping the element quietly would leave the operator believing it configured.

Only a string can be `Required`. The values with no sensible default are DSNs,
endpoints and secrets; a port or a timeout that reaches production unset wants
a default, not a boot failure.

## sortid

Mints ids that sort in creation order, and parses back only the text they are
written in. Stdlib only.

```go
mint, err := sortid.NewMinter(time.Now) // at wiring; a nil clock is refused here
id := mint.Mint()                       // a version 7 UUID, greater than every id before it
text := id.String()                     // 0192258c-4a7e-7b3c-9d1e-2f3a4b5c6d7e, the stdlib's text
id, err = sortid.Parse(r.PathValue("id")) // ErrMalformed on anything String would not write
id.Compare(other)                       // mint order; the texts compare the same way as strings
json.Marshal(order{ID: id})             // {"id":"0192258c-..."}; a map keyed by ID writes the same text
json.Unmarshal(body, &req)              // an ID field decodes like Parse, ErrMalformed otherwise

row.ID = uuid.UUID(id)                  // to the row's 16 bytes, no call
id = sortid.ID(row.ID)                  // and back, trusting the service's own row
```

An `ID` is a version 7 UUID of RFC 9562: 48 bits of Unix milliseconds, the
version, 12 bits of the millisecond's fraction as the stdlib writes them, the
variant, and 62 bits from `crypto/rand`. Its bytes and its text sort in mint
order, so a row keyed on it lands at the end of its index.

A `Minter` takes the service's clock at wiring, as `httpd.CountWithin` does,
and keeps the last timestamp it used. When the clock stands still or steps
back, it moves one 4,096th of a millisecond past that one instead, so ids keep
increasing and run ahead of the clock by that much per id until it catches up.
It is not the stdlib's `uuid.NewV7` behind a function: that reads its own
clock, so no test can pin what a stalled or backward clock gives, and on a
backward step it starts over and the order breaks. There is no package-level
default minter either, which would be global state with its clock set before
first use by convention alone.

`Parse` checks the length first, fixed at 36, then accepts only the lowercase
8-4-4-4-12 text `String` writes, of version 7 and RFC 9562 variant. The upper
case, the braces, the `urn:uuid:` prefix and the undashed form the stdlib reads
are refused, as are another version, another variant, the nil and the max UUID,
and the zero `ID`'s own text. Every refusal is the one fixed `ErrMalformed`,
carrying no part of what arrived.

An `ID` field or map key encodes as the text `String` writes, and decodes from a
JSON body or through `encoding.TextUnmarshaler` exactly as `Parse` reads it,
not as the stdlib's `UUID.UnmarshalText`, which reads four forms in any case. A
JSON null, a number, or the array of 16 numbers a bare `[16]byte` would take is
refused under the same `ErrMalformed`, and a refusal leaves the field as it
was. What `MarshalText` writes is always a text `Parse` accepts back, so the
zero `ID`, or bytes of another version converted from a row, fail to marshal
under `ErrMalformed` rather than write the nil UUID; `IsZero` lets a field
tagged `omitzero` drop the zero `ID` instead.

An id identifies a row and grants nothing: its random bits make it hard to
guess, but it is no capability, and the service authorizes access to the row
itself. It publishes its creation time to the millisecond to anyone who reads
it; a service that must not say when an account or an order was created does
not expose the id.

## crypto/password

Holds a plaintext password in a carrier nothing reads it out of, and hashes and
verifies it through the hasher a service names at wiring. Stdlib only.

```go
type login struct {
    Email    string            `json:"email"`
    Password password.Password `json:"password"`
}

pw, err := password.New(candidate)           // ErrEmpty on ""
fmt.Sprintf("%v %s %q %+v %#v", pw, /*…*/)   // [REDACTED], every verb
json.Marshal(pw)                             // "[REDACTED]"
slog.Info("login", slog.Any("password", pw)) // password=[REDACTED]
pw.Equal(confirmation)                       // constant time, neither plaintext leaves

hasher, err := password.NewPBKDF2()          // or WithCost(n), or a Hasher of yours
stored, err := pw.Hash(hasher)               // "$pbkdf2-sha256$i=600000$<salt>$<key>"
err = pw.Verify(hasher, stored)              // nil, ErrMismatch or ErrUnreadable

bounded, err := password.Bound(hasher, 8)    // at most 8 calls at once
err = pw.Verify(bounded, stored)             // … or ErrBusy, at once, when all 8 are held
```

A login body decodes into the carrier directly, so the plaintext is inside it
from the moment it enters the process, instead of passing through a plain
string field in a struct someone prints. That asymmetry is the whole point:
unmarshalling reads the secret, marshalling writes the redaction.

Containment is a property of the type, not a convention to remember. The
plaintext sits behind one indirection, so a struct holding a carrier in an
unexported field cannot have it reflected out either: `fmt` cannot call a
method on a value it reaches through an unexported field, so it reads that
value's fields, and a pointer prints as an address where a string would print
the secret. Every other surface is closed by the hook Go offers for it,
`String` for the verbs `fmt` routes through it, `GoString` for `%#v`,
`MarshalJSON` and `MarshalText` for an encoder, `LogValue` for an `slog`
handler, and each answers the same fixed redaction.

Marshalling redacts instead of failing. A service that puts a password in an
outgoing payload has a bug, and a toolkit answering it with an error decides an
outage on that service's behalf; what it costs is that the bug ships a redacted
member rather than stopping at the boundary. The round trip through text is
lossy for the same reason.

A carrier holding nothing matches nothing. `New("")` and either decoding of an
empty value answer `ErrEmpty`, a JSON `null` and a JSON value that is not a
string are refused too, and the zero value redacts like any other, so nothing
derived from a carrier nobody filled can verify. Hashing one is refused under
the same error rather than done.

`Hasher` is the seam: one interface naming both the hashing and the
verification, and the service names at wiring which implementation it runs on.
This package picks none on its own. It takes the plaintext as bytes and not as
a carrier, because a carrier reads out to nobody and an implementation living
in a service's own module could not open one. That call is also the one place
the plaintext leaves the carrier, into the hasher the service chose and nowhere
else.

`NewPBKDF2` is the implementation in the box: PBKDF2-HMAC-SHA256 at
`DefaultCost` iterations unless `WithCost` names another, and a cost below one
refused at wiring under an error naming the value. It derives by iteration
alone, the memory-cheap one of the three schemes anyone recommends, which is
the price of this module holding nothing outside the standard library. A
service whose stolen table would be worth cracking on GPUs writes a bcrypt or
an argon2id `Hasher` at its own wiring, in its own module, where that
dependency belongs; nothing else about the service changes.

A stored value carries the parameters it was made under, so a service raising
its cost still verifies everything it stored before. Each hash salts afresh, so
one password hashed twice gives two stored values that both verify it. A value
another implementation wrote, one truncated, one whose parameters it cannot
read and an empty one are each refused and match nothing. The cost is read back
out of the value with no ceiling on it, since the ceiling would have to be the
one `WithCost` accepts: a corrupted or attacker-written row can therefore make
one verification arbitrarily slow, and a service that has to bound that bounds
the row.

The two refusals are distinct on purpose. `ErrMismatch` is an answer about a
password; `ErrUnreadable` is the verification never having run, and names a row
nobody can authenticate against until it is rewritten. Both deny, neither is
nil, and a caller that folds them into one watches an unreadable store look
like a user who keeps mistyping. `ErrMismatch` is the same error whatever the
password was, and neither carries any part of the password or of the stored
value.

`Bound` wraps any `Hasher`, the one in the box or a service's own, so that at
most a number of its calls the service names run at once. A call arriving while
every place is held is refused at once under `ErrBusy`, the wrapped hasher never
seeing it, rather than queued. Hash and Verify draw on the one budget, since
both spend the same CPU, and a place is given back however the call returns, a
panic included. A number below one, and a nil hasher, are refused at wiring.
`ErrBusy` is the third refusal and is distinct from the other two on purpose: a
caller folding it into `ErrMismatch` shows an honest user a wrong password under
load and feeds any lockout it counts. It denies, is never nil, and is one fixed
error carrying no part of the password nor of the stored value. What the bound
trades is a flood refused rather than slowed: enough logins, or a few rows
carrying an attacker-written cost, hold every place and every honest login is
refused until one frees. Metering callers in front (`httpd.LimitRate`) and
bounding the rows stored are what limit that. The budget is the process's own,
so three replicas hold three.

The plaintext is not erased from memory, and nothing here pretends otherwise: a
Go string is immutable and copied by the collector. That is the bound a service
holding one for longer than a request should know.

## crypto/token

Issues an opaque bearer token, and parses it back when it arrives. Stdlib only.

```go
tk := token.Issue()                   // 256 bits from crypto/rand
text := tk.Reveal()                   // 43 characters, base64url: the response hands it out once
stored, err := tk.Stored()            // hex SHA-256 of the secret: the column, under a unique index

text, ok := authctx.Bearer(r.Header)  // on a later request
tk, err = token.Parse(text)           // ErrMalformed on anything Issue did not write
stored, err = tk.Stored()             // the row to look up; the row is the verdict

fmt.Sprintf("%v %s %q %+v %#v", tk, /*…*/) // [REDACTED], every verb
json.Marshal(tk)                      // "[REDACTED]"
slog.Info("issued", slog.Any("token", tk)) // token=[REDACTED]
json.Unmarshal(body, &req)            // a Token field parses like Parse, ErrMalformed otherwise
```

The text leaves the carrier through `Reveal` alone. What a service writes to
disk is the stored value, which replays nothing: the secret carries 256 bits,
so there is no preimage to search, and a leaked table or a logged row opens no
session. It is deterministic so the service finds its row by it.

Every other surface redacts, as a `password.Password` does and by the same
hooks: `String`, `GoString` for `%#v`, `MarshalJSON`, `MarshalText` and
`LogValue` each answer the one fixed `[REDACTED]`, the zero value included. The
secret sits behind a pointer, so a struct holding a carrier in an unexported
field, out of reach of those hooks, prints an address and no part of the text.
Marshalling redacts instead of failing, and the response handing a token out
writes `Reveal` into its own field. A request body decodes into the carrier
directly: `UnmarshalJSON` and `UnmarshalText` parse like `Parse`, and a JSON
`null`, a JSON value that is not a string and any text `Parse` refuses are
refused under `ErrMalformed`.

It is not a `password.Hasher`'s output on purpose. A salted, slow derivation
cannot be looked up by value, so it would force a selector into the token and
spend on every request the CPU `password.Bound` exists to limit, to protect a
secret that is already unguessable.

A token that parses is well formed, not valid. This package identifies nobody:
the verdict is the service's row, carrying its own expiry and revocation, and a
`Resolver` treating a parse success as authentication fails open. `Parse`
checks the length first, fixed at 43, so a parse costs the same whatever
arrives. It accepts only the one text `Issue` would have written for a secret:
a padded text, a line break or the unused low bits of the last character set
differently decode to the same secret and are refused. Every refusal is the
one fixed `ErrMalformed`, carrying no part of what arrived. A stored value is
64 characters and never parses.

The zero value reveals `""` and answers `ErrEmpty` for a stored value, so no
row is ever keyed on a token nothing issued. The package is named `token` and
shadows `go/token`, which a service has no use for; `opaque` would read, under
`crypto/`, as the OPAQUE protocol. It writes no log line and does not erase the
text from memory.

## storage/keyset

Serves a list page by page, newest first, keyed on a `sortid.ID`, from the
cursor the previous page returned. Stdlib and `sortid` only, in the root
module.

```go
pages, err := keyset.New(25, 100) // at wiring: default size, largest size
q, err := pages.Query(keyset.Texts{
    Size:   r.URL.Query().Get("size"),
    Cursor: r.URL.Query().Get("cursor"),
})
if errors.Is(err, keyset.ErrSize) || errors.Is(err, keyset.ErrCursor) {
    // the handler's refusal, under the status it owns
}
rows, err := s.list(ctx, uuid.UUID(q.Bound()), q.Limit())
//   SELECT ... WHERE owner = $1 AND id < $2 ORDER BY id DESC LIMIT $3
page, next, err := keyset.Page(q, rows, func(o Order) sortid.ID { return o.ID })
// next is "" on the last page
```

The package runs no query. The table, the filters that scope the list to the
caller and the driver are the service's; what the package owns is the bound,
the limit and the cursor. A helper running the query in `storage/postgres`
could carry neither the table nor the filters, and a handler would import pgx's
module to read a cursor off a request.

`Query` asks for one row more than the page size. When the query returns it,
the page is full and `next` is the id of its last row; when it does not, the
page is the last one and `next` is `""`, including a last page holding exactly
the page size. An empty list gives an empty, allocated page, so a response
writes `[]` and not `null`. The first page's bound compares above every id; it
is no id itself and fails `MarshalText`, so the service passes it to its query
as `uuid.UUID(q.Bound())` and never writes it out.

Rows minted after a page was read sort above its cursor, so the walk's next
pages do not return them, and no older row is skipped or repeated.

`Page` checks what the query returned before writing a cursor from it: more
rows than the limit, a row at or above the bound, two rows out of descending
order, or an id of another version than 7 is the service's query breaking its
contract. It is refused under an error that is neither `ErrCursor`, `ErrSize`
nor `sortid.ErrMalformed`, so a service mapping those to a client error does
not blame the client for its own query, and no page nor cursor is returned.

A size is a decimal integer from 1 to the largest named at wiring, in ASCII
digits only, its length checked against the largest's digits first. Anything
else, 0, a sign, a space, a fraction, a text wider than the largest can be, is
the one fixed `ErrSize`, carrying no part of what arrived. The empty text is no
size and serves the default.

A cursor is the text of an id, parsed like `sortid.Parse`, its length checked
first. Anything else, an id of another version, a truncated or an over-long
text, is the one fixed `ErrCursor`, carrying no part of what arrived. The empty
text is no cursor and starts at the newest row.

`New` refuses a largest size below 1 or at `math.MaxInt`, whose row limit would
overflow, or a default size below 1 or above the largest, naming the value, and
returns no pager.

The cursor grants nothing and is not signed. A client may write any cursor the
package accepts, which only moves where the walk starts within the rows the
service's own query scopes to the caller: the query's `WHERE`, not the cursor,
authorizes. A cursor carries an id, so it publishes that row's creation time as
the id does.

## storage/postgres

Opens a `*pgxpool.Pool` from a DSN. No wrapper type, no ping: pgxpool connects
lazily, and reachability is `health`'s question.

Every statement is bounded by default. `New` sets the server's
`statement_timeout` to `DefaultStatementTimeout` (30s) on every connection, so
a service running a longer statement names a longer bound with
`WithStatementTimeout`. A service behind a stock PgBouncer fails its first
connection, since PgBouncer refuses the parameter at startup: it passes
`WithoutStatementTimeout`, and its bound is then the role's or the pooler's.

```go
pool, err := postgres.New(ctx, dsn,
    postgres.WithMaxConns(25),
    postgres.WithStatementTimeout(5*time.Second),
)
if err != nil {
    return fmt.Errorf("db: %w", err)
}
defer pool.Close()
```

`New` owns the pool size. A `pool_max_conns` in the DSN is overwritten by
`DefaultMaxConns` (10) or by `WithMaxConns`.

`New` owns the bound the same way. A `statement_timeout` in the DSN is
overwritten by the default or by `WithStatementTimeout`, and dropped by
`WithoutStatementTimeout`. The bound travels in whole milliseconds: `New`
refuses one under a millisecond or over 2147483647 ms, and returns no pool.
The server enforces it on every statement, whether or not the call carries a
context deadline.

`Classify` reads the error pgx returned, before a repository maps it to its
own kinds, so a service tests one package's vocabulary instead of pgx's and
pgconn's:

```go
_, err := pool.Exec(ctx, insertUser, u.Email)
switch err := postgres.Classify(err); {
case err == nil:
    return nil
case errors.Is(err, postgres.ErrUniqueViolation) && postgres.ConstraintName(err) == "users_email_key":
    return user.ErrEmailTaken
default:
    return fmt.Errorf("insert user: %w", err)
}
```

| pgx returned | `Classify` returns |
|---|---|
| nil | nil |
| no row for a single-row query | `ErrNoRows`, which `errors.Is` also matches to `pgx.ErrNoRows` |
| a refusal under SQLSTATE 23505 | a value matching `ErrUniqueViolation`; `ConstraintName` names the index |
| an error already read, wrapped or not | the same error, unchanged |
| anything else | `*Error`, whose `Unwrap` reaches the driver's error |

`errors.As` to `*pgconn.PgError` and `errors.Is` to a context error still work
through `*Error`. Neither `ErrNoRows` nor a unique violation is an `*Error`.

The rule is positional. `Classify` cannot tell a driver failure from an error
a callback handed back through `pgx.BeginFunc`, and takes both as the
driver's. Read errors inside the transaction callback and return what
`Classify` gave you: it comes back unchanged through `BeginFunc`.

A unique violation's message never carries the server's detail, which quotes
the colliding row (an email, a username). Nor does `*Error`'s, for a server
refusal: it reads `pg: server error, SQLSTATE 22P02`, the code alone, since
the server's message and detail can quote a value the statement sent (an
invalid input syntax does). Any other cause, a network, context or pool
failure, keeps its text after `pg: `. The full cause stays reachable through
`errors.As` to `*pgconn.PgError`.

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
tel, err := otel.Setup(ctx, "users",
    otel.WithServiceVersion(build.Version),
    otel.WithOTLPEndpoint(cfg.OTLPEndpoint), // "" reports nowhere
)
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

`WithOTLPEndpoint("")`, like no `WithOTLPEndpoint` at all, installs providers
with no exporter, so the instrumented code runs unchanged on a laptop and in a
test.

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
tel, err := otel.Setup(ctx, "users", otel.WithMetricReader(reader))
...
mux.Handle("GET /metrics", prometheus.Handler(reg))
```

The instruments stay OpenTelemetry's, so a service switches between scraping
and pushing by changing this wiring in main and nothing else.

It is a separate module from `telemetry/otel` and requires neither it nor the
reverse: a pushed service never sees the Prometheus dependency, and a scraped
one plugs in through the SDK's own `Reader` interface.
