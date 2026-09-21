# json-envelope
> one body shape for every response, errors included, and a request body bounded to a size the caller names

**Created**: 2026-09-21
**Out of scope**: health's probe bodies and its own writer, a shape orchestrators already read; the 404 and 405 responses the router writes, and panic recovery; any router, middleware chain or content negotiation.
**Approach**: Free functions in `transport/http` (package `httpd`), stdlib only: one envelope carrying either a data member or an error member and never both, a writer for each side, and a decoder that bounds the body to the caller's byte limit and refuses through that same error writer. Not a middleware that rewraps every response, and not a new sibling package: buffering and re-encoding each response costs a copy and hides the status the handler chose, while a second package would split the one HTTP brick without removing a single dependency.
**Threat surface**: input: every request body the decoder reads, oversized and malformed ones included, bounded by a byte limit the caller names; secrets: the error envelope reaches the client, so a decode failure's detail stays out of it; no authn/authz, no egress, no dependency added.
**Landmarks**: transport/http/: package `httpd`, root module, stdlib only, the server-lifecycle brick this lands beside; health/: the repo's only JSON writer today, unexported, its probe shape fixed and out of scope; go.mod: the root module, no requirement outside the stdlib; Makefile: `make check` is fmt-check, vet and race over every module; README.md: one H2 per package, its shape and the reason behind it.

## Steps
1. [transport] Answer in one envelope, success and failure alike
   - seam: httpd's exported writers, called from the package's external test
   - acceptance: a success write answers with the status it was given, the payload under a single data member and a JSON content type; a failure write answers with its status, a machine-readable code and a message under a single error member; no body carries both members, and a failure body carries no data member at all

2. [transport] Read a request body bounded to a size the caller names
   - seam: httpd's exported decoder, called from the package's external test over a served request
   - acceptance: a body within the limit lands in the caller's value; a body one byte over it is refused with 413 and the failure envelope; a malformed body is refused with 400 and the failure envelope; two calls given different limits refuse at different sizes; neither refusal's body repeats anything of the body it rejected

## Execution
- step-01
- step-02
