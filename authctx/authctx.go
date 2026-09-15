// Package authctx reads a bearer credential off a request and carries what
// authenticating it produced through the context.
//
// It does not verify anything. The token's signature, its claims and its
// expiry are the service's business, because that is where the algorithm and
// the key material differ; this package covers the two ends nobody should
// rewrite: pulling the credential out of a header, and handing the principal
// to the handler.
//
// Both ends are transport-agnostic. Bearer takes an http.Header, which is what
// a net/http middleware and a connect.Request both hold.
package authctx

import (
	"context"
	"net/http"
	"strings"
)

// Bearer returns the token of an Authorization: Bearer header, and false when
// there is none to read.
//
// The scheme is matched case-insensitively and may be followed by more than
// one space, as RFC 6750 allows. The token itself carries no whitespace, so a
// value that does is rejected rather than passed on to a parser.
func Bearer(h http.Header) (string, bool) {
	scheme, token, found := strings.Cut(h.Get("Authorization"), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}

	token = strings.TrimLeft(token, " ")
	if token == "" || strings.ContainsAny(token, " \t") {
		return "", false
	}
	return token, true
}

// Key carries values of type T through a context under an identity of its own.
type Key[T any] struct {
	name string
}

// NewKey declares a context key, named for what it carries. Every call returns
// a distinct key, so a user id and a tenant id that are both uuid.UUID do not
// overwrite each other, which a key typed by T alone would let them do.
//
// Declare it once, at package level, next to the middleware that fills it.
func NewKey[T any](name string) *Key[T] {
	return &Key[T]{name: name}
}

// With returns a copy of ctx carrying v.
func (k *Key[T]) With(ctx context.Context, v T) context.Context {
	return context.WithValue(ctx, k, v)
}

// From returns the value a middleware put in ctx, and false when the request
// went through no such middleware. A caller that treats the false as "not
// authenticated" is the authorization check failing closed.
func (k *Key[T]) From(ctx context.Context) (T, bool) {
	v, ok := ctx.Value(k).(T)
	return v, ok
}

// String names the key in a context dump, where a bare pointer says nothing.
func (k *Key[T]) String() string { return "authctx." + k.name }
