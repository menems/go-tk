package httpd_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/menems/go-tk/authctx"
	"github.com/menems/go-tk/transport/http"
)

// principal stands for whatever authenticating produces: a service's user id,
// its tenant, its whole session record. What is under test is that the one the
// resolver answered is the one the handler reads back.
type principal struct{ Name string }

// The key a service declares once, next to the wrap that fills it. It is read
// from every test, written by none, so the parallel tests below share nothing.
var principalKey = authctx.NewKey[principal]("principal")

// The token the resolver below accepts, the one it refuses, and the one it
// cannot answer about at all.
const (
	goodToken   = "good-token"
	badToken    = "marker-token"
	brokenToken = "marker-broken-token"
)

// The texts the resolver puts in its own errors, and what the no-echo
// assertions look for in the answers those two requests get: one names the
// credential that was read, the other the resolver's own innards.
const (
	refusalText = "marker-token belongs to nobody"
	failureText = "marker-store unreachable"
)

// resolver stands for the service's own verification: it accepts one token,
// refuses another with the sentinel carrying that verdict, and answers a third
// with a failure of its own instead of a verdict. It records how many times it
// was asked anything, so a request that must reach no resolver can be checked
// to have reached none.
type resolver struct{ calls atomic.Int64 }

func (rv *resolver) resolve(_ context.Context, token string) (principal, error) {
	rv.calls.Add(1)
	switch token {
	case goodToken:
		return principal{Name: "owner"}, nil
	case brokenToken:
		// Not a verdict: this stands for the store being down or the key
		// material being unreadable, neither of which is the token's fault.
		return principal{}, errors.New(failureText)
	default:
		return principal{}, fmt.Errorf("%s: %w", refusalText, httpd.ErrUnauthenticated)
	}
}

// reader is a route's handler: it answers with the principal it finds in the
// request context, and with "anonymous" when it finds none, so a context
// carrying no principal is told apart from one carrying an empty one.
type reader struct{ ran atomic.Bool }

func (h *reader) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.ran.Store(true)

	p, ok := principalKey.From(r.Context())
	if !ok {
		httpd.WriteJSON(w, http.StatusOK, payload{Name: "anonymous"})
		return
	}
	httpd.WriteJSON(w, http.StatusOK, payload{Name: p.Name})
}

// guarded is one table of two routes: one the service covered and one it did
// not, each with its own handler, plus the resolver the cover was given.
type guarded struct {
	srv      *httptest.Server
	resolver *resolver
	covered  *reader
	open     *reader
}

// guard builds that table. This is the seam: a consumer meets the wrap through
// the handler NewRouter returns, one served request at a time.
func guard(t *testing.T) guarded {
	t.Helper()

	rv := &resolver{}
	covered, open := &reader{}, &reader{}
	srv := gate(t,
		httpd.Route{
			Method:  http.MethodGet,
			Pattern: "/things",
			Handler: httpd.RequireBearer(principalKey, rv.resolve)(covered),
		},
		httpd.Route{Method: http.MethodGet, Pattern: "/status", Handler: open},
	)

	return guarded{srv: srv, resolver: rv, covered: covered, open: open}
}

// withBearer sets the Authorization header of req, and leaves it unset for an
// empty value, which is the request carrying no credential at all.
func withBearer(req *http.Request, header string) *http.Request {
	if header != "" {
		req.Header.Set("Authorization", header)
	}
	return req
}

// assertUnauthenticated checks the answer is the wrap's 401 envelope, with the
// challenge naming the scheme to retry under and nothing else.
func assertUnauthenticated(t *testing.T, status int, answer []byte, header http.Header) {
	t.Helper()

	assertRefusal(t, status, answer, http.StatusUnauthorized, httpd.CodeUnauthenticated, "this request carries no credential this service accepts")
	if got, want := header.Values("WWW-Authenticate"), "Bearer"; len(got) != 1 || got[0] != want {
		t.Errorf("WWW-Authenticate = %q, want [%q]", got, want)
	}
}

func TestBearerReachesTheHandlerWithThePrincipalItsResolverProduced(t *testing.T) {
	t.Parallel()

	g := guard(t)

	req := withBearer(ask(t, g.srv, http.MethodGet, "/things", nil), "Bearer "+goodToken)
	status, answer := send(t, g.srv, req)

	assertServed(t, status, answer, "owner")
	if got := g.resolver.calls.Load(); got != 1 {
		t.Errorf("resolver asked %d times, want 1", got)
	}
}

