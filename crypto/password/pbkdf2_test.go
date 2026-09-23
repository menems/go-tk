package password_test

import (
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/menems/go-tk/crypto/password"
)

// testCost keeps the hashing of a test above the floor and far under what a
// service runs at: the cost a stored value was made under is read back from
// that value, so nothing here depends on the number being a realistic one.
const testCost = 2

func newHasher(t *testing.T, opts ...password.Option) password.Hasher {
	t.Helper()

	h, err := password.NewPBKDF2(opts...)
	if err != nil {
		t.Fatalf("NewPBKDF2 = %v, want a hasher", err)
	}
	return h
}

func hashed(t *testing.T, h password.Hasher, s string) string {
	t.Helper()

	stored, err := newPassword(t, s).Hash(h)
	if err != nil {
		t.Fatalf("Hash = %v, want a stored value", err)
	}
	return stored
}

// fieldsOf splits a stored value on the separator the documented shape uses,
// so a case can replace one field and leave the others as this package wrote
// them.
func fieldsOf(t *testing.T, stored string) []string {
	t.Helper()

	f := strings.Split(stored, "$")
	if len(f) != 5 {
		t.Fatalf("stored value %q has %d fields, want the five of the documented shape", stored, len(f))
	}
	return f
}

func replacing(f []string, i int, with string) string {
	g := append([]string(nil), f...)
	g[i] = with
	return strings.Join(g, "$")
}

func TestStoredValueVerifiesAgainstItsOwnPasswordAndNoOther(t *testing.T) {
	t.Parallel()

	h := newHasher(t, password.WithCost(testCost))
	stored := hashed(t, h, plaintext)

	if err := newPassword(t, plaintext).Verify(h, stored); err != nil {
		t.Errorf("Verify against its own password = %v, want it to go through", err)
	}

	for _, other := range []string{"another-secret", plaintext + "!", plaintext[:len(plaintext)-1], strings.ToUpper(plaintext)} {
		if err := newPassword(t, other).Verify(h, stored); !errors.Is(err, password.ErrMismatch) {
			t.Errorf("Verify against %q = %v, want ErrMismatch", other, err)
		}
	}
}

func TestHashingOnePasswordTwiceGivesTwoStoredValues(t *testing.T) {
	t.Parallel()

	h := newHasher(t, password.WithCost(testCost))
	first, second := hashed(t, h, plaintext), hashed(t, h, plaintext)

	if first == second {
		t.Fatal("hashing the same password twice gave one stored value, so nothing salts it")
	}
	for _, stored := range []string{first, second} {
		if err := newPassword(t, plaintext).Verify(h, stored); err != nil {
			t.Errorf("Verify = %v, want both stored values to verify that password", err)
		}
	}
}

func TestAStoredValueItCannotReadIsRefusedAndMatchesNothing(t *testing.T) {
	t.Parallel()

	h := newHasher(t, password.WithCost(testCost))
	valid := hashed(t, h, plaintext)
	f := fieldsOf(t, valid)

	tests := []struct {
		name   string
		stored string
	}{
		{"empty", ""},
		{"truncated", valid[:len(valid)-8]},
		{"nothing but separators", "$$$$"},
		{"written by another implementation", "$argon2id$v=19$m=65536,t=3,p=4$c29tZXNhbHRzYWx0$c29tZWtleXNvbWVrZXlzb21la2V5c29tZWs"},
		{"missing its leading separator", strings.TrimPrefix(valid, "$")},
		{"carrying one field too many", valid + "$extra"},
		{"parameters under another name", replacing(f, 2, "iterations=2")},
		{"a cost that is not a number", replacing(f, 2, "i=two")},
		{"a cost below one", replacing(f, 2, "i=0")},
		{"no parameters at all", replacing(f, 2, "")},
		{"a salt that is not base64", replacing(f, 3, "not base64!")},
		{"a salt of another length", replacing(f, 3, base64.RawStdEncoding.EncodeToString(make([]byte, 4)))},
		{"no salt at all", replacing(f, 3, "")},
		{"a key that is not base64", replacing(f, 4, "not base64!")},
		{"a key of another length", replacing(f, 4, base64.RawStdEncoding.EncodeToString(make([]byte, 8)))},
		// The one that would otherwise match every password: an empty key and
		// an empty derivation compare equal to a constant-time comparison.
		{"no key at all", replacing(f, 4, "")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// The password is the one the untouched value was made from, so a
			// case that verifies is one this hasher read a value it did not
			// write.
			if err := newPassword(t, plaintext).Verify(h, tt.stored); !errors.Is(err, password.ErrUnreadable) {
				t.Errorf("Verify = %v, want ErrUnreadable", err)
			}
		})
	}
}

