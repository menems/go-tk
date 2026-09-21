package httpd_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/menems/go-tk/transport/http"
)

// marker is a named route's handler: it answers with its own name and records
// that it ran, so a refusal can be checked to have run none.
type marker struct {
	name string
	ran  atomic.Bool
}

func (m *marker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.ran.Store(true)
	httpd.WriteJSON(w, http.StatusOK, payload{Name: m.name})
}

// gate serves a router built over routes. This is the seam: a consumer meets
// the table as an http.Handler a server calls, one request at a time.
func gate(t *testing.T, routes ...httpd.Route) *httptest.Server {
	t.Helper()

	h, err := httpd.NewRouter(routes...)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	// A redirect is one of the answers the gate must never give, so the test
	// reads it instead of following it to a route it would then reach.
	srv.Client().CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	return srv
}

// ask builds a request for path on srv.
func ask(t *testing.T, srv *httptest.Server, method, path string, body io.Reader) *http.Request {
	t.Helper()

	req, err := http.NewRequest(method, srv.URL+path, body)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	return req
}

// send issues req and returns the status and the whole answer. The caller
// builds the request, so one table can vary the method, the headers and the
// body of what reaches the gate.
func send(t *testing.T, srv *httptest.Server, req *http.Request) (int, []byte) {
	t.Helper()

	status, answer, _ := exchange(t, srv, req)
	return status, answer
}

// exchange is send plus the answer's headers, for the one header the gate
// itself puts on the wire.
func exchange(t *testing.T, srv *httptest.Server, req *http.Request) (int, []byte, http.Header) {
	t.Helper()

	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()

	answer, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read answer: %v", err)
	}
	return resp.StatusCode, answer, resp.Header
}

// assertServed checks the answer is the payload the named handler writes.
func assertServed(t *testing.T, status int, answer []byte, wantName string) {
	t.Helper()

	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d (%s)", status, http.StatusOK, answer)
	}
	if got, want := strings.TrimSpace(string(answer)), `{"data":{"name":"`+wantName+`"}}`; got != want {
		t.Errorf("answer = %s, want %s", got, want)
	}
}

// assertRefused checks the answer is the gate's 404 envelope and nothing else.
func assertRefused(t *testing.T, status int, answer []byte) {
	t.Helper()

	assertRefusal(t, status, answer, http.StatusNotFound, httpd.CodeNoSuchRoute, "no route serves this request")
}

// assertNotAllowed checks the answer is the gate's 405 envelope and nothing
// else.
func assertNotAllowed(t *testing.T, status int, answer []byte) {
	t.Helper()

	assertRefusal(t, status, answer, http.StatusMethodNotAllowed, httpd.CodeMethodNotAllowed, "this method is not allowed on this path")
}

// assertAllow checks the answer names exactly the methods wanted, in one
// header value.
func assertAllow(t *testing.T, header http.Header, want string) {
	t.Helper()

	if got := header.Values("Allow"); len(got) != 1 || got[0] != want {
		t.Errorf("Allow = %q, want [%q]", got, want)
	}
}

func TestRouterServesTheRoutesTheServiceNamed(t *testing.T) {
	t.Parallel()

	list := &marker{name: "list"}
	create := &marker{name: "create"}
	srv := gate(t,
		httpd.Route{Method: http.MethodGet, Pattern: "/things", Handler: list},
		httpd.Route{Method: http.MethodPost, Pattern: "/things", Handler: create},
		httpd.Route{Method: http.MethodGet, Pattern: "/things/{id}", Handler: http.HandlerFunc(
			func(w http.ResponseWriter, r *http.Request) {
				httpd.WriteJSON(w, http.StatusOK, payload{Name: r.PathValue("id")})
			})},
	)

	t.Run("the method and the path the service named reach that route", func(t *testing.T) {
		status, answer := send(t, srv, ask(t, srv, http.MethodGet, "/things", nil))

		assertServed(t, status, answer, "list")
	})

	t.Run("a second method on the same path reaches its own route", func(t *testing.T) {
		status, answer := send(t, srv, ask(t, srv, http.MethodPost, "/things", strings.NewReader(`{}`)))

		assertServed(t, status, answer, "create")
	})

	t.Run("a wildcard route reaches its handler with the matched value", func(t *testing.T) {
		status, answer := send(t, srv, ask(t, srv, http.MethodGet, "/things/42", nil))

		assertServed(t, status, answer, "42")
	})
}

