package httpd_test

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/menems/go-tk/transport/http"
)

// The two origins the service lists in these tests. The second is listed and
// never asks for anything: what it pins is that an answer names the origin
// that asked and nothing else of the list.
const (
	listedOrigin = "https://app.example.test"
	otherListed  = "https://admin.example.test"
)

// The lists the service names at wiring in these tests: the origins it grants,
// and the request headers it lets a browser send.
var (
	listedOrigins = []string{listedOrigin, otherListed}
	listedHeaders = []string{"Content-Type", "X-Request-Id"}
)

// grantedTable is one table served behind an origin grant: a path the service
// left open under two methods, a route it covered, and the resolver that cover
// was given.
type grantedTable struct {
	srv      *httptest.Server
	open     *marker
	create   *marker
	covered  *reader
	resolver *resolver
}

// granting builds that table behind a grant listing origins and requestHeaders.
// This is the seam: a consumer meets the grant through the handler its service
// serves, one served request at a time.
func granting(t *testing.T, origins, requestHeaders []string) grantedTable {
	t.Helper()

	grant, err := httpd.GrantOrigins(origins, requestHeaders)
	if err != nil {
		t.Fatalf("GrantOrigins: %v", err)
	}

	open, create, covered, rv := &marker{name: "list"}, &marker{name: "create"}, &reader{}, &resolver{}
	table, err := grant.Router(
		httpd.Route{Method: http.MethodGet, Pattern: "/things", Handler: open},
		httpd.Route{Method: http.MethodPost, Pattern: "/things", Handler: create},
		httpd.Route{
			Method:  http.MethodGet,
			Pattern: "/guarded",
			Handler: httpd.RequireBearer(principalKey, rv.resolve)(covered),
		},
	)
	if err != nil {
		t.Fatalf("Router: %v", err)
	}

	srv := httptest.NewServer(grant.Wrap(table))
	t.Cleanup(srv.Close)
	srv.Client().CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	return grantedTable{srv: srv, open: open, create: create, covered: covered, resolver: rv}
}

// preflighting builds the question a browser asks before a request it cannot
// send blind: the origin it comes from and the method it means to send. The
// headers it means to send are set too, and no answer ever names them back.
func preflighting(t *testing.T, srv *httptest.Server, origin, path, method string) *http.Request {
	t.Helper()

	req := withOrigin(ask(t, srv, http.MethodOptions, path, nil), origin)
	req.Header.Set("Access-Control-Request-Method", method)
	req.Header.Set("Access-Control-Request-Headers", "x-marker")
	return req
}

// withOrigin sets the Origin header of req, and leaves it unset for an empty
// value, which is the request carrying no origin at all.
func withOrigin(req *http.Request, origin string) *http.Request {
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	return req
}

// headerText renders every header of an answer, so a test can check that
// nothing of the list the service named is named anywhere in it.
func headerText(t *testing.T, header http.Header) string {
	t.Helper()

	var b strings.Builder
	if err := header.Write(&b); err != nil {
		t.Fatalf("write headers: %v", err)
	}
	return b.String()
}

// assertGrant checks the answer grants exactly want, in one header value, and
// names no other origin the service listed.
func assertGrant(t *testing.T, header http.Header, want, unlisted string) {
	t.Helper()

	if got := header.Values("Access-Control-Allow-Origin"); len(got) != 1 || got[0] != want {
		t.Errorf("Access-Control-Allow-Origin = %q, want [%q]", got, want)
	}
	if text := headerText(t, header); strings.Contains(text, unlisted) {
		t.Errorf("the answer names %q, which this request never carried:\n%s", unlisted, text)
	}
}

// assertNoGrant checks the answer carries no grant header at all.
func assertNoGrant(t *testing.T, header http.Header) {
	t.Helper()

	if got := header.Values("Access-Control-Allow-Origin"); len(got) != 0 {
		t.Errorf("Access-Control-Allow-Origin = %q, want no grant header", got)
	}
}

