# go-tk

Go toolkit. Bricks every service here rewrote, extracted once. `README.md` documents each package, its shape and the reason behind it: read the section of the package you touch before changing it.

## Agnostic by construction
go-tk imports no consumer module, and no package here carries a project fact: a domain name, a table, a route, an env var spelled for one service, a business rule. A package that needs one is not a toolkit package; it belongs in that consumer's `pkg/`. Failure mode: a change made for one service breaks the three others, and the toolkit becomes a shared `internal/`.

The rule holds downward too. `app`, `transport/http`, `authctx`, `health` and `config` reach for the stdlib alone. A brick that needs a third-party dependency gets its own module, never an import added to the root one.

A package hands back stdlib types and lets the caller write its own adapter. Nothing here imports chi, ConnectRPC or a framework.

## Modules
Five, one per dependency set, so importing one package cannot drag another's dependencies into a consumer's graph.

| Module | Packages | Outside the stdlib |
|---|---|---|
| `github.com/menems/go-tk` | `app`, `transport/http`, `authctx`, `health`, `config`, `crypto/password` | none |
| `github.com/menems/go-tk/storage/postgres` | `storage/postgres` | pgx |
| `github.com/menems/go-tk/storage/postgres/migrate` | `storage/postgres/migrate` | golang-migrate, pgx |
| `github.com/menems/go-tk/telemetry/otel` | `telemetry/otel` | OpenTelemetry |
| `github.com/menems/go-tk/telemetry/prometheus` | `telemetry/prometheus` | OpenTelemetry SDK, Prometheus |

Import paths are the module paths, so moving a package between modules changes nothing in a consumer.

A directory groups siblings, present or planned: `storage/mysql` and `transport/grpc` land next to their peers without moving anything. Package names match their directory, except `transport/http`, which is `package httpd`: `package http` would shadow `net/http` in every consumer that uses both.

## Releases
Each module carries its own tag: `v0.2.0`, `storage/postgres/v0.2.0`. `make release VERSION=vX.Y.Z` runs `make check` and pushes every tag. `go.work` is committed, so an edit across modules resolves locally without publishing anything.

## Plans
Implementation plans live in `docs/plans/<plan-name>.md`, a tracked path. `plan-feature` writes them and commits each one as the first commit of its `feat/<plan-name>` branch; `implement` reads them.

Worktrees: `.claude/worktrees`. `plan-feature` gives each plan a working directory of its own there, and this checkout stays on `main`. `merge` removes it.

`docs/plans/BACKLOG.md` lists the plans, in order, with the context that owns each one and their dependencies. `roadmap` appends to it on `main`; no branch edits it, no line is removed. It carries no status: `next-plan` computes merged / in flight / queued from git.

A context there is the directory the brick lands in; the module follows from its dependency set, so a plan that adds a third-party dependency adds a module. A merged plan is not yet importable next door: `make release` is what a consumer waits for.

## QA command
`make check`

Runs `fmt-check`, `vet` and `race` over each module in turn. Everything CI runs.

## Consumers
A consumer builds against this working copy through a workspace of its own, so an unpublished change is visible to it before a tag exists. Two consequences: a breaking change is felt next door immediately, and nothing merges there until the version it needs is tagged and required. A consumer never edits this repo from one of its own steps.

**Arrival.** A merged plan here is available to a consumer once a tag of its module contains it and that consumer requires the tag. The plan's commit is `git log main --grep='^plan(<name>): create' --format=%H`, the tags carrying it are `git tag --contains <sha>` filtered on the module's prefix, and what the consumer requires is its own `go.mod`. Merged here and untagged is the state `make release` closes, and it is the one blockage a consumer's queue cannot lift for itself.
