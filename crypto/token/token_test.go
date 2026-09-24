package token_test

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/menems/go-tk/authctx"
	"github.com/menems/go-tk/crypto/token"
)

// knownText encodes the 32 bytes 3, 10, 17, … (i*7+3), and knownStored is the
// hex SHA-256 of those bytes, computed outside Go. knownSibling differs from
// knownText in its last character only, whose two low bits the encoding
// leaves unused: a lenient decoder reads it back into the same 32 bytes.
const (
	knownText    = "AwoRGB8mLTQ7QklQV15lbHN6gYiPlp2kq7K5wMfO1dw"
	knownSibling = "AwoRGB8mLTQ7QklQV15lbHN6gYiPlp2kq7K5wMfO1dx"
	knownStored  = "ab5f8b5cb9435354c7b58603592d5faf081e17ceb05f7a7c67f4b666f12ca457"
)

func stored(t *testing.T, tk token.Token) string {
	t.Helper()

	s, err := tk.Stored()
	if err != nil {
		t.Fatalf("Stored = %v, want a stored value", err)
	}
	return s
}

func parsed(t *testing.T, text string) token.Token {
	t.Helper()

	tk, err := token.Parse(text)
	if err != nil {
		t.Fatalf("Parse = %v, want a token", err)
	}
	return tk
}

func TestAnIssuedTokenArrivesAsABearerAndParsesToItsStoredValue(t *testing.T) {
	t.Parallel()

	issued := token.Issue()
	text := issued.Reveal()
	want := stored(t, issued)

	h := http.Header{}
	h.Set("Authorization", "Bearer "+text)
	arrived, ok := authctx.Bearer(h)
	if !ok || arrived != text {
		t.Fatalf("Bearer = %q, %v, want the issued text %q unchanged", arrived, ok, text)
	}

	back := parsed(t, arrived)
	if got := stored(t, back); got != want {
		t.Errorf("the parsed token's stored value = %q, want the issued one %q", got, want)
	}
	if got := back.Reveal(); got != text {
		t.Errorf("the parsed token reveals %q, want the text it was parsed from %q", got, text)
	}
}

func TestTheStoredValueIsTheDigestOfTheSecret(t *testing.T) {
	t.Parallel()

	if got := stored(t, parsed(t, knownText)); got != knownStored {
		t.Errorf("Stored = %q, want %q", got, knownStored)
	}
}

func TestTenThousandIssuedTokensAreDistinct(t *testing.T) {
	t.Parallel()

	const n = 10_000
	texts := make(map[string]struct{}, n)
	values := make(map[string]struct{}, n)
	for range n {
		tk := token.Issue()
		texts[tk.Reveal()] = struct{}{}
		values[stored(t, tk)] = struct{}{}
	}
	if len(texts) != n {
		t.Errorf("%d tokens issued gave %d distinct texts, want %d", n, len(texts), n)
	}
	if len(values) != n {
		t.Errorf("%d tokens issued gave %d distinct stored values, want %d", n, len(values), n)
	}
}

func TestAStoredValueCarriesNoPartOfItsTextAndDoesNotParse(t *testing.T) {
	t.Parallel()

	// Any six characters of the text in a row would be a part of it.
	const window = 6
	for i := 0; i+window <= len(knownText); i++ {
		if part := knownText[i : i+window]; strings.Contains(knownStored, part) {
			t.Errorf("the stored value %q carries %q, a part of its text", knownStored, part)
		}
	}

	if _, err := token.Parse(stored(t, parsed(t, knownText))); !errors.Is(err, token.ErrMalformed) {
		t.Errorf("Parse of a stored value = %v, want ErrMalformed", err)
	}
}

func TestAnythingNotIssuedIsRefusedUnderOneFixedError(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct{ name, text string }{
		{"one character short", knownText[:42]},
		{"one character long", knownText + "A"},
		{"a character of the standard alphabet", "/" + knownText[1:]},
		{"a character of no base64 alphabet", knownText[:20] + "." + knownText[21:]},
		{"a padding character", knownText[:42] + "="},
		{"a line break the decoder would skip", knownText[:42] + "\n"},
		{"the same bytes under another text", knownSibling},
		{"the empty string", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tk, err := token.Parse(tt.text)
			if !errors.Is(err, token.ErrMalformed) {
				t.Fatalf("Parse = %v, want ErrMalformed", err)
			}
			if err.Error() != token.ErrMalformed.Error() {
				t.Errorf("the refusal reads %q, want the one fixed %q", err, token.ErrMalformed)
			}
			if tt.text != "" && strings.Contains(err.Error(), tt.text) {
				t.Errorf("the refusal %q carries what arrived", err)
			}
			if got := tk.Reveal(); got != "" {
				t.Errorf("the refused parse hands out %q, want no text", got)
			}
		})
	}
}

func TestTheZeroTokenHasNoStoredValueAndNoText(t *testing.T) {
	t.Parallel()

	var tk token.Token

	s, err := tk.Stored()
	if !errors.Is(err, token.ErrEmpty) {
		t.Errorf("Stored = %q, %v, want ErrEmpty", s, err)
	}
	if s != "" {
		t.Errorf("Stored = %q, want no stored value", s)
	}
	if got := tk.Reveal(); got != "" {
		t.Errorf("Reveal = %q, want no text", got)
	}
}
