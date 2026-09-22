package password_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/menems/go-tk/crypto/password"
)

// plaintext shares no substring with redacted, so one output cannot satisfy
// both "it carries the marker" and "it carries no part of the secret".
const (
	plaintext = "hunter2-correct-horse"
	redacted  = "[REDACTED]"
)

func newPassword(t *testing.T, s string) password.Password {
	t.Helper()

	p, err := password.New(s)
	if err != nil {
		t.Fatalf("New = %v, want a carrier", err)
	}
	return p
}

func logged(t *testing.T, handler func(*bytes.Buffer) slog.Handler, p password.Password) string {
	t.Helper()

	var buf bytes.Buffer
	slog.New(handler(&buf)).Info("login", slog.Any("password", p))
	return buf.String()
}

func TestCarrierRedactsOnEverySurface(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		render func(*testing.T, password.Password) string
	}{
		{"verb v", func(_ *testing.T, p password.Password) string { return fmt.Sprintf("%v", p) }},
		{"verb s", func(_ *testing.T, p password.Password) string { return fmt.Sprintf("%s", p) }},
		{"verb q", func(_ *testing.T, p password.Password) string { return fmt.Sprintf("%q", p) }},
		{"verb plus v", func(_ *testing.T, p password.Password) string { return fmt.Sprintf("%+v", p) }},
		{"verb sharp v", func(_ *testing.T, p password.Password) string { return fmt.Sprintf("%#v", p) }},
		{"string method", func(_ *testing.T, p password.Password) string { return p.String() }},
		{"json encoder", func(t *testing.T, p password.Password) string {
			t.Helper()

			b, err := json.Marshal(p)
			if err != nil {
				t.Fatalf("json.Marshal = %v, want a redacted member", err)
			}
			return string(b)
		}},
		{"text marshaller", func(t *testing.T, p password.Password) string {
			t.Helper()

			b, err := p.MarshalText()
			if err != nil {
				t.Fatalf("MarshalText = %v, want the redaction", err)
			}
			return string(b)
		}},
		{"slog json handler", func(t *testing.T, p password.Password) string {
			t.Helper()

			return logged(t, func(w *bytes.Buffer) slog.Handler { return slog.NewJSONHandler(w, nil) }, p)
		}},
		{"slog text handler", func(t *testing.T, p password.Password) string {
			t.Helper()

			return logged(t, func(w *bytes.Buffer) slog.Handler { return slog.NewTextHandler(w, nil) }, p)
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := tt.render(t, newPassword(t, plaintext))
			if !strings.Contains(got, redacted) {
				t.Errorf("output = %s, want it to carry %s", got, redacted)
			}
			if strings.Contains(got, plaintext) {
				t.Error("output carries the plaintext")
			}
		})
	}
}

type exportedHolder struct {
	Password password.Password
}

type unexportedHolder struct {
	password password.Password
}

func TestStructHoldingTheCarrierRedacts(t *testing.T) {
	t.Parallel()

	verbs := []string{"%v", "%+v", "%#v"}

	t.Run("exported field", func(t *testing.T) {
		t.Parallel()

		h := exportedHolder{Password: newPassword(t, plaintext)}
		for _, verb := range verbs {
			got := fmt.Sprintf(verb, h)
			if !strings.Contains(got, redacted) {
				t.Errorf("%s = %s, want it to carry %s", verb, got, redacted)
			}
			if strings.Contains(got, plaintext) {
				t.Errorf("%s carries the plaintext", verb)
			}
		}
	})

	// An unexported field is out of reach of fmt's method dispatch, so the
	// marker cannot appear there. What holds is containment itself: the
	// plaintext sits behind a pointer, which prints as an address.
	t.Run("unexported field", func(t *testing.T) {
		t.Parallel()

		h := unexportedHolder{password: newPassword(t, plaintext)}
		for _, verb := range verbs {
			if got := fmt.Sprintf(verb, h); strings.Contains(got, plaintext) {
				t.Errorf("%s carries the plaintext", verb)
			}
		}
	})
}

