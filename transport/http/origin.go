package httpd

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// The header a browser states its own origin in. It is also the value of the
// Vary header every answer here carries, a name and a dependency being the
// same word in that one place.
const headerOrigin = "Origin"

// GrantOrigins lets a browser at one of origins read the answers the wrapped
// handler gives. The list is fixed at wiring, and an origin is granted by
// being equal to an entry of it, whole: nothing here splits, lowercases or
// suffix-matches what a request carried.
//
//	h, err := httpd.NewRouter(...)
//	grant, err := httpd.GrantOrigins("https://app.example.test")
//	srv := httpd.New(":8080", grant(h))
//
// The wrap sits in front of the table rather than on a handler: it answers
// nothing and only adds headers to whatever the table answers, so a listed
// origin reads the table's own 404 and 405, and a covered route's 401 and 403,
// as it reads a handler's own answer. Listing an origin is trusting it with
// what those refusals say.
//
// A request whose Origin header is on the list is answered with
// Access-Control-Allow-Origin naming that one origin and nothing else of the
// list. A request whose origin is on it nowhere, and one carrying no origin at
// all, is answered exactly as it would have been without that header: the same
// status, the same body, no grant. Origin is a browser's own statement about
// itself, which anything that is not a browser forges in one header, so
// refusing on it gates nobody, while a same-origin POST sends one too and
// would meet that refusal from the service's own page. It costs a non-browser
// caller every sign that its origin was not listed, an absent header being the
// whole verdict.
//
// Every answer carries Vary: Origin, granted or not: the headers depend on
// which origin asked, and without it a shared cache hands one origin's grant
// to the next caller.
//
// No answer grants a wildcard, and none carries
// Access-Control-Allow-Credentials: a browser sending a cookie or a credential
// of its own is refused the read, so nothing a viewer carries is granted to a
// page on the strength of this list alone.
func GrantOrigins(origins ...string) (func(http.Handler) http.Handler, error) {
	// The caller keeps the slice it passed, a variadic call site keeping its
	// backing array: what is compared against has to be this package's own.
	listed := slices.Clone(origins)
	for i, o := range listed {
		if err := checkOrigin(o); err != nil {
			return nil, fmt.Errorf("httpd: origin %d (%q): %w", i, o, err)
		}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Both are written before the table runs, so they reach the wire
			// with whatever status it answers: this wrap sees no answer, and
			// a header set after the handler wrote its own would be too late.
			w.Header().Add("Vary", headerOrigin)
			if origin := r.Header.Get(headerOrigin); slices.Contains(listed, origin) {
				w.Header().Set("Access-Control-Allow-Origin", origin)
			}
			next.ServeHTTP(w, r)
		})
	}, nil
}

// checkOrigin refuses an entry no Origin header could ever equal. An origin is
// a scheme and a host, and rebuilding the entry from what url.Parse read is
// the one rule that says so: a path, a query, a trailing slash, credentials or
// a missing scheme all fail that comparison. Such an entry would be listed and
// never granted, so it is refused where the mistake is, at wiring, rather than
// read as a grant nobody ever gets.
func checkOrigin(origin string) error {
	// A wildcard passes the rule above inside a host, so it is named here: this
	// compares whole values and matches no pattern, and an entry carrying one
	// promises a match it would never make.
	if strings.Contains(origin, "*") {
		return errors.New("a wildcard: an origin is granted by being equal to an entry, and no pattern is matched here")
	}
	u, err := url.Parse(origin)
	if err != nil || u.Scheme == "" || u.Host == "" || origin != u.Scheme+"://"+u.Host {
		return errors.New("not a scheme and a host alone: an Origin header carries nothing else, so no request would ever equal it")
	}
	return nil
}
