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

// The header a browser asks its preflight question in, and the three an answer
// gives back.
const (
	headerRequestMethod = "Access-Control-Request-Method"
	headerAllowOrigin   = "Access-Control-Allow-Origin"
	headerAllowMethods  = "Access-Control-Allow-Methods"
	headerAllowHeaders  = "Access-Control-Allow-Headers"
)

// Grant is the origin policy a service names once at wiring, and the value its
// two halves share: Wrap, written in front of the table, and Router, the table
// itself. Both compare against the same lists, so a service names its origins
// and its request headers in one place.
type Grant struct {
	origins []string
	headers []string
}

// GrantOrigins names that policy: origins are the origins a browser may read
// this service's answers from, requestHeaders the headers it may send with a
// request. Both are fixed at wiring, and an origin is granted by being equal to
// an entry of the list, whole: nothing here splits, lowercases or suffix-matches
// what a request carried.
//
//	grant, err := httpd.GrantOrigins(
//	    []string{"https://app.example.test"},
//	    []string{"Content-Type"},
//	)
//	h, err := grant.Router(httpd.Route{...})
//	srv := httpd.New(":8080", grant.Wrap(h))
//
// Neither list is read from a request: what a preflight is told is what the
// service named here. Each is validated against a rule the other's entries
// cannot pass, so the two handed over in the wrong order fail at wiring,
// naming the entry, rather than granting nothing quietly.
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
// No answer grants a wildcard, and none carries
// Access-Control-Allow-Credentials: a browser sending a cookie or a credential
// of its own is refused the read, so nothing a viewer carries is granted to a
// page on the strength of this list alone.
func GrantOrigins(origins, requestHeaders []string) (*Grant, error) {
	// The caller keeps the slices it passed: what is compared against has to be
	// this package's own.
	g := &Grant{origins: slices.Clone(origins), headers: slices.Clone(requestHeaders)}
	for i, o := range g.origins {
		if err := checkOrigin(o); err != nil {
			return nil, fmt.Errorf("httpd: origin %d (%q): %w", i, o, err)
		}
	}
	for i, h := range g.headers {
		if err := checkRequestHeader(h); err != nil {
			return nil, fmt.Errorf("httpd: request header %d (%q): %w", i, h, err)
		}
	}
	return g, nil
}

// Wrap is the half of the policy written in front of the table: it answers
// nothing and only adds headers to whatever the table answered, so a listed
// origin reads the table's own 404 and 405, and a covered route's 401 and 403,
// as it reads a handler's own answer. Listing an origin is trusting it with
// what those refusals say.
//
// Every answer carries Vary: Origin, granted or not: the headers depend on
// which origin asked, and without it a shared cache hands one origin's grant
// to the next caller. It is the one place that header is written, the
// preflight below being an answer this same wrap sits in front of.
func (g *Grant) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Both are written before the table runs, so they reach the wire
		// with whatever status it answers: this wrap sees no answer, and
		// a header set after the handler wrote its own would be too late.
		w.Header().Add("Vary", headerOrigin)
		if origin := r.Header.Get(headerOrigin); g.lists(origin) {
			w.Header().Set(headerAllowOrigin, origin)
		}
		next.ServeHTTP(w, r)
	})
}

// Router is the other half: the route table of NewRouter, answering besides it
// the preflight of a path it names, out of the methods it names for that path.
//
// A preflight is an answer and not a header, which is why it is given here
// rather than in Wrap: one asked on a path no route names is refused 404 like
// any other request, and one on a covered route is answered without its
// resolver being asked, the table answering before any wrap on a handler runs.
// A preflight carries no credential by construction, so a table built with
// NewRouter and wrapped with Wrap answers every preflight 405 and breaks every
// browser that needed one.
func (g *Grant) Router(routes ...Route) (http.Handler, error) {
	return newRouter(g, routes)
}

// lists reports whether origin is one the service named.
func (g *Grant) lists(origin string) bool {
	return slices.Contains(g.origins, origin)
}

// isPreflight reports whether r asks the question a browser sends before a
// request it may not send blind: the method it means to use. Only the presence
// of that header is read; its value reaches no comparison and no answer, a
// preflight being told the methods the path names whatever method it asked
// about.
func isPreflight(r *http.Request) bool {
	return r.Method == http.MethodOptions && r.Header.Get(headerRequestMethod) != ""
}

// preflight answers that question for a path the table names, allow being the
// methods it names there.
//
// An origin the service did not list is told none of it, and is told so by the
// same 204 a listed one gets: a status or a code of its own would report a
// mistyped list to a caller that forged the header, and Origin is a statement
// this package refuses on nobody (see GrantOrigins). The headers the request
// said it means to send are not read either: what is named back is the list the
// service fixed at wiring, and never a header it did not name.
func (g *Grant) preflight(w http.ResponseWriter, r *http.Request, allow []string) {
	if g.lists(r.Header.Get(headerOrigin)) {
		w.Header().Set(headerAllowMethods, strings.Join(allow, ", "))
		if len(g.headers) > 0 {
			w.Header().Set(headerAllowHeaders, strings.Join(g.headers, ", "))
		}
	}
	w.WriteHeader(http.StatusNoContent)
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

// The characters a header name is made of besides letters and digits, as
// RFC 9110 spells a token.
const tokenPunctuation = "!#$%&'*+-.^_`|~"

// checkRequestHeader refuses an entry that is not one header name. The list is
// written back as one comma-separated value, so an entry carrying a comma, a
// blank or a line break names headers the service never listed, and an empty
// one names nothing at all.
func checkRequestHeader(name string) error {
	if name == "" {
		return errors.New("an empty entry: it names no header a browser could send")
	}
	for i := range len(name) {
		c := name[i]
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
		case strings.IndexByte(tokenPunctuation, c) >= 0:
		default:
			return errors.New("not one header name: the list is written back as one value, so an entry carrying anything else names a header the service did not list")
		}
	}
	return nil
}
