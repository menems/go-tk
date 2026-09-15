package authctx_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/menems/got-tk/authctx"
)

func TestBearer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		header    string
		wantToken string
		wantOK    bool
	}{
		{name: "no header"},
		{name: "scheme alone", header: "Bearer"},
		{name: "scheme and nothing", header: "Bearer "},
		{name: "another scheme", header: "Basic dXNlcjpwYXNz"},
		{name: "token without a scheme", header: "abc.def.ghi"},
		{name: "token carrying a space", header: "Bearer abc def"},
		{name: "token carrying a tab", header: "Bearer abc\tdef"},
		{
			name:      "canonical",
			header:    "Bearer abc.def.ghi",
			wantToken: "abc.def.ghi",
			wantOK:    true,
		},
		{
			name:      "lowercase scheme",
			header:    "bearer abc.def.ghi",
			wantToken: "abc.def.ghi",
			wantOK:    true,
		},
		{
			name:      "uppercase scheme",
			header:    "BEARER abc.def.ghi",
			wantToken: "abc.def.ghi",
			wantOK:    true,
		},
		{
			name:      "several spaces after the scheme",
			header:    "Bearer    abc.def.ghi",
			wantToken: "abc.def.ghi",
			wantOK:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := http.Header{}
			if tt.header != "" {
				h.Set("Authorization", tt.header)
			}

			token, ok := authctx.Bearer(h)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if token != tt.wantToken {
				t.Errorf("token = %q, want %q", token, tt.wantToken)
			}
		})
	}
}

type userID string

func TestKeyRoundTrip(t *testing.T) {
	t.Parallel()

	key := authctx.NewKey[userID]("user_id")
	ctx := key.With(context.Background(), "u-1")

	got, ok := key.From(ctx)
	if !ok {
		t.Fatal("From = not found, want the value With put in")
	}
	if got != "u-1" {
		t.Errorf("From = %q, want %q", got, userID("u-1"))
	}
}

func TestKeyMissingValue(t *testing.T) {
	t.Parallel()

	key := authctx.NewKey[userID]("user_id")

	got, ok := key.From(context.Background())
	if ok {
		t.Fatalf("From = %q, true; want the zero value and false", got)
	}
	if got != "" {
		t.Errorf("From = %q, want the zero value", got)
	}
}

// TestKeysOfTheSameTypeDoNotCollide is why a key is a value and not a type:
// a tenant id and a user id that are both strings stay apart.
func TestKeysOfTheSameTypeDoNotCollide(t *testing.T) {
	t.Parallel()

	user := authctx.NewKey[userID]("user_id")
	tenant := authctx.NewKey[userID]("tenant_id")

	ctx := tenant.With(user.With(context.Background(), "u-1"), "t-1")

	if got, _ := user.From(ctx); got != "u-1" {
		t.Errorf("user = %q, want %q", got, userID("u-1"))
	}
	if got, _ := tenant.From(ctx); got != "t-1" {
		t.Errorf("tenant = %q, want %q", got, userID("t-1"))
	}
}

func TestKeyString(t *testing.T) {
	t.Parallel()

	if got := authctx.NewKey[userID]("user_id").String(); got != "authctx.user_id" {
		t.Errorf("String = %q, want %q", got, "authctx.user_id")
	}
}