func TestRouterRefusesAPathNobodyNamed(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		path string
	}{
		{name: "a path of its own", path: "/secret-path"},
		{name: "a prefix of a named path", path: "/thing"},
		{name: "a segment under a named path", path: "/things/42/owner"},
		{name: "a named path reached through a repeated slash", path: "//things"},
		{name: "a named path reached through a parent segment", path: "/things/42/../../things"},
		{name: "the root", path: "/"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			list := &marker{name: "list"}
			srv := gate(t, httpd.Route{Method: http.MethodGet, Pattern: "/things", Handler: list})

			status, answer := send(t, srv, ask(t, srv, http.MethodGet, tc.path, nil))

			assertRefused(t, status, answer)
			assertNoEcho(t, answer, "things")
			if list.ran.Load() {
				t.Error("the named route ran on a path nobody named")
			}
		})
	}
}

func TestRouterRefusesAPathDifferingOnlyByATrailingSlash(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		pattern string
		path    string
	}{
		{name: "the slash the service did not name", pattern: "/things", path: "/things/"},
		{name: "the slash the service named and the request dropped", pattern: "/things/", path: "/things"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			list := &marker{name: "list"}
			srv := gate(t, httpd.Route{Method: http.MethodGet, Pattern: tc.pattern, Handler: list})

			status, answer := send(t, srv, ask(t, srv, http.MethodGet, tc.path, nil))

			assertRefused(t, status, answer)
			if list.ran.Load() {
				t.Error("the named route ran on a path nobody named")
			}
		})
	}
}

func TestRouterRefusesTheSameWhateverTheRequestCarried(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		method  string
		body    string
		headers map[string]string
	}{
		{name: "a GET carrying nothing", method: http.MethodGet},
		{name: "a POST carrying a JSON body", method: http.MethodPost, body: `{"name":"marker"}`, headers: map[string]string{"Content-Type": "application/json"}},
		{name: "a PUT carrying credentials", method: http.MethodPut, body: "marker", headers: map[string]string{"Authorization": "Bearer marker", "X-Forwarded-For": "10.0.0.1"}},
		{name: "a PATCH carrying a body and no type", method: http.MethodPatch, body: "marker"},
		{name: "a DELETE", method: http.MethodDelete},
		{name: "an OPTIONS preflight", method: http.MethodOptions, headers: map[string]string{"Origin": "https://example.test", "Access-Control-Request-Method": "GET"}},
		{name: "a method nobody ever named", method: "FROB", body: "marker"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			list := &marker{name: "list"}
			srv := gate(t, httpd.Route{Method: http.MethodGet, Pattern: "/things", Handler: list})

			req := ask(t, srv, tc.method, "/secret-path", strings.NewReader(tc.body))
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			status, answer := send(t, srv, req)

			assertRefused(t, status, answer)
			assertNoEcho(t, answer, "marker")
			assertNoEcho(t, answer, "secret-path")
			if list.ran.Load() {
				t.Error("the named route ran on a request nobody named")
			}
		})
	}
}

// open is one route of a case's table, named by its method and its pattern.
// The case gives them all one handler: what is under test is an answer that
// runs none of them.
type open struct {
	method  string
	pattern string
}

