package httpd

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
)

// The codes a router refuses with: CodeNoSuchRoute when no route of its table
// names the path asked, CodeMethodNotAllowed when routes name that path and
// none of them names the method.
const (
	CodeNoSuchRoute      ErrorCode = "no_such_route"
	CodeMethodNotAllowed ErrorCode = "method_not_allowed"
)

// Fixed, and says nothing of the table: a refusal describes the request that
// was refused, never what else is served. The Allow header of a 405 is the one
// exception, and it is bounded by what the caller already named.
const (
	messageNoSuchRoute      = "no route serves this request"
	messageMethodNotAllowed = "this method is not allowed on this path"
)

// Route is one route a service opens: the method and the pattern that reach
// Handler, and nothing else does.
//
// Pattern is an http.ServeMux pattern without its method or host, so "/things"
// or "/things/{id}", whose value the handler reads with Request.PathValue.
type Route struct {
	Method  string
	Pattern string
	Handler http.Handler
}

// router answers the routes it was built from through their handlers, and
// every other request through the failure envelope.
//
// methods is every method the table names anywhere, sorted and without
// repetition. It is the candidate set a refusal asks the mux about, and
// sorting it at wiring is what makes an Allow header depend on the routes a
// service named rather than on the order it named them in.
type router struct {
	mux     *http.ServeMux
	methods []string
}

// named wraps a handler the service named, so a match can be told apart from
// what ServeMux answers on its own: its plain-text 404, its 405, and the
// redirects it offers towards a route the request did not ask for.
type named struct{ http.Handler }

// isNamed reports whether what the mux matched is a route the service named,
// rather than one of ServeMux's own answers.
func isNamed(h http.Handler) bool {
	_, ok := h.(named)
	return ok
}

// NewRouter returns a handler serving exactly routes: a request whose method
// and pattern one of them names reaches that route's handler, and every other
// request is answered in the package's failure envelope. A path routes name
// with other methods is answered 405 under CodeMethodNotAllowed, with an Allow
// header naming exactly those methods; anything else is answered 404 under
// CodeNoSuchRoute, naming no method. Nothing of a refused request is read, and
// nothing of it or of the rest of the table comes back in the answer.
//
//	h, err := httpd.NewRouter(
//	    httpd.Route{Method: http.MethodGet, Pattern: "/things/{id}", Handler: show},
//	)
//
// It refuses a route that would open more than the service named: a missing
// method opens every method on the pattern, a pattern that does not start with
// "/" names a host rather than a path, and a nil handler is a route that
// panics the first time it is reached.
//
// A pattern ServeMux itself refuses, and a route named twice, panic at wiring:
// that is a bug in the table, and ServeMux names it more precisely than this
// could.
func NewRouter(routes ...Route) (http.Handler, error) {
	mux := http.NewServeMux()
	methods := make([]string, 0, len(routes))
	for i, r := range routes {
		if err := check(r); err != nil {
			return nil, fmt.Errorf("httpd: route %d (%q %q): %w", i, r.Method, r.Pattern, err)
		}
		mux.Handle(r.Method+" "+r.Pattern, named{r.Handler})
		if !slices.Contains(methods, r.Method) {
			methods = append(methods, r.Method)
		}
	}
	slices.Sort(methods)
	return &router{mux: mux, methods: methods}, nil
}

// check refuses what ServeMux would accept while serving more than the route
// says. Everything else is left to ServeMux's own parsing.
func check(r Route) error {
	switch {
	case r.Method == "":
		return errors.New("no method: a route without one answers every method on its pattern")
	case strings.ContainsAny(r.Method, " \t"):
		return errors.New("blank in the method: ServeMux would read the field before it as the method, an empty one included")
	case !strings.HasPrefix(r.Pattern, "/"):
		return errors.New("pattern does not start with /: ServeMux would read it as a host")
	case r.Handler == nil:
		return errors.New("no handler")
	}
	return nil
}

// ServeHTTP hands a named request to its route and refuses every other one,
// 405 when the table names the path asked and 404 when it does not.
//
// The table is consulted without being served on purpose. Handler reports what
// matched without answering, which is how ServeMux's own answers are kept off
// the wire entirely: its 404 and 405 carry a plain-text body outside the
// envelope, and its redirects would send a client on towards a route it never
// named. Only ServeHTTP fills the pattern values the handler then reads back,
// so the dispatch, once the answer is ours to give, goes back through it.
func (rt *router) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h, _ := rt.mux.Handler(r)
	if isNamed(h) {
		rt.mux.ServeHTTP(w, r)
		return
	}
	if allow := rt.allow(r); len(allow) > 0 {
		w.Header().Set("Allow", strings.Join(allow, ", "))
		WriteError(w, http.StatusMethodNotAllowed, CodeMethodNotAllowed, messageMethodNotAllowed)
		return
	}
	WriteError(w, http.StatusNotFound, CodeNoSuchRoute, messageNoSuchRoute)
}

// allow lists the methods the table names for the path this request asks for,
// sorted, and nothing for a path no route of the table names.
//
// The mux is asked once per method the table names anywhere, rather than a
// second table of methods per pattern being kept beside it: which pattern
// answers a path, wildcards, precedence and the trailing slash included, is
// ServeMux's to decide, and a table deciding it here would drift from the one
// that serves. The cost is bounded by the methods named at wiring, and it is
// paid on a refusal only.
//
// The request is copied to carry each candidate method: Handler reads it and
// answers nothing, and the copy is what keeps the method the client sent off
// the request the rest of this answer is written from.
func (rt *router) allow(r *http.Request) []string {
	var allow []string
	probe := *r
	for _, m := range rt.methods {
		probe.Method = m
		if h, _ := rt.mux.Handler(&probe); isNamed(h) {
			allow = append(allow, m)
		}
	}
	return allow
}
