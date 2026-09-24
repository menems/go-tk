package token_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"testing"

	"github.com/menems/go-tk/crypto/token"
)

// redacted is what every surface below must print, exactly. An output equal
// to a constant carries no part of the text nor of the stored value, whatever
// the carrier holds, so the check stays deterministic over an issued token.
const redacted = "[REDACTED]"

// withoutTime drops the record's time, so a logged line is a constant too.
func withoutTime(groups []string, a slog.Attr) slog.Attr {
	if a.Key == slog.TimeKey && len(groups) == 0 {
		return slog.Attr{}
	}
	return a
}

func logged(t *testing.T, handler func(*bytes.Buffer) slog.Handler, tk token.Token) string {
	t.Helper()

	var buf bytes.Buffer
	slog.New(handler(&buf)).Info("issued", slog.Any("token", tk))
	return buf.String()
}

// carriers are the three a surface is checked on: issued, parsed, and the
// zero value, which redacts like any other.
func carriers(t *testing.T) map[string]token.Token {
	t.Helper()

	return map[string]token.Token{
		"issued":     token.Issue(),
		"parsed":     parsed(t, knownText),
		"zero value": {},
	}
}

func TestACarrierRedactsOnEverySurface(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		render func(*testing.T, token.Token) string
		want   string
	}{
		{"verb v", func(_ *testing.T, tk token.Token) string { return fmt.Sprintf("%v", tk) }, redacted},
		{"verb s", func(_ *testing.T, tk token.Token) string { return fmt.Sprintf("%s", tk) }, redacted},
		{"verb q", func(_ *testing.T, tk token.Token) string { return fmt.Sprintf("%q", tk) }, `"[REDACTED]"`},
		{"verb plus v", func(_ *testing.T, tk token.Token) string { return fmt.Sprintf("%+v", tk) }, redacted},
		{"verb sharp v", func(_ *testing.T, tk token.Token) string { return fmt.Sprintf("%#v", tk) }, redacted},
		{"string method", func(_ *testing.T, tk token.Token) string { return tk.String() }, redacted},
		{"json encoder", func(t *testing.T, tk token.Token) string {
			t.Helper()

			b, err := json.Marshal(tk)
			if err != nil {
				t.Fatalf("json.Marshal = %v, want a redacted member", err)
			}
			return string(b)
		}, `"[REDACTED]"`},
		{"text marshaller", func(t *testing.T, tk token.Token) string {
			t.Helper()

			b, err := tk.MarshalText()
			if err != nil {
				t.Fatalf("MarshalText = %v, want the redaction", err)
			}
			return string(b)
		}, redacted},
		{"slog json handler", func(t *testing.T, tk token.Token) string {
			t.Helper()

			return logged(t, func(w *bytes.Buffer) slog.Handler {
				return slog.NewJSONHandler(w, &slog.HandlerOptions{ReplaceAttr: withoutTime})
			}, tk)
		}, `{"level":"INFO","msg":"issued","token":"[REDACTED]"}` + "\n"},
		{"slog text handler", func(t *testing.T, tk token.Token) string {
			t.Helper()

			return logged(t, func(w *bytes.Buffer) slog.Handler {
				return slog.NewTextHandler(w, &slog.HandlerOptions{ReplaceAttr: withoutTime})
			}, tk)
		}, "level=INFO msg=issued token=[REDACTED]\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			for name, tk := range carriers(t) {
				if got := tt.render(t, tk); got != tt.want {
					t.Errorf("%s carrier: output = %s, want exactly %s", name, got, tt.want)
				}
			}
		})
	}
}

type exportedHolder struct {
	Token token.Token
}

type unexportedHolder struct {
	token token.Token
}