func TestRouterRefusesAMethodNobodyNamedForThePath(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		open      []open
		method    string
		path      string
		wantAllow string
	}{
		{
			name:      "a method no route names for a path two of them name",
			open:      []open{{http.MethodGet, "/things"}, {http.MethodPost, "/things"}},
			method:    http.MethodDelete,
			path:      "/things",
			wantAllow: "GET, POST",
		},
		{
			name:      "a method named for another path only",
			open:      []open{{http.MethodGet, "/things"}, {http.MethodPost, "/things"}, {http.MethodGet, "/things/{id}"}},
			method:    http.MethodPost,
			path:      "/things/42",
			wantAllow: "GET",
		},
		{
			name:      "a method nobody ever named",
			open:      []open{{http.MethodGet, "/things"}},
			method:    "FROB",
			path:      "/things",
			wantAllow: "GET",
		},
		{
			name:      "a path a wildcard and an exact route both name",
			open:      []open{{http.MethodGet, "/things/{id}"}, {http.MethodDelete, "/things/special"}},
			method:    http.MethodPut,
			path:      "/things/special",
			wantAllow: "DELETE, GET",
		},
		{
			name:      "an OPTIONS the service did not name",
			open:      []open{{http.MethodGet, "/things"}},
			method:    http.MethodOptions,
			path:      "/things",
			wantAllow: "GET",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			named := &marker{name: "named"}
			routes := make([]httpd.Route, len(tc.open))
			for i, o := range tc.open {
				routes[i] = httpd.Route{Method: o.method, Pattern: o.pattern, Handler: named}
			}
			srv := gate(t, routes...)

			status, answer, header := exchange(t, srv, ask(t, srv, tc.method, tc.path, nil))

			assertNotAllowed(t, status, answer)
			assertAllow(t, header, tc.wantAllow)
			assertNoEcho(t, answer, "things")
			if named.ran.Load() {
				t.Error("a named route ran on a method nobody named for its path")
			}
		})
	}
}

// TestRouterServesTheMethodsItNamesOnAPathItRefuses puts the two answers of one
// named path side by side: the methods the table names reach their own
// handlers, and the one it does not is refused without either running.
func TestRouterServesTheMethodsItNamesOnAPathItRefuses(t *testing.T) {
	t.Parallel()

	list := &marker{name: "list"}
	create := &marker{name: "create"}
	srv := gate(t,
		httpd.Route{Method: http.MethodGet, Pattern: "/things", Handler: list},
		httpd.Route{Method: http.MethodPost, Pattern: "/things", Handler: create},
	)

	status, answer, header := exchange(t, srv, ask(t, srv, http.MethodDelete, "/things", nil))
	assertNotAllowed(t, status, answer)
	assertAllow(t, header, "GET, POST")
	if list.ran.Load() || create.ran.Load() {
		t.Fatal("a named route ran on a method nobody named for its path")
	}

	status, answer = send(t, srv, ask(t, srv, http.MethodGet, "/things", nil))
	assertServed(t, status, answer, "list")

	status, answer = send(t, srv, ask(t, srv, http.MethodPost, "/things", strings.NewReader(`{}`)))
	assertServed(t, status, answer, "create")
}

func TestRouterNamesNoMethodRefusingAPathNobodyNamed(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		path string
	}{
		{name: "a path of its own", path: "/secret-path"},
		{name: "a path differing by a trailing slash", path: "/things/"},
		{name: "a segment under a named path", path: "/things/42"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			srv := gate(t, httpd.Route{Method: http.MethodGet, Pattern: "/things", Handler: &marker{name: "list"}})

			status, answer, header := exchange(t, srv, ask(t, srv, http.MethodDelete, tc.path, nil))

			assertRefused(t, status, answer)
			if got := header.Values("Allow"); len(got) != 0 {
				t.Errorf("Allow = %q, want no Allow header on a path nobody named", got)
			}
		})
	}
}

func TestNewRouterRefusesATableThatWouldOpenMore(t *testing.T) {
	t.Parallel()

	served := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})

	cases := []struct {
		name  string
		route httpd.Route
	}{
		{name: "no method, which opens every method on the pattern", route: httpd.Route{Pattern: "/things", Handler: served}},
		{name: "a method of blanks, which ServeMux reads as no method", route: httpd.Route{Method: " ", Pattern: "/things", Handler: served}},
		{name: "a method carrying a second field", route: httpd.Route{Method: "GET /admin", Pattern: "/things", Handler: served}},
		{name: "a pattern ServeMux would read as a host", route: httpd.Route{Method: http.MethodGet, Pattern: "example.test/things", Handler: served}},
		{name: "no handler", route: httpd.Route{Method: http.MethodGet, Pattern: "/things"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			h, err := httpd.NewRouter(tc.route)

			if err == nil {
				t.Fatalf("NewRouter = %v, nil, want an error", h)
			}
			if h != nil {
				t.Errorf("NewRouter = %v, want a nil handler beside the error", h)
			}
		})
	}
}