func TestDecodedCarrierHoldsItsPlaintext(t *testing.T) {
	t.Parallel()

	t.Run("from a json string", func(t *testing.T) {
		t.Parallel()

		var got password.Password
		if err := json.Unmarshal([]byte(`"`+plaintext+`"`), &got); err != nil {
			t.Fatalf("Unmarshal = %v, want the plaintext held", err)
		}
		if !got.Equal(newPassword(t, plaintext)) {
			t.Error("the decoded carrier does not hold the plaintext it was given")
		}
		if got.Equal(newPassword(t, "another-secret")) {
			t.Error("the decoded carrier matches a plaintext it was never given")
		}
	})

	t.Run("from text", func(t *testing.T) {
		t.Parallel()

		var got password.Password
		if err := got.UnmarshalText([]byte(plaintext)); err != nil {
			t.Fatalf("UnmarshalText = %v, want the plaintext held", err)
		}
		if !got.Equal(newPassword(t, plaintext)) {
			t.Error("the decoded carrier does not hold the plaintext it was given")
		}
		if got.Equal(newPassword(t, "another-secret")) {
			t.Error("the decoded carrier matches a plaintext it was never given")
		}
	})
}

func TestEmptyPlaintextIsRefused(t *testing.T) {
	t.Parallel()

	t.Run("at construction", func(t *testing.T) {
		t.Parallel()

		got, err := password.New("")
		if !errors.Is(err, password.ErrEmpty) {
			t.Fatalf("New = %v, want ErrEmpty", err)
		}
		if got.Equal(newPassword(t, plaintext)) {
			t.Error("the refused carrier holds something")
		}
	})

	t.Run("at json decoding", func(t *testing.T) {
		t.Parallel()

		var got password.Password
		if err := json.Unmarshal([]byte(`""`), &got); !errors.Is(err, password.ErrEmpty) {
			t.Fatalf("Unmarshal = %v, want ErrEmpty", err)
		}
	})

	t.Run("at json decoding of null", func(t *testing.T) {
		t.Parallel()

		var got password.Password
		if err := json.Unmarshal([]byte(`null`), &got); !errors.Is(err, password.ErrEmpty) {
			t.Fatalf("Unmarshal = %v, want ErrEmpty", err)
		}
	})

	t.Run("at text decoding", func(t *testing.T) {
		t.Parallel()

		var got password.Password
		if err := got.UnmarshalText(nil); !errors.Is(err, password.ErrEmpty) {
			t.Fatalf("UnmarshalText = %v, want ErrEmpty", err)
		}
	})
}

func TestJSONValueThatIsNotAStringIsRefused(t *testing.T) {
	t.Parallel()

	var got password.Password
	err := json.Unmarshal([]byte(`{"password":"`+plaintext+`"}`), &got)
	if err == nil {
		t.Fatal("Unmarshal = nil, want an error")
	}
	if strings.Contains(err.Error(), plaintext) {
		t.Error("the error carries the plaintext")
	}
	if got.Equal(newPassword(t, plaintext)) {
		t.Error("the refused carrier holds something")
	}
}

func TestZeroValueHoldsNothingAndRedacts(t *testing.T) {
	t.Parallel()

	var zero password.Password

	got := fmt.Sprintf("%v %s %q %+v %#v %s", zero, zero, zero, zero, zero, zero.String())
	if strings.Count(got, redacted) != 6 {
		t.Errorf("output = %s, want the redaction six times", got)
	}

	if zero.Equal(zero) {
		t.Error("a zero carrier matches itself, so something derived from it could verify")
	}
	if pw := newPassword(t, plaintext); zero.Equal(pw) || pw.Equal(zero) {
		t.Error("a zero carrier matches a carrier holding a plaintext")
	}
}