// assertVary checks the answer says it depends on the origin that asked.
func assertVary(t *testing.T, header http.Header) {
	t.Helper()

	if got, want := header.Values("Vary"), "Origin"; len(got) != 1 || got[0] != want {
		t.Errorf("Vary = %q, want [%q]", got, want)
	}
}

// TestGrantOriginsAnswersAListedOriginAsItWouldHaveWithout puts the two
// answers of one route side by side: the same status and the same body, and
// the grant as the only difference between them.
func TestGrantOriginsAnswersAListedOriginAsItWouldHaveWithout(t *testing.T) {
	t.Parallel()

	g := granting(t, listedOrigins, listedHeaders)

	plainStatus, plainAnswer, plainHeader := exchange(t, g.srv, ask(t, g.srv, http.MethodGet, "/things", nil))
	assertServed(t, plainStatus, plainAnswer, "list")
	assertNoGrant(t, plainHeader)
	assertVary(t, plainHeader)

	status, answer, header := exchange(t, g.srv, withOrigin(ask(t, g.srv, http.MethodGet, "/things", nil), listedOrigin))

	if status != plainStatus {
		t.Errorf("status = %d, want %d, the status this request gets without an origin", status, plainStatus)
	}
	if got, want := string(answer), string(plainAnswer); got != want {
		t.Errorf("answer = %s, want %s, the answer this request gets without an origin", got, want)
	}
	assertServed(t, status, answer, "list")
	assertGrant(t, header, listedOrigin, otherListed)
	assertVary(t, header)
}

func TestGrantOriginsGrantsNothingToAnOriginNobodyListed(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		origin string
	}{
		{name: "an origin of its own", origin: "https://evil.example.test"},
		{name: "a listed origin under another scheme", origin: "http://app.example.test"},
		{name: "a listed origin under another port", origin: "https://app.example.test:8443"},
		{name: "a listed origin carrying a trailing slash", origin: "https://app.example.test/"},
		{name: "the parent domain of a listed origin", origin: "https://example.test"},
		{name: "a name a listed origin is a suffix of", origin: "https://evil.app.example.test"},
		{name: "the wildcard itself", origin: "*"},
		{name: "no origin at all", origin: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			g := granting(t, listedOrigins, listedHeaders)

			status, answer, header := exchange(t, g.srv, withOrigin(ask(t, g.srv, http.MethodGet, "/things", nil), tc.origin))

			assertServed(t, status, answer, "list")
			assertNoGrant(t, header)
			assertVary(t, header)
		})
	}
}

// TestGrantOriginsRidesOnTheAnswersNoHandlerGives pins the grant on the two
// answers written above every handler: the table's own refusal, and the one a
// covered route gives a request it could not authenticate.
func TestGrantOriginsRidesOnTheAnswersNoHandlerGives(t *testing.T) {
	t.Parallel()

	g := granting(t, listedOrigins, listedHeaders)

	t.Run("the table's own 404", func(t *testing.T) {
		status, answer, header := exchange(t, g.srv, withOrigin(ask(t, g.srv, http.MethodGet, "/nothing", nil), listedOrigin))

		assertRefused(t, status, answer)
		assertGrant(t, header, listedOrigin, otherListed)
		assertVary(t, header)
		if g.open.ran.Load() {
			t.Error("a named route ran on a path nobody named")
		}
	})

	t.Run("a covered route's 401", func(t *testing.T) {
		status, answer, header := exchange(t, g.srv, withOrigin(ask(t, g.srv, http.MethodGet, "/guarded", nil), listedOrigin))

		assertUnauthenticated(t, status, answer, header)
		assertGrant(t, header, listedOrigin, otherListed)
		assertVary(t, header)
		if g.covered.ran.Load() {
			t.Error("the covered route ran on a request carrying no credential")
		}
		if got := g.resolver.calls.Load(); got != 0 {
			t.Errorf("resolver asked %d times, want 0", got)
		}
	})
}

