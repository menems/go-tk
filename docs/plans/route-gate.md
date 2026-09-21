# route-gate
> a service closes its routes by default by naming only the open ones, and a request on a route nobody named is refused whatever it asks

**Created**: 2026-09-21
**Out of scope**: who may call a named route, which later plans own (a bearer, a principal, a per-route right); CORS and preflight; rate limiting; panic recovery and request logging; any helper over the matched pattern, such as reading a path parameter; serving anything the service did not name, static trees and redirects to a named route included.
**Approach**: A route table in `transport/http` (package `httpd`, root module, stdlib only): the service names each open route by its method, its pattern and its handler, and gets back an `http.Handler` that serves those and answers every other request through the failure envelope the package already writes. Not a gate placed in front of the caller's own router: each route would then be named twice, in two pattern syntaxes, and a gate that drifts from the router it guards opens exactly what it claims to close.
**Threat surface**: input: the request line itself, method and path, matched against a table fixed at wiring, nothing of an unmatched request being read, parsed or echoed back; authn/authz: this is the reachability boundary of a service, it decides what can be reached at all, while naming no caller and granting no right; secrets: a refusal describes the request and never the table, the one exception being the methods listed for a path the caller already named; no egress, no dependency added.
**Landmarks**: transport/http/: package `httpd`, root module, stdlib only, where this lands, beside the server-lifecycle brick and the `WriteError` / `ErrorCode` failure envelope a refusal goes out through; health/: the one handler the repo already serves, its probe shape fixed and out of scope; go.mod: the root module, nothing required outside the stdlib; Makefile: `make check` is fmt-check, vet and race over every module; README.md: one H2 per package, its shape and the reason behind it.

## Steps
1. [transport] Serve only the routes the service named
   - seam: the gate's `http.Handler`, called over a served request from the package's external test
   - acceptance: a request whose method and path the service named reaches that route's handler; a request on a path nobody named is answered 404 in the package's failure envelope, carrying a machine-readable code and a message, and no named handler runs; that same answer comes back whatever method, headers or body the request carried; a path differing from a named one only by a trailing slash is refused like any other path nobody named

2. [transport] Refuse a named path asked with a method nobody named for it
   - seam: the same gate handler, called over a served request from the package's external test
   - acceptance: a request on a named path carrying a method the service did not name for it is answered 405 in that same failure envelope, under a code of its own, and that path's handler does not run; the answer carries an `Allow` header naming exactly the methods the service named for that path; a second method named for the same path is served by its own handler; a path nobody named is still answered 404, and its answer names no method

## Execution
- step-01
- step-02
