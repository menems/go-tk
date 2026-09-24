package keyset_test

import (
	"errors"
	"math"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/menems/go-tk/sortid"
	"github.com/menems/go-tk/storage/keyset"
)

// start is the instant every test clock stands at, 2026-09-24T10:00:00Z.
var start = time.Date(2026, time.September, 24, 10, 0, 0, 0, time.UTC)

// refused is the one text every cursor refusal carries, whatever arrived.
const refused = "keyset: not a cursor this package writes"

// refusedSize is the one text every size refusal carries, whatever arrived.
const refusedSize = "keyset: not a page size this service serves"

type row struct {
	ID   sortid.ID
	Name string
}

func rowID(r row) sortid.ID { return r.ID }

// store is a list of rows in the order they were minted, answering a query
// as the service's own would: id < bound ORDER BY id DESC LIMIT limit.
type store struct {
	mint *sortid.Minter
	rows []row
}

func newStore(t *testing.T) *store {
	t.Helper()

	m, err := sortid.NewMinter(func() time.Time { return start })
	if err != nil {
		t.Fatalf("NewMinter = %v, want a minter", err)
	}
	return &store{mint: m}
}

// add mints n rows and returns their ids in mint order.
func (s *store) add(n int) []sortid.ID {
	ids := make([]sortid.ID, n)
	for i := range ids {
		ids[i] = s.mint.Mint()
		s.rows = append(s.rows, row{ID: ids[i]})
	}
	return ids
}

func (s *store) list(q keyset.Query) []row {
	var out []row
	for i := len(s.rows) - 1; i >= 0 && len(out) < q.Limit(); i-- {
		if s.rows[i].ID.Compare(q.Bound()) < 0 {
			out = append(out, s.rows[i])
		}
	}
	return out
}

func pager(t *testing.T, size, largest int) *keyset.Pager {
	t.Helper()

	p, err := keyset.New(size, largest)
	if err != nil {
		t.Fatalf("New(%d, %d) = %v, want a pager", size, largest, err)
	}
	return p
}

// page serves one page of the size text size from cursor, failing the test on
// any refusal.
func page(t *testing.T, p *keyset.Pager, s *store, size, cursor string) ([]row, string) {
	t.Helper()

	q, err := p.Query(keyset.Texts{Size: size, Cursor: cursor})
	if err != nil {
		t.Fatalf("Query(%q, %q) = %v, want a query", size, cursor, err)
	}
	rows, next, err := keyset.Page(q, s.list(q), rowID)
	if err != nil {
		t.Fatalf("Page from %q = %v, want a page", cursor, err)
	}
	return rows, next
}

// walk serves pages of the size text size from next, then from each next
// cursor until none is returned, and returns every page's rows. It gives up
// past 100 pages, so a walk that never ends fails instead of hanging.
func walk(t *testing.T, p *keyset.Pager, s *store, size, next string, pages [][]row) [][]row {
	t.Helper()

	for range 100 {
		var rows []row
		rows, next = page(t, p, s, size, next)
		pages = append(pages, rows)
		if next == "" {
			return pages
		}
	}
	t.Fatalf("walk did not end after 100 pages")
	return nil
}

func lens(pages [][]row) []int {
	out := make([]int, len(pages))
	for i, rows := range pages {
		out[i] = len(rows)
	}
	return out
}

func ids(pages [][]row) []sortid.ID {
	var out []sortid.ID
	for _, rows := range pages {
		for _, r := range rows {
			out = append(out, r.ID)
		}
	}
	return out
}

func newestFirst(minted []sortid.ID) []sortid.ID {
	out := slices.Clone(minted)
	slices.Reverse(out)
	return out
}

func TestAWalkReturnsEveryRowOnceNewestFirstAndEndsWithoutACursor(t *testing.T) {
	t.Parallel()

	// A default size of 3 and a largest of 10.
	cases := map[string]struct {
		rows  int
		pages []int
	}{
		"an empty list":                 {rows: 0, pages: []int{0}},
		"one row":                       {rows: 1, pages: []int{1}},
		"exactly one page":              {rows: 3, pages: []int{3}},
		"one row past a page":           {rows: 4, pages: []int{3, 1}},
		"exactly two pages":             {rows: 6, pages: []int{3, 3}},
		"two pages and a partial third": {rows: 7, pages: []int{3, 3, 1}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s := newStore(t)
			minted := s.add(c.rows)
			pages := walk(t, pager(t, 3, 10), s, "", "", nil)

			if got := lens(pages); !slices.Equal(got, c.pages) {
				t.Fatalf("page lengths = %v, want %v", got, c.pages)
			}
			if got, want := ids(pages), newestFirst(minted); !slices.Equal(got, want) {
				t.Errorf("walk = %v, want every row once newest first %v", got, want)
			}
			if pages[0] == nil {
				t.Error("first page is nil, want an allocated slice, so a response writes [] and not null")
			}
		})
	}
}

