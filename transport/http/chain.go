package httpd

import (
	"fmt"
	"net/http"
	"slices"
)

// Middleware wraps the handler of a route: RequireBearer, RequireRight and
// LimitRate each return one, and a service's own wraps are one too.
//
// It is func(http.Handler) http.Handler under a name, so a value of either
// type assigns to the other: a wrap written against the unnamed type hands
// itself to Chain as it is, and a variable of that type still takes what the
// package's wraps return.
type Middleware func(http.Handler) http.Handler

// Chain composes the wraps a route carries into the one wrap its handler is
// given, at wiring. The first listed is the outermost, so a request meets
// them in the order they are written and the handler last.
//
//	covered := httpd.Chain(auth, listing, meter) // each is a wrap of yours
//	h, err := httpd.NewRouter(
//	    httpd.Route{Method: http.MethodGet, Pattern: "/things", Handler: covered(list)},
//	    httpd.Route{Method: http.MethodGet, Pattern: "/status", Handler: status},
//	)
//
// Composing changes no wrap's answer and covers nothing by itself: coverage is
// still exactly the set of handlers a chain is set on, named where the route
// is, and the table's 404, 405 and preflight still answer before any of it
// runs. That is why it is set on a handler and not in front of the table,
// where it would ask a resolver about a path nobody named.
//
// The order is the service's. A chain naming RequireRight before
// RequireBearer reaches the right with no principal, and is answered 500 on
// every request rather than served. A chain of no wrap serves the handler as
// it is.
//
// A nil wrap panics here, naming its position: it is a bug in the table, and
// found at wiring it is not found by the first request to reach that route.
func Chain(ms ...Middleware) Middleware {
	for i, m := range ms {
		if m == nil {
			panic(fmt.Sprintf("httpd: Chain: wrap %d is nil", i))
		}
	}
	// A copy, so a caller reusing the slice it spread here does not rewrite a
	// chain already set on its routes.
	ms = slices.Clone(ms)

	return func(next http.Handler) http.Handler {
		for i := len(ms) - 1; i >= 0; i-- {
			next = ms[i](next)
		}
		return next
	}
}