func TestGrantOriginsRefusesAListNoOriginCouldMatch(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		origins []string
		named   string
	}{
		{name: "the wildcard", origins: []string{"*"}, named: "*"},
		{name: "a wildcard inside a host", origins: []string{"https://*.example.test"}, named: "https://*.example.test"},
		{name: "an empty entry", origins: []string{""}, named: ""},
		{name: "an origin bearing a path", origins: []string{"https://app.example.test/app"}, named: "https://app.example.test/app"},
		{name: "an origin bearing a query", origins: []string{"https://app.example.test?tenant=1"}, named: "https://app.example.test?tenant=1"},
		{name: "an origin bearing a trailing slash", origins: []string{"https://app.example.test/"}, named: "https://app.example.test/"},
		{name: "an origin with no scheme", origins: []string{"app.example.test"}, named: "app.example.test"},
		{name: "an origin carrying credentials", origins: []string{"https://user:pass@app.example.test"}, named: "https://user:pass@app.example.test"},
		{name: "a bad entry after a good one", origins: []string{listedOrigin, "*"}, named: "*"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			grant, err := httpd.GrantOrigins(tc.origins, nil)

			if err == nil {
				t.Fatal("GrantOrigins returned no error, want one")
			}
			if grant != nil {
				t.Error("GrantOrigins returned a wrap beside the error, want none")
			}
			if got := err.Error(); !strings.Contains(got, strconv.Quote(tc.named)) {
				t.Errorf("error = %q, want it to name the entry %q", got, tc.named)
			}
		})
	}
}

// assertHeader checks name carries exactly want, in one value, and is absent
// from the answer altogether for an empty want.
func assertHeader(t *testing.T, header http.Header, name, want string) {
	t.Helper()

	got := header.Values(name)
	if want == "" {
		if len(got) != 0 {
			t.Errorf("%s = %q, want no such header", name, got)
		}
		return
	}
	if len(got) != 1 || got[0] != want {
		t.Errorf("%s = %q, want [%q]", name, got, want)
	}
}

// assertPreflight checks the answer is the one a preflight gets: 204, no body,
// and exactly the methods, the request headers and the origin named.
func assertPreflight(t *testing.T, status int, answer []byte, header http.Header, wantMethods, wantHeaders, wantOrigin string) {
	t.Helper()

	if status != http.StatusNoContent {
		t.Fatalf("status = %d, want %d (%s)", status, http.StatusNoContent, answer)
	}
	if len(answer) != 0 {
		t.Errorf("answer = %s, want no body", answer)
	}
	assertHeader(t, header, "Access-Control-Allow-Methods", wantMethods)
	assertHeader(t, header, "Access-Control-Allow-Headers", wantHeaders)
	assertHeader(t, header, "Access-Control-Allow-Origin", wantOrigin)
}

// TestGrantRouterAnswersThePreflightOfAPathTheTableNames is the acceptance: the
// table answers the question a browser asks before a request it cannot send
// blind, out of the methods it already names for that path, and no handler of
// that path runs.
func TestGrantRouterAnswersThePreflightOfAPathTheTableNames(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		origin      string
		path        string
		method      string
		wantMethods string
		wantHeaders string
		wantOrigin  string
	}{
		{
			name:        "a path the table names under two methods",
			origin:      listedOrigin,
			path:        "/things",
			method:      http.MethodPost,
			wantMethods: "GET, POST",
			wantHeaders: "Content-Type, X-Request-Id",
			wantOrigin:  listedOrigin,
		},
		{
			name:        "a route the bearer wrap covers",
			origin:      listedOrigin,
			path:        "/guarded",
			method:      http.MethodGet,
			wantMethods: "GET",
			wantHeaders: "Content-Type, X-Request-Id",
			wantOrigin:  listedOrigin,
		},
		{
			name:        "a method the table does not name for that path",
			origin:      listedOrigin,
			path:        "/things",
			method:      http.MethodDelete,
			wantMethods: "GET, POST",
			wantHeaders: "Content-Type, X-Request-Id",
			wantOrigin:  listedOrigin,
		},
		{name: "an origin nobody listed", origin: "https://evil.example.test", path: "/things", method: http.MethodPost},
		{name: "no origin at all", origin: "", path: "/things", method: http.MethodPost},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			g := granting(t, listedOrigins, listedHeaders)

			status, answer, header := exchange(t, g.srv, preflighting(t, g.srv, tc.origin, tc.path, tc.method))

			assertPreflight(t, status, answer, header, tc.wantMethods, tc.wantHeaders, tc.wantOrigin)
			assertVary(t, header)
			if text := headerText(t, header); strings.Contains(text, "x-marker") {
				t.Errorf("the answer names the headers this request asked about:\n%s", text)
			}
			if g.open.ran.Load() || g.create.ran.Load() || g.covered.ran.Load() {
				t.Error("a handler ran on a preflight")
			}
			if got := g.resolver.calls.Load(); got != 0 {
				t.Errorf("resolver asked %d times, want 0", got)
			}
		})
	}
}