func TestAStructHoldingTheCarrierRedacts(t *testing.T) {
	t.Parallel()

	t.Run("exported field", func(t *testing.T) {
		t.Parallel()

		for _, tt := range []struct{ verb, want string }{
			{"%v", "{[REDACTED]}"},
			{"%+v", "{Token:[REDACTED]}"},
			{"%#v", "token_test.exportedHolder{Token:[REDACTED]}"},
		} {
			for name, tk := range carriers(t) {
				if got := fmt.Sprintf(tt.verb, exportedHolder{Token: tk}); got != tt.want {
					t.Errorf("%s carrier: %s = %s, want exactly %s", name, tt.verb, got, tt.want)
				}
			}
		}
	})

	// An unexported field is out of reach of fmt's method dispatch, so the
	// redaction cannot appear there: fmt reads the carrier's fields instead.
	// What holds is containment: the secret is behind a pointer, which prints
	// as an address or as nil, and the whole output is that and nothing else.
	t.Run("unexported field", func(t *testing.T) {
		t.Parallel()

		for _, tt := range []struct {
			verb string
			want *regexp.Regexp
		}{
			{"%v", regexp.MustCompile(`^\{\{(0x[0-9a-f]+|<nil>)\}\}$`)},
			{"%+v", regexp.MustCompile(`^\{token:\{secret:(0x[0-9a-f]+|<nil>)\}\}$`)},
			{"%#v", regexp.MustCompile(`^token_test\.unexportedHolder\{token:token\.Token\{secret:\(\*\[32\]uint8\)\((0x[0-9a-f]+|nil)\)\}\}$`)},
		} {
			for name, tk := range carriers(t) {
				if got := fmt.Sprintf(tt.verb, unexportedHolder{token: tk}); !tt.want.MatchString(got) {
					t.Errorf("%s carrier: %s = %s, want only an address, matching %s", name, tt.verb, got, tt.want)
				}
			}
		}
	})
}

func TestADecodedCarrierYieldsTheStoredValueIssued(t *testing.T) {
	t.Parallel()

	issued := token.Issue()
	text := issued.Reveal()
	want := stored(t, issued)

	t.Run("from a json string", func(t *testing.T) {
		t.Parallel()

		var got struct {
			Token token.Token `json:"token"`
		}
		if err := json.Unmarshal([]byte(`{"token":"`+text+`"}`), &got); err != nil {
			t.Fatalf("Unmarshal = %v, want the token held", err)
		}
		if s := stored(t, got.Token); s != want {
			t.Errorf("the decoded carrier's stored value = %q, want the issued one %q", s, want)
		}
	})

	t.Run("from text", func(t *testing.T) {
		t.Parallel()

		var got token.Token
		if err := got.UnmarshalText([]byte(text)); err != nil {
			t.Fatalf("UnmarshalText = %v, want the token held", err)
		}
		if s := stored(t, got); s != want {
			t.Errorf("the decoded carrier's stored value = %q, want the issued one %q", s, want)
		}
	})
}

// refusedTexts are the texts Parse refuses, as its own test lists them.
var refusedTexts = []struct{ name, text string }{
	{"one character short", knownText[:42]},
	{"one character long", knownText + "A"},
	{"a character of the standard alphabet", "/" + knownText[1:]},
	{"a character of no base64 alphabet", knownText[:20] + "." + knownText[21:]},
	{"a padding character", knownText[:42] + "="},
	{"a line break the decoder would skip", knownText[:42] + "\n"},
	{"the same bytes under another text", knownSibling},
	{"the empty string", ""},
}

func assertMalformed(t *testing.T, what string, err error, got token.Token) {
	t.Helper()

	if !errors.Is(err, token.ErrMalformed) {
		t.Fatalf("%s = %v, want ErrMalformed", what, err)
	}
	if err.Error() != token.ErrMalformed.Error() {
		t.Errorf("%s reads %q, want the one fixed %q", what, err, token.ErrMalformed)
	}
	if s, err := got.Stored(); !errors.Is(err, token.ErrEmpty) {
		t.Errorf("the refused carrier yields %q, %v, want ErrEmpty", s, err)
	}
}

func TestADecodingRefusesUnderTheParsersError(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct{ name, value string }{
		{"a json null", `null`},
		{"a json number", `123`},
		{"a json boolean", `true`},
		{"a json object", `{"token":"` + knownText + `"}`},
		{"a json array", `["` + knownText + `"]`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var got token.Token
			assertMalformed(t, "Unmarshal", json.Unmarshal([]byte(tt.value), &got), got)
		})
	}

	for _, tt := range refusedTexts {
		t.Run("a json string holding "+tt.name, func(t *testing.T) {
			t.Parallel()

			value, err := json.Marshal(tt.text)
			if err != nil {
				t.Fatalf("json.Marshal = %v, want the text as a json string", err)
			}
			var got token.Token
			assertMalformed(t, "Unmarshal", json.Unmarshal(value, &got), got)
		})

		t.Run("text holding "+tt.name, func(t *testing.T) {
			t.Parallel()

			var got token.Token
			assertMalformed(t, "UnmarshalText", got.UnmarshalText([]byte(tt.text)), got)
		})
	}
}
