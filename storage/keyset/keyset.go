// Package keyset serves a list page by page, newest first, keyed on a
// sortid.ID, from the cursor the previous page returned.
//
// A Pager is built at wiring with the service's default and largest page
// sizes. It reads the texts a request carried into a Query, a bound and a row
// limit of one more than the page size, which the service's own query applies:
//
//	WHERE id < $bound ORDER BY id DESC LIMIT $limit
//
// Page then checks the rows that query returned against that contract and
// turns them into the page and the next cursor, the extra row telling a last
// page from a full one. The package runs no query: the table, the filters and
// the driver are the service's, and a handler reads a cursor off a request
// without importing a driver's module.
//
// The cursor grants nothing and is not signed. A client may write any cursor
// Query accepts, which only moves where the walk starts within the rows the
// service's own query already scopes to the caller: its WHERE, not the
// cursor, is what authorizes. A cursor is the text of the last row's id, so it
// publishes that row's creation time as the id does.
package keyset

import (
	"errors"
	"fmt"
	"math"
	"strconv"

	"github.com/menems/go-tk/sortid"
)

// ErrCursor answers a cursor text that is not one Page writes. It is one fixed
// error whatever arrived, and carries no part of it, so a refusal never echoes
// a request back into a log or a body.
var ErrCursor = errors.New("keyset: not a cursor this package writes")

// ErrSize answers a size text that is not a decimal integer from 1 to the
// largest size named at wiring. Like ErrCursor it is one fixed error and
// carries no part of what arrived.
var ErrSize = errors.New("keyset: not a page size this service serves")

// above is the bound of a walk's first page: every byte set, so it compares
// above every version 7 id. It is no id, and fails MarshalText.
var above = sortid.ID{
	0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff,
	0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff,
}

// Pager reads a request's texts into a Query. It holds no state past wiring
// and is safe for concurrent use.
type Pager struct {
	size    int
	largest int
}

// New builds a Pager serving pages of size rows, and never more than largest.
// A largest below 1 or at math.MaxInt, or a size below 1 or above largest, is
// refused here rather than on the first request, and no Pager is returned.
func New(size, largest int) (*Pager, error) {
	if largest < 1 {
		return nil, fmt.Errorf("keyset: largest page size %d is below 1", largest)
	}
	// A query asks for one row past the page: at math.MaxInt that limit
	// wraps to a negative LIMIT, and Page would refuse every page.
	if largest == math.MaxInt {
		return nil, fmt.Errorf("keyset: largest page size %d leaves no room for the row past the page", largest)
	}
	if size < 1 || size > largest {
		return nil, fmt.Errorf("keyset: default page size %d is outside 1 to %d", size, largest)
	}
	return &Pager{size: size, largest: largest}, nil
}

// Texts are the texts a request carried, read by the handler under the names
// it owns. A field's zero value is the request not carrying it: an empty
// Size serves the default size, an empty Cursor starts the walk at the newest
// row.
type Texts struct {
	Size   string
	Cursor string
}

// Query is what the service's query applies: the rows whose id is below Bound,
// newest first, at most Limit of them. Its zero value is no Pager's, and Page
// refuses it.
type Query struct {
	bound sortid.ID
	limit int
}

// Bound is the id every row must sort below. The first page's is no id: it
// compares above every one and fails MarshalText, and the service hands it to
// its query as uuid.UUID(q.Bound()).
func (q Query) Bound() sortid.ID { return q.bound }

// Limit is the most rows the query returns: one more than the page size, the
// extra row telling a last page from a full one.
func (q Query) Limit() int { return q.limit }

// Query reads t into the query the service runs. A size that is not a decimal
// integer from 1 to the largest is refused under ErrSize, a cursor Page did not
// write under ErrCursor; each text's length is checked first, so what a parse
// costs does not grow with what arrives.
func (p *Pager) Query(t Texts) (Query, error) {
	size, err := p.sizeOf(t.Size)
	if err != nil {
		return Query{}, err
	}
	q := Query{bound: above, limit: size + 1}
	if t.Cursor == "" {
		return q, nil
	}
	id, err := sortid.Parse(t.Cursor)
	if err != nil {
		return Query{}, ErrCursor
	}
	q.bound = id
	return q, nil
}

// sizeOf reads a size text, the default when empty. Only ASCII digits pass,
// since strconv.Atoi alone takes a leading sign; a text wider than the
// largest's digits is refused before any is read, so what Atoi reads is as
// short as the largest. A text of that width past an int is Atoi's range
// error, refused like any other.
func (p *Pager) sizeOf(text string) (int, error) {
	if text == "" {
		return p.size, nil
	}
	if len(text) > len(strconv.Itoa(p.largest)) {
		return 0, ErrSize
	}
	for i := range len(text) {
		if text[i] < '0' || text[i] > '9' {
			return 0, ErrSize
		}
	}
	n, err := strconv.Atoi(text)
	if err != nil || n < 1 || n > p.largest {
		return 0, ErrSize
	}
	return n, nil
}

// Page turns the rows the service's query returned for q into the page and
// the next cursor, "" on the last page. id reads a row's key.
//
// Rows breaking what q asked for, more than its limit, one at or above its
// bound, two out of descending order or an id of another version than 7, are
// refused under an error that is neither ErrCursor, ErrSize nor
// sortid.ErrMalformed: they are the service's query failing its contract, not
// the request, and no page nor cursor is returned. The page is the
// rows' own slice, clipped so an append cannot overwrite the extra row, and an
// allocated one when empty, so a response writes [] and not null.
func Page[R any](q Query, rows []R, id func(R) sortid.ID) ([]R, string, error) {
	if q.limit < 2 {
		return nil, "", errors.New("keyset: a query no Pager handed out")
	}
	if len(rows) > q.limit {
		return nil, "", fmt.Errorf("keyset: %d rows past the limit of %d", len(rows), q.limit)
	}
	prev := q.bound
	for i, r := range rows {
		k := id(r)
		if k.Compare(prev) >= 0 {
			return nil, "", fmt.Errorf("keyset: row %d not below the bound or the row before it", i)
		}
		// MarshalText refuses exactly the ids Parse does: a row of another
		// version would write a cursor the next request is refused on. Its
		// error is sortid.ErrMalformed, a request's refusal, so it is not
		// wrapped: the breach is the service's query, not the client's.
		if _, err := k.MarshalText(); err != nil {
			return nil, "", fmt.Errorf("keyset: row %d is not a version 7 id", i)
		}
		prev = k
	}
	size := q.limit - 1
	if len(rows) <= size {
		if len(rows) == 0 {
			return []R{}, "", nil
		}
		return rows[:len(rows):len(rows)], "", nil
	}
	return rows[:size:size], id(rows[size-1]).String(), nil
}