// TestGrantRouterRefusesAPreflightOnAPathNobodyNamed pins that answering a
// preflight opened no path: one asked on a path outside the table meets that
// table's own 404, naming no method.
func TestGrantRouterRefusesAPreflightOnAPathNobodyNamed(t *testing.T) {
	t.Parallel()

	g := granting(t, listedOrigins, listedHeaders)

	status, answer, header := exchange(t, g.srv, preflighting(t, g.srv, listedOrigin, "/nothing", http.MethodGet))

	assertRefused(t, status, answer)
	assertHeader(t, header, "Access-Control-Allow-Methods", "")
	assertHeader(t, header, "Allow", "")
	assertGrant(t, header, listedOrigin, otherListed)
	assertVary(t, header)
}

// TestGrantRouterRefusesAnOptionsCarryingNoPreflightQuestion pins the other
// half of that path's answer: an OPTIONS naming no method it means to send is
// no preflight, so it is refused like any method nobody named, and the Allow
// header names OPTIONS because this table now answers it.
func TestGrantRouterRefusesAnOptionsCarryingNoPreflightQuestion(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		origin string
	}{
		{name: "carrying a listed origin", origin: listedOrigin},
		{name: "carrying no origin at all", origin: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			g := granting(t, listedOrigins, listedHeaders)

			status, answer, header := exchange(t, g.srv, withOrigin(ask(t, g.srv, http.MethodOptions, "/things", nil), tc.origin))

			assertNotAllowed(t, status, answer)
			assertAllow(t, header, "GET, OPTIONS, POST")
			assertHeader(t, header, "Access-Control-Allow-Methods", "")
			if g.open.ran.Load() || g.create.ran.Load() {
				t.Error("a named route ran on a method nobody named for its path")
			}
		})
	}
}

func TestGrantOriginsRefusesARequestHeaderNoBrowserCouldSend(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		headers []string
		named   string
	}{
		{name: "an empty entry", headers: []string{""}, named: ""},
		{name: "an entry naming two headers", headers: []string{"Content-Type, X-Request-Id"}, named: "Content-Type, X-Request-Id"},
		{name: "an entry bearing a blank", headers: []string{"X Request Id"}, named: "X Request Id"},
		{name: "an entry bearing a line break", headers: []string{"X-Request-Id\r\nX-Admin: 1"}, named: "X-Request-Id\r\nX-Admin: 1"},
		{name: "an origin, the two lists having been swapped", headers: []string{listedOrigin}, named: listedOrigin},
		{name: "a bad entry after a good one", headers: []string{"Content-Type", ""}, named: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			grant, err := httpd.GrantOrigins(listedOrigins, tc.headers)

			if err == nil {
				t.Fatal("GrantOrigins returned no error, want one")
			}
			if grant != nil {
				t.Error("GrantOrigins returned a grant beside the error, want none")
			}
			if got := err.Error(); !strings.Contains(got, strconv.Quote(tc.named)) {
				t.Errorf("error = %q, want it to name the entry %q", got, tc.named)
			}
		})
	}
}
