package httpd

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// CodeNoSuchRoute is what a router refuses a request no route of its table
// answers.
const CodeNoSuchRoute ErrorCode = "no_such_route"

// Fixed, and says nothing of the table: a refusal describes the request that
// was refused, never what else is served.
const messageNoSuchRoute = "no route serves this request"

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
type router struct {
	mux *http.ServeMux
}

// named wraps a handler the service named, so a match can be told apart from
// what ServeMux answers on its own: its plain-text 404, its 405, and the
// redirects it offers towards a route the request did not ask for.
type named struct{ http.Handler }

// NewRouter returns a handler serving exactly routes: a request whose method
// and pattern one of them names reaches that route's handler, and every other
// request is answered 404 in the package's failure envelope, under
// CodeNoSuchRoute. Nothing of a refused request is read, and nothing of it or
// of the table comes back in the answer.
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
	for i, r := range routes {
		if err := check(r); err != nil {
			return nil, fmt.Errorf("httpd: route %d (%q %q): %w", i, r.Method, r.Pattern, err)
		}
		mux.Handle(r.Method+" "+r.Pattern, named{r.Handler})
	}
	return &router{mux: mux}, nil
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

// ServeHTTP hands a named request to its route and refuses every other one.
//
// The table is consulted twice on purpose. Handler reports what matched
// without answering, which is how ServeMux's own answers are kept off the wire
// entirely: its 404 and 405 carry a plain-text body outside the envelope, and
// its redirects would send a client on towards a route it never named. Only
// ServeHTTP fills the pattern values the handler then reads back, so the
// dispatch, once the answer is ours to give, goes back through it.
func (rt *router) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h, _ := rt.mux.Handler(r)
	if _, ok := h.(named); !ok {
		WriteError(w, http.StatusNotFound, CodeNoSuchRoute, messageNoSuchRoute)
		return
	}
	rt.mux.ServeHTTP(w, r)
}