func TestRowsMintedDuringAWalkAreNotReturnedAndNoOlderRowIsSkippedOrRepeated(t *testing.T) {
	t.Parallel()

	s := newStore(t)
	minted := s.add(7)
	p := pager(t, 3, 10)

	first, next := page(t, p, s, "", "")
	s.add(2)
	pages := walk(t, p, s, "", next, [][]row{first})

	if got, want := lens(pages), []int{3, 3, 1}; !slices.Equal(got, want) {
		t.Fatalf("page lengths = %v, want %v", got, want)
	}
	if got, want := ids(pages), newestFirst(minted); !slices.Equal(got, want) {
		t.Errorf("walk = %v, want the 7 rows minted before it %v", got, want)
	}
}

func TestAQueryAsksForOneRowMoreThanTheDefaultSize(t *testing.T) {
	t.Parallel()

	q, err := pager(t, 25, 100).Query(keyset.Texts{})
	if err != nil {
		t.Fatalf("Query = %v, want a query", err)
	}
	if got := q.Limit(); got != 26 {
		t.Errorf("Limit = %d, want 26", got)
	}
}

func TestASizeTextFromOneToTheLargestServesPagesOfThatSizeToTheEnd(t *testing.T) {
	t.Parallel()

	// A default size of 3 and a largest of 10.
	cases := map[string]struct {
		size  string
		rows  int
		pages []int
	}{
		"no size text serves the default": {size: "", rows: 7, pages: []int{3, 3, 1}},
		"a size of 1":                     {size: "1", rows: 3, pages: []int{1, 1, 1}},
		"a size between":                  {size: "4", rows: 9, pages: []int{4, 4, 1}},
		"the largest":                     {size: "10", rows: 21, pages: []int{10, 10, 1}},
		"a leading zero within the width": {size: "05", rows: 5, pages: []int{5}},
		"a size past the list":            {size: "9", rows: 2, pages: []int{2}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s := newStore(t)
			minted := s.add(c.rows)
			pages := walk(t, pager(t, 3, 10), s, c.size, "", nil)

			if got := lens(pages); !slices.Equal(got, c.pages) {
				t.Fatalf("page lengths = %v, want %v", got, c.pages)
			}
			if got, want := ids(pages), newestFirst(minted); !slices.Equal(got, want) {
				t.Errorf("walk = %v, want every row once newest first %v", got, want)
			}
		})
	}
}

func TestASizeTextOutsideTheBoundsOrNotADecimalIntegerIsRefusedUnderOneFixedError(t *testing.T) {
	t.Parallel()

	// A default size of 3 and a largest of 10, two digits wide.
	cases := map[string]string{
		"zero":                  "0",
		"zero within the width": "00",
		"a negative size":       "-1",
		"one above the largest": "11",
		"far above the largest": "99",
		"a plus sign":           "+5",
		"a leading space":       " 5",
		"a trailing space":      "5 ",
		"a fraction":            "2.5",
		"an exponent":           "1e1",
		"a hexadecimal prefix":  "0x5",
		"a non-ASCII digit":     "٥",
		"digits past the width": "010",
		"far over the width":    strings.Repeat("1", 1<<20),
		"past an int":           "99999999999999999999",
	}
	for name, text := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			q, err := pager(t, 3, 10).Query(keyset.Texts{Size: text})
			if !errors.Is(err, keyset.ErrSize) {
				t.Fatalf("Query(%.40q) = %v, want ErrSize", text, err)
			}
			if errors.Is(err, keyset.ErrCursor) {
				t.Errorf("Query(%.40q) refuses under ErrCursor, want ErrSize alone", text)
			}
			if err.Error() != refusedSize {
				t.Errorf("Query(%.40q) refuses with %q, want the fixed %q", text, err.Error(), refusedSize)
			}
			if q != (keyset.Query{}) {
				t.Errorf("Query(%.40q) refused but returned %+v, want the zero Query", text, q)
			}
		})
	}
}

