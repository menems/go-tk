package sortid_test

import (
	"encoding"
	"encoding/json"
	"errors"
	"testing"
	"time"
	"uuid"

	"github.com/menems/go-tk/sortid"
)

// known is the id knownText writes.
func known(t *testing.T) sortid.ID {
	t.Helper()

	id, err := sortid.Parse(knownText)
	if err != nil {
		t.Fatalf("Parse(%q) = %v, want an id", knownText, err)
	}
	return id
}

type order struct {
	ID sortid.ID `json:"id"`
}

type draft struct {
	Name string    `json:"name"`
	ID   sortid.ID `json:"id,omitzero"`
}

func TestAnIDFieldMarshalsAsItsTextAndUnmarshalsBack(t *testing.T) {
	t.Parallel()

	id := minter(t, time.Now).Mint()
	b, err := json.Marshal(order{ID: id})
	if err != nil {
		t.Fatalf("Marshal = %v, want the id's text", err)
	}
	if want := `{"id":"` + id.String() + `"}`; string(b) != want {
		t.Fatalf("Marshal = %s, want %s", b, want)
	}

	var back order
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("Unmarshal(%s) = %v, want the id back", b, err)
	}
	if back.ID != id {
		t.Errorf("Unmarshal(%s) = %s, want %s", b, back.ID, id)
	}
}

func TestAMapKeyedByIDMarshalsItsKeysAsTheirTextAndUnmarshalsBack(t *testing.T) {
	t.Parallel()

	id := known(t)
	b, err := json.Marshal(map[sortid.ID]int{id: 1})
	if err != nil {
		t.Fatalf("Marshal = %v, want the keys' text", err)
	}
	if want := `{"` + knownText + `":1}`; string(b) != want {
		t.Fatalf("Marshal = %s, want %s", b, want)
	}

	var back map[sortid.ID]int
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("Unmarshal(%s) = %v, want the map back", b, err)
	}
	if len(back) != 1 || back[id] != 1 {
		t.Errorf("Unmarshal(%s) = %v, want %s: 1", b, back, id)
	}
}

func TestTheZeroIDFailsToMarshalUnderErrMalformed(t *testing.T) {
	t.Parallel()

	b, err := json.Marshal(order{})
	if !errors.Is(err, sortid.ErrMalformed) {
		t.Fatalf("Marshal of the zero ID = %s, %v, want ErrMalformed", b, err)
	}

	b, err = json.Marshal(map[sortid.ID]int{{}: 1})
	if !errors.Is(err, sortid.ErrMalformed) {
		t.Fatalf("Marshal of a map keyed by the zero ID = %s, %v, want ErrMalformed", b, err)
	}
}

func TestAnIDOfAnotherVersionFailsToMarshalUnderErrMalformed(t *testing.T) {
	t.Parallel()

	v4 := sortid.ID(uuid.MustParse("0192258c-4a7e-4b3c-9d1e-2f3a4b5c6d7e"))
	b, err := v4.MarshalText()
	if !errors.Is(err, sortid.ErrMalformed) {
		t.Fatalf("MarshalText of a version 4 id = %s, %v, want ErrMalformed", b, err)
	}
}

func TestTheZeroIDIsDroppedFromAnOmitzeroField(t *testing.T) {
	t.Parallel()

	if !(sortid.ID{}).IsZero() {
		t.Error("the zero ID reports IsZero false")
	}
	if known(t).IsZero() {
		t.Errorf("%s reports IsZero true", knownText)
	}

	b, err := json.Marshal(draft{Name: "a"})
	if err != nil {
		t.Fatalf("Marshal = %v, want the field dropped", err)
	}
	if want := `{"name":"a"}`; string(b) != want {
		t.Errorf("Marshal = %s, want %s", b, want)
	}
}

func TestABodyWhoseIDIsNotItsTextIsRefusedAndLeavesTheFieldAsItWas(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"upper-cased":             `{"id":"0192258C-4A7E-7B3C-9D1E-2F3A4B5C6D7E"}`,
		"braced":                  `{"id":"{0192258c-4a7e-7b3c-9d1e-2f3a4b5c6d7e}"}`,
		"urn prefixed":            `{"id":"urn:uuid:0192258c-4a7e-7b3c-9d1e-2f3a4b5c6d7e"}`,
		"undashed":                `{"id":"0192258c4a7e7b3c9d1e2f3a4b5c6d7e"}`,
		"version 4":               `{"id":"0192258c-4a7e-4b3c-9d1e-2f3a4b5c6d7e"}`,
		"the nil UUID":            `{"id":"00000000-0000-0000-0000-000000000000"}`,
		"the empty string":        `{"id":""}`,
		"a JSON null":             `{"id":null}`,
		"a number":                `{"id":1}`,
		"a boolean":               `{"id":true}`,
		"an object":               `{"id":{}}`,
		"an array of 16 numbers":  `{"id":[1,146,37,140,74,126,123,60,157,30,47,58,75,92,109,126]}`,
		"an escaped upper-casing": `{"id":"0192258c-4a7e-7b3c-9d1e-2f3a4b5c6d7E"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			prev := known(t)
			got := order{ID: prev}
			err := json.Unmarshal([]byte(body), &got)
			if !errors.Is(err, sortid.ErrMalformed) {
				t.Fatalf("Unmarshal(%s) = %v, want ErrMalformed", body, err)
			}
			if got.ID != prev {
				t.Errorf("Unmarshal(%s) refused but changed the field to %s, want %s", body, got.ID, prev)
			}
		})
	}
}

func TestUnmarshalTextAcceptsAndRefusesExactlyWhatParseDoes(t *testing.T) {
	t.Parallel()

	texts := []string{
		knownText,
		"0192258C-4A7E-7B3C-9D1E-2F3A4B5C6D7E",
		"{0192258c-4a7e-7b3c-9d1e-2f3a4b5c6d7e}",
		"urn:uuid:0192258c-4a7e-7b3c-9d1e-2f3a4b5c6d7e",
		"0192258c4a7e7b3c9d1e2f3a4b5c6d7e",
		"0192258c-4a7e-7b3c-9d1e-2f3a4b5c6d7e ",
		"0192258c-4a7e-4b3c-9d1e-2f3a4b5c6d7e",
		"0192258c-4a7e-7b3c-5d1e-2f3a4b5c6d7e",
		"00000000-0000-0000-0000-000000000000",
		"ffffffff-ffff-ffff-ffff-ffffffffffff",
		"",
	}
	for _, text := range texts {
		t.Run(text, func(t *testing.T) {
			t.Parallel()

			want, wantErr := sortid.Parse(text)

			prev := minter(t, stalled).Mint()
			got := prev
			var u encoding.TextUnmarshaler = &got
			err := u.UnmarshalText([]byte(text))
			if !errors.Is(err, wantErr) {
				t.Fatalf("UnmarshalText(%q) = %v, Parse = %v", text, err, wantErr)
			}
			if wantErr == nil && got != want {
				t.Errorf("UnmarshalText(%q) = %s, Parse = %s", text, got, want)
			}
			if wantErr != nil && got != prev {
				t.Errorf("UnmarshalText(%q) refused but changed the id to %s, want %s", text, got, prev)
			}
		})
	}
}
