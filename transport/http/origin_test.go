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

// grantedTable is one table served behind an origin grant: a route the service
// left open, a route it covered, and the resolver that cover was given.
type grantedTable struct {
	srv      *httptest.Server
	open     *marker
	covered  *reader
	resolver *resolver
}

// granting builds that table behind a grant listing origins. This is the seam:
// a consumer meets the grant through the handler its service serves, one
// served request at a time.
func granting(t *testing.T, origins ...string) grantedTable {
	t.Helper()

	open, covered, rv := &marker{name: "list"}, &reader{}, &resolver{}
	table, err := httpd.NewRouter(
		httpd.Route{Method: http.MethodGet, Pattern: "/things", Handler: open},
		httpd.Route{
			Method:  http.MethodGet,
			Pattern: "/guarded",
			Handler: httpd.RequireBearer(principalKey, rv.resolve)(covered),
		},
	)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	grant, err := httpd.GrantOrigins(origins...)
	if err != nil {
		t.Fatalf("GrantOrigins: %v", err)
	}

	srv := httptest.NewServer(grant(table))
	t.Cleanup(srv.Close)
	srv.Client().CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	return grantedTable{srv: srv, open: open, covered: covered, resolver: rv}
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

	g := granting(t, listedOrigin, otherListed)

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

			g := granting(t, listedOrigin, otherListed)

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

	g := granting(t, listedOrigin, otherListed)

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

			grant, err := httpd.GrantOrigins(tc.origins...)

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
