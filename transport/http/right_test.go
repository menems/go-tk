package httpd_test

import (
	"context"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/menems/go-tk/transport/http"
)

// right stands for whatever a service calls the thing a caller must hold: a
// scope of a token, a permission of a role, a row of its own table. It is the
// service's type, and this package never reads it.
type right string

// The rights the table below demands, and a third the caller holds while being
// refused. All three carry a marker, so an answer repeating any of them is
// caught by the no-echo assertions.
const (
	rightList  right = "marker-things-list"
	rightPurge right = "marker-things-purge"
	rightHeld  right = "marker-things-else"
)

// The refusal a route demanding a right gives, written here as the literal a
// client reads.
const messageForbidden = "this credential does not allow this request"

// holder stands for the service's own right table: it answers whether the
// principal it is handed holds the right the route demanded. It records how
// many times it was asked anything, so a request that must reach no check can
// be checked to have reached none.
//
// held is written at construction and only read afterwards, so the parallel
// tests below share nothing through it.
type holder struct {
	held  map[right]bool
	calls atomic.Int64
}

func (h *holder) holds(_ context.Context, _ principal, r right) bool {
	h.calls.Add(1)
	return h.held[r]
}

// demanding is one table of three covered routes: two demanding two different
// rights, one demanding none, each with its own handler, plus the holder the
// two demands were given.
type demanding struct {
	srv    *httptest.Server
	holder *holder
	list   *reader
	purge  *reader
	open   *reader
}

// demand builds that table, granting the caller exactly the rights named. This
// is the seam: a consumer meets the wrap through the handler NewRouter
// returns, one served request at a time.
//
// The order the wraps are written in is the order a request meets them: the
// table, then the bearer, then the right.
func demand(t *testing.T, held ...right) demanding {
	t.Helper()

	h := &holder{held: make(map[right]bool, len(held))}
	for _, r := range held {
		h.held[r] = true
	}
	auth := httpd.RequireBearer(principalKey, (&resolver{}).resolve)
	list, purge, open := &reader{}, &reader{}, &reader{}
	srv := gate(t,
		httpd.Route{
			Method:  http.MethodGet,
			Pattern: "/things",
			Handler: auth(httpd.RequireRight(principalKey, rightList, h.holds)(list)),
		},
		httpd.Route{
			Method:  http.MethodDelete,
			Pattern: "/things",
			Handler: auth(httpd.RequireRight(principalKey, rightPurge, h.holds)(purge)),
		},
		httpd.Route{Method: http.MethodGet, Pattern: "/status", Handler: auth(open)},
	)

	return demanding{srv: srv, holder: h, list: list, purge: purge, open: open}
}

// assertForbidden checks the answer is the wrap's 403 envelope, names none of
// the rights in play and nothing of the principal, and carries no challenge: a
// scheme to retry under would invite a retry there is no credential for.
func assertForbidden(t *testing.T, status int, answer []byte, header http.Header) {
	t.Helper()

	assertRefusal(t, status, answer, http.StatusForbidden, httpd.CodeForbidden, messageForbidden)
	for _, r := range []right{rightList, rightPurge, rightHeld} {
		assertNoEcho(t, answer, string(r))
	}
	assertNoEcho(t, answer, "owner")
	if got := header.Values("WWW-Authenticate"); len(got) != 0 {
		t.Errorf("WWW-Authenticate = %q, want none on a refusal no credential answers", got)
	}
}

// withoutDate drops the one header two answers written at two moments cannot
// share, so what is left is what the wrap itself put on the wire.
func withoutDate(h http.Header) http.Header {
	out := h.Clone()
	out.Del("Date")
	return out
}

func TestRightReachesTheHandlerOfARouteWhoseRightItsCallerHolds(t *testing.T) {
	t.Parallel()

	d := demand(t, rightList)

	req := withBearer(ask(t, d.srv, http.MethodGet, "/things", nil), "Bearer "+goodToken)
	status, answer := send(t, d.srv, req)

	assertServed(t, status, answer, "owner")
	if got := d.holder.calls.Load(); got != 1 {
		t.Errorf("holder asked %d times, want 1", got)
	}
}

func TestRightRefusesACallerHoldingItNowhere(t *testing.T) {
	t.Parallel()

	d := demand(t, rightHeld)

	req := withBearer(ask(t, d.srv, http.MethodGet, "/things", nil), "Bearer "+goodToken)
	status, answer, header := exchange(t, d.srv, req)

	assertForbidden(t, status, answer, header)
	if d.list.ran.Load() {
		t.Error("the covered handler ran on a request the wrap refused")
	}
	if got := d.holder.calls.Load(); got != 1 {
		t.Errorf("holder asked %d times, want 1", got)
	}
}