func TestACursorThePagerDoesNotWriteIsRefusedUnderOneFixedError(t *testing.T) {
	t.Parallel()

	s := newStore(t)
	s.add(4)
	_, cursor := page(t, pager(t, 3, 10), s, "", "")

	cases := map[string]string{
		"one character long":        cursor + "0",
		"far over the length":       strings.Repeat("0", 1<<20),
		"truncated":                 cursor[:len(cursor)-1],
		"an id of version 4":        "0192258c-4a7e-4b3c-9d1e-2f3a4b5c6d7e",
		"upper-cased":               strings.ToUpper(cursor),
		"the nil UUID":              "00000000-0000-0000-0000-000000000000",
		"a blank of the same width": strings.Repeat(" ", len(cursor)),
	}
	for name, text := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			q, err := pager(t, 3, 10).Query(keyset.Texts{Cursor: text})
			if !errors.Is(err, keyset.ErrCursor) {
				t.Fatalf("Query(%.40q) = %v, want ErrCursor", text, err)
			}
			if err.Error() != refused {
				t.Errorf("Query(%.40q) refuses with %q, want the fixed %q", text, err.Error(), refused)
			}
			if q != (keyset.Query{}) {
				t.Errorf("Query(%.40q) refused but returned %+v, want the zero Query", text, q)
			}
		})
	}
}

func TestRowsBreakingWhatThePagerAskedForAreRefusedAndNoPageIsReturned(t *testing.T) {
	t.Parallel()

	s := newStore(t)
	// a < b < c < d < e, in mint order.
	minted := s.add(5)
	a, b, c, d, e := minted[0], minted[1], minted[2], minted[3], minted[4]
	v4 := sortid.ID(uuid.MustParse("0192258c-4a7e-4b3c-9d1e-2f3a4b5c6d7e"))

	// A size of 2 asks for 3 rows below the bound d.
	q, err := pager(t, 2, 10).Query(keyset.Texts{Cursor: d.String()})
	if err != nil {
		t.Fatalf("Query(%s) = %v, want a query", d, err)
	}
	// The same size below the bound e: d, c, b and a are four rows below it,
	// strictly descending and of version 7, so the limit alone refuses them.
	qe, err := pager(t, 2, 10).Query(keyset.Texts{Cursor: e.String()})
	if err != nil {
		t.Fatalf("Query(%s) = %v, want a query", e, err)
	}

	cases := map[string]struct {
		q   keyset.Query
		ids []sortid.ID
	}{
		"more than the row limit":      {qe, []sortid.ID{d, c, b, a}},
		"a row at the bound":           {q, []sortid.ID{d, c}},
		"a row above the bound":        {q, []sortid.ID{e}},
		"two rows in ascending order":  {q, []sortid.ID{b, c}},
		"the same row twice":           {q, []sortid.ID{c, c}},
		"a full page ending ascending": {q, []sortid.ID{c, a, b}},
		"an id of version 4":           {q, []sortid.ID{v4}},
		"the zero ID":                  {q, []sortid.ID{{}}},
		"a query no pager handed out":  {keyset.Query{}, nil},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rows := make([]row, len(tc.ids))
			for i, id := range tc.ids {
				rows[i] = row{ID: id}
			}
			got, next, err := keyset.Page(tc.q, rows, rowID)
			if err == nil {
				t.Fatalf("Page(%v) = %v, %q, nil, want a refusal", tc.ids, got, next)
			}
			if errors.Is(err, keyset.ErrCursor) || errors.Is(err, keyset.ErrSize) || errors.Is(err, sortid.ErrMalformed) {
				t.Errorf("Page(%v) refuses under ErrCursor, ErrSize or sortid.ErrMalformed, want an error of its own", tc.ids)
			}
			if got != nil || next != "" {
				t.Errorf("Page(%v) refused but returned %v, %q, want no page and no cursor", tc.ids, got, next)
			}
		})
	}
}

func TestSizesOutsideTheirBoundsAreRefusedAtWiringNamingTheValue(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		size, largest int
		named         string
	}{
		"a largest of 0":              {size: 1, largest: 0, named: "largest page size 0"},
		"a negative largest":          {size: 1, largest: -5, named: "largest page size -5"},
		"a default of 0":              {size: 0, largest: 10, named: "default page size 0"},
		"a negative default":          {size: -1, largest: 10, named: "default page size -1"},
		"a default above the largest": {size: 11, largest: 10, named: "default page size 11"},
		"a largest whose row limit overflows an int": {
			size: 1, largest: math.MaxInt, named: "largest page size " + strconv.Itoa(math.MaxInt),
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			p, err := keyset.New(c.size, c.largest)
			if err == nil {
				t.Fatalf("New(%d, %d) = nil error, want a refusal", c.size, c.largest)
			}
			if !strings.Contains(err.Error(), c.named) {
				t.Errorf("New(%d, %d) refuses with %q, want it to name %q", c.size, c.largest, err.Error(), c.named)
			}
			if p != nil {
				t.Errorf("New(%d, %d) refused but returned %v, want no pager", c.size, c.largest, p)
			}
		})
	}
}

func TestADefaultEqualToTheLargestIsAccepted(t *testing.T) {
	t.Parallel()

	pager(t, 10, 10)
	pager(t, 1, 1)
}