func TestBearerRefusesACoveredRequestItCannotResolve(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		header    string
		wantCalls int64
	}{
		{name: "no Authorization header at all", header: "", wantCalls: 0},
		{name: "a scheme that is not bearer", header: "Basic dXNlcjpwYXNz", wantCalls: 0},
		{name: "the scheme and no token", header: "Bearer ", wantCalls: 0},
		{name: "a bearer token the resolver refuses", header: "Bearer " + badToken, wantCalls: 1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			g := guard(t)

			req := withBearer(ask(t, g.srv, http.MethodGet, "/things", nil), tc.header)
			status, answer, header := exchange(t, g.srv, req)

			assertUnauthenticated(t, status, answer, header)
			assertNoEcho(t, answer, badToken)
			assertNoEcho(t, answer, refusalText)
			if g.covered.ran.Load() {
				t.Error("the covered handler ran on a request the wrap refused")
			}
			if got := g.resolver.calls.Load(); got != tc.wantCalls {
				t.Errorf("resolver asked %d times, want %d", got, tc.wantCalls)
			}
		})
	}
}

// TestBearerTellsAResolverRefusalFromAResolverFailure pins the two answers a
// resolver's error can get, side by side: a client branches on the status and
// the code, and on nothing else, because nothing else of what arrived, or of
// what the resolver said about it, is in either answer.
func TestBearerTellsAResolverRefusalFromAResolverFailure(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name          string
		token         string
		wantStatus    int
		wantCode      httpd.ErrorCode
		wantMessage   string
		wantChallenge []string
		unsaid        string
	}{
		{
			name:          "a token its resolver refuses",
			token:         badToken,
			wantStatus:    http.StatusUnauthorized,
			wantCode:      httpd.CodeUnauthenticated,
			wantMessage:   "this request carries no credential this service accepts",
			wantChallenge: []string{"Bearer"},
			unsaid:        refusalText,
		},
		{
			// No challenge on this one: a scheme to retry under would claim a
			// verdict was reached, and none was.
			name:        "a token its resolver could not answer about",
			token:       brokenToken,
			wantStatus:  http.StatusInternalServerError,
			wantCode:    httpd.CodeAuthUnavailable,
			wantMessage: "this service could not answer whether this request is authenticated",
			unsaid:      failureText,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			g := guard(t)

			req := withBearer(ask(t, g.srv, http.MethodGet, "/things", nil), "Bearer "+tc.token)
			status, answer, header := exchange(t, g.srv, req)

			assertRefusal(t, status, answer, tc.wantStatus, tc.wantCode, tc.wantMessage)
			if got := header.Values("WWW-Authenticate"); !slices.Equal(got, tc.wantChallenge) {
				t.Errorf("WWW-Authenticate = %q, want %q", got, tc.wantChallenge)
			}
			assertNoEcho(t, answer, tc.token)
			assertNoEcho(t, answer, tc.unsaid)
			if g.covered.ran.Load() {
				t.Error("the covered handler ran on a request the wrap did not authenticate")
			}
			if got := g.resolver.calls.Load(); got != 1 {
				t.Errorf("resolver asked %d times, want 1", got)
			}
		})
	}
}

// TestBearerLeavesARouteTheServiceDidNotCoverServingAnonymously pins coverage
// as exactly the set of handlers wrapped: the credential-less request the
// covered route refuses is served by the route beside it, whose handler finds
// no principal to read.
func TestBearerLeavesARouteTheServiceDidNotCoverServingAnonymously(t *testing.T) {
	t.Parallel()

	g := guard(t)

	status, answer := send(t, g.srv, ask(t, g.srv, http.MethodGet, "/status", nil))

	assertServed(t, status, answer, "anonymous")
	if g.covered.ran.Load() {
		t.Error("the covered handler ran on a route it does not serve")
	}
	if got := g.resolver.calls.Load(); got != 0 {
		t.Errorf("resolver asked %d times on an uncovered route, want 0", got)
	}
}

// TestBearerAsksNothingOfTheResolverOnAPathNobodyNamed pins the order the wrap
// sits in: the table answers first, so enumerating paths stays as anonymous as
// the router alone made it.
func TestBearerAsksNothingOfTheResolverOnAPathNobodyNamed(t *testing.T) {
	t.Parallel()

	g := guard(t)

	req := withBearer(ask(t, g.srv, http.MethodGet, "/secret-path", nil), "Bearer "+badToken)
	status, answer, header := exchange(t, g.srv, req)

	assertRefused(t, status, answer)
	assertNoEcho(t, answer, badToken)
	if got := header.Values("WWW-Authenticate"); len(got) != 0 {
		t.Errorf("WWW-Authenticate = %q, want none on a path nobody named", got)
	}
	if got := g.resolver.calls.Load(); got != 0 {
		t.Errorf("resolver asked %d times on a path nobody named, want 0", got)
	}
}