func TestAStoredValueVerifiesThroughAHasherBuiltWithAnotherCost(t *testing.T) {
	t.Parallel()

	low := newHasher(t, password.WithCost(testCost))
	high := newHasher(t, password.WithCost(testCost+5))

	if err := newPassword(t, plaintext).Verify(high, hashed(t, low, plaintext)); err != nil {
		t.Errorf("Verify of a value made under the lower cost = %v, want it to go through", err)
	}
	if err := newPassword(t, plaintext).Verify(low, hashed(t, high, plaintext)); err != nil {
		t.Errorf("Verify of a value made under the higher cost = %v, want it to go through", err)
	}
}

func TestAHasherBuiltWithNoCostUsesTheOneThisPackageNames(t *testing.T) {
	t.Parallel()

	unnamed := fieldsOf(t, hashed(t, newHasher(t), plaintext))
	named := fieldsOf(t, hashed(t, newHasher(t, password.WithCost(password.DefaultCost)), plaintext))
	other := fieldsOf(t, hashed(t, newHasher(t, password.WithCost(testCost)), plaintext))

	if unnamed[2] != named[2] {
		t.Errorf("a hasher built with no cost wrote %q, want the %q of DefaultCost", unnamed[2], named[2])
	}
	if unnamed[2] == other[2] {
		t.Errorf("a hasher built at %d wrote the parameters of one built with no cost", testCost)
	}
}

func TestACostBelowOneIsRefusedAtWiring(t *testing.T) {
	t.Parallel()

	for _, cost := range []int{0, -1, -600000} {
		h, err := password.NewPBKDF2(password.WithCost(cost))
		if err == nil {
			t.Errorf("NewPBKDF2 at cost %d = nil, want an error", cost)
			continue
		}
		if h != nil {
			t.Errorf("NewPBKDF2 at cost %d handed back a hasher, want none", cost)
		}
		if !strings.Contains(err.Error(), strconv.Itoa(cost)) {
			t.Errorf("NewPBKDF2 at cost %d = %v, want the error to name the value", cost, err)
		}
	}
}

func TestARefusedVerificationSaysTheSameThingWhateverThePassword(t *testing.T) {
	t.Parallel()

	h := newHasher(t, password.WithCost(testCost))
	stored := hashed(t, h, plaintext)

	candidates := []string{
		"zzz-wrong-horse",
		"qqq-another-secret-entirely",
		plaintext + "-suffix",
		strings.Repeat("wxy", 200),
	}

	var texts []string
	for _, candidate := range candidates {
		err := newPassword(t, candidate).Verify(h, stored)
		if !errors.Is(err, password.ErrMismatch) {
			t.Fatalf("Verify of %q = %v, want ErrMismatch", candidate, err)
		}
		if strings.Contains(err.Error(), candidate) {
			t.Errorf("the refusal of %q carries the password", candidate)
		}
		for _, field := range fieldsOf(t, stored)[1:] {
			if strings.Contains(err.Error(), field) {
				t.Errorf("the refusal carries %q, a part of the stored value", field)
			}
		}
		texts = append(texts, err.Error())
	}

	for _, text := range texts {
		if text != texts[0] {
			t.Errorf("a refusal said %q and another said %q, want one text whatever the password", text, texts[0])
		}
	}
}
