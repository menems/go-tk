package httpd

import (
	"context"
	"net/http"

	"github.com/menems/go-tk/authctx"
)

// CodeForbidden is the code a route demanding a right refuses with: the caller
// of this request holds that right nowhere.
const CodeForbidden ErrorCode = "forbidden"

// Fixed, like the rest of the envelope's messages, and it names neither the
// right demanded nor any right the caller does hold: the first tells a caller
// which right to go and get, the second answers a question it never asked.
const messageForbidden = "this credential does not allow this request"

// Holds answers whether principal holds right; false denies.
//
// There is no third answer, so a check that could not reach a verdict answers
// false and authorization fails closed: a right store that is down denies
// rather than serves. The cost is that an outage reads from outside as a
// refusal, which is the opposite trade from Resolver, where a credential
// nobody could judge is told apart from one that was refused. A right is asked
// about per route and per request, and a 500 there would turn every such
// outage into a status the caller retries against a service that cannot answer
// yet.
//
// ctx is the request's, so a lookup it makes is cancelled with the request
// that asked for it.
type Holds[T, R any] func(ctx context.Context, principal T, right R) bool

// RequireRight covers the handler of a route a service closes on a right: the
// principal riding in the request context under key reaches holds with right,
// and the handler runs only when holds answers true.
//
//	var userID = authctx.NewKey[uuid.UUID]("user_id")
//
//	auth := httpd.RequireBearer(userID, verify)                 // verify is yours
//	listing := httpd.RequireRight(userID, scopeList, allowed)   // allowed is yours
//	h, err := httpd.NewRouter(
//	    httpd.Route{Method: http.MethodGet, Pattern: "/things", Handler: auth(listing(list))},
//	    httpd.Route{Method: http.MethodGet, Pattern: "/me", Handler: auth(me)},
//	)
//
// A request whose caller holds that right nowhere is answered 403 under
// CodeForbidden, and so is one reaching this wrap with no principal in its
// context, which is a route wired without RequireBearer above it. The wrapped
// handler runs in neither case. The answer is the same body under the same
// code whatever right was demanded and whatever the caller does hold, and it
// carries no WWW-Authenticate header: a challenge invites a retry, and no
// credential this caller can present makes this request go through.
//
// This package holds no right table and grants nothing. The verdict is the
// service's own, which is why nothing here reads right: a holds that answers
// wrongly authorizes wrongly.
//
// The wrap sits on the handler and inside the bearer wrap, so the order a
// request meets is the route table, then the bearer, then the right. Nothing
// is asked about rights for a request no route matched or no credential
// authenticated, and coverage is exactly the set of handlers wrapped: a route
// wrapped for no right demands none. A route named for GET is reached by HEAD
// too, and the wrap sitting on the handler, that HEAD is checked like the GET.
func RequireRight[T, R any](key *authctx.Key[T], right R, holds Holds[T, R]) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal, ok := key.From(r.Context())
			if !ok {
				// Nothing to ask holds about. Serving here would make a route
				// left outside RequireBearer an open door, and answering 401
				// would invite a retry the wiring, not the caller, has to fix.
				WriteError(w, http.StatusForbidden, CodeForbidden, messageForbidden)
				return
			}

			if !holds(r.Context(), principal, right) {
				WriteError(w, http.StatusForbidden, CodeForbidden, messageForbidden)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