// TestRightRefusesTheSameWhateverRightItDemanded puts the two refusals side by
// side: a caller learns from them that it may not make the request, and not
// which right would have let it, so probing a service's table one route at a
// time returns the same answer every time.
func TestRightRefusesTheSameWhateverRightItDemanded(t *testing.T) {
	t.Parallel()

	d := demand(t, rightHeld)

	listStatus, listAnswer, listHeader := exchange(t, d.srv,
		withBearer(ask(t, d.srv, http.MethodGet, "/things", nil), "Bearer "+goodToken))
	assertForbidden(t, listStatus, listAnswer, listHeader)

	purgeStatus, purgeAnswer, purgeHeader := exchange(t, d.srv,
		withBearer(ask(t, d.srv, http.MethodDelete, "/things", nil), "Bearer "+goodToken))
	assertForbidden(t, purgeStatus, purgeAnswer, purgeHeader)

	if string(listAnswer) != string(purgeAnswer) {
		t.Errorf("answers differ: %s and %s", listAnswer, purgeAnswer)
	}
	if !maps.EqualFunc(withoutDate(listHeader), withoutDate(purgeHeader), slices.Equal) {
		t.Errorf("headers differ: %v and %v", withoutDate(listHeader), withoutDate(purgeHeader))
	}
	if d.list.ran.Load() || d.purge.ran.Load() {
		t.Error("a covered handler ran on a request the wrap refused")
	}
}

// TestRightLeavesARouteDemandingNoneServingItsCaller pins coverage as exactly
// the set of handlers wrapped: the caller refused on the two routes above is
// served by the route beside them, whose handler still reads the principal the
// bearer wrap put in its context.
func TestRightLeavesARouteDemandingNoneServingItsCaller(t *testing.T) {
	t.Parallel()

	d := demand(t, rightHeld)

	req := withBearer(ask(t, d.srv, http.MethodGet, "/status", nil), "Bearer "+goodToken)
	status, answer := send(t, d.srv, req)

	assertServed(t, status, answer, "owner")
	if got := d.holder.calls.Load(); got != 0 {
		t.Errorf("holder asked %d times on a route demanding no right, want 0", got)
	}
}

// TestRightIsAskedNothingBeforeTheTableAndTheBearerAnswered pins the order the
// wrap sits in: what a caller can learn about the table is what the router and
// the bearer wrap left it, and neither of their answers turns into a 403.
func TestRightIsAskedNothingBeforeTheTableAndTheBearerAnswered(t *testing.T) {
	t.Parallel()

	t.Run("a path nobody named is still answered 404", func(t *testing.T) {
		t.Parallel()

		d := demand(t, rightList)

		req := withBearer(ask(t, d.srv, http.MethodGet, "/secret-path", nil), "Bearer "+goodToken)
		status, answer := send(t, d.srv, req)

		assertRefused(t, status, answer)
		if got := d.holder.calls.Load(); got != 0 {
			t.Errorf("holder asked %d times on a path nobody named, want 0", got)
		}
	})

	t.Run("a request carrying no credential is still answered 401", func(t *testing.T) {
		t.Parallel()

		d := demand(t, rightList)

		status, answer, header := exchange(t, d.srv, ask(t, d.srv, http.MethodGet, "/things", nil))

		assertUnauthenticated(t, status, answer, header)
		if d.list.ran.Load() {
			t.Error("the covered handler ran on a request the bearer wrap refused")
		}
		if got := d.holder.calls.Load(); got != 0 {
			t.Errorf("holder asked %d times on an unauthenticated request, want 0", got)
		}
	})
}

// TestRightRefusesAWrapItsServiceLeftWithNoPrincipal pins the wrap failing
// closed: a route wired without RequireBearer above it reaches this wrap with
// no principal to hand the check, and serving it would make a wiring mistake
// an open door.
func TestRightRefusesAWrapItsServiceLeftWithNoPrincipal(t *testing.T) {
	t.Parallel()

	h := &holder{held: map[right]bool{rightList: true}}
	bare := &reader{}
	srv := gate(t, httpd.Route{
		Method:  http.MethodGet,
		Pattern: "/things",
		Handler: httpd.RequireRight(principalKey, rightList, h.holds)(bare),
	})

	req := withBearer(ask(t, srv, http.MethodGet, "/things", nil), "Bearer "+goodToken)
	status, answer, header := exchange(t, srv, req)

	assertForbidden(t, status, answer, header)
	if bare.ran.Load() {
		t.Error("the covered handler ran on a request carrying no principal")
	}
	if got := h.calls.Load(); got != 0 {
		t.Errorf("holder asked %d times with no principal to hand it, want 0", got)
	}
}
