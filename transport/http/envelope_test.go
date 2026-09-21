package httpd_test

import (
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/menems/go-tk/transport/http"
)

// payload stands for whatever a handler answers with; the envelope is what is
// under test, not the shape a service puts inside it.
type payload struct {
	Name string `json:"name"`
}

// errorEnvelope decodes the failure side of the contract.
type errorEnvelope struct {
	Error struct {
		Code    httpd.ErrorCode `json:"code"`
		Message string          `json:"message"`
	} `json:"error"`
}

// members returns the top-level member names of a JSON object body, sorted, so
// a test can pin that a body carries one member and not the other.
func members(t *testing.T, body []byte) []string {
	t.Helper()

	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		t.Fatalf("body is not a JSON object: %v (%s)", err, body)
	}
	return slices.Sorted(maps.Keys(obj))
}

func TestWriteJSONAnswersTheStatusUnderOneDataMember(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	httpd.WriteJSON(rec, http.StatusCreated, payload{Name: "one"})

	if rec.Code != http.StatusCreated {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusCreated)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want %q", got, "application/json")
	}
	if got, want := members(t, rec.Body.Bytes()), []string{"data"}; !slices.Equal(got, want) {
		t.Errorf("body members = %v, want %v", got, want)
	}

	var body struct {
		Data payload `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Data.Name != "one" {
		t.Errorf("data.name = %q, want %q", body.Data.Name, "one")
	}
}

func TestWriteErrorAnswersTheStatusUnderOneErrorMember(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	httpd.WriteError(rec, http.StatusConflict, "already_exists", "a record with that key exists")

	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusConflict)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want %q", got, "application/json")
	}
	if got, want := members(t, rec.Body.Bytes()), []string{"error"}; !slices.Equal(got, want) {
		t.Errorf("body members = %v, want %v", got, want)
	}

	var body errorEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Error.Code != "already_exists" {
		t.Errorf("error.code = %q, want %q", body.Error.Code, "already_exists")
	}
	if body.Error.Message != "a record with that key exists" {
		t.Errorf("error.message = %q, want %q", body.Error.Message, "a record with that key exists")
	}
}

func TestDecodeJSONFillsTheValueAndAnswersNothing(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"one"}`))

	var got payload
	if err := httpd.DecodeJSON(rec, req, 1024, &got); err != nil {
		t.Fatalf("DecodeJSON: %v", err)
	}
	if got.Name != "one" {
		t.Errorf("name = %q, want %q", got.Name, "one")
	}
	if rec.Body.Len() != 0 {
		t.Errorf("wrote a body on success: %s", rec.Body.Bytes())
	}
}

func TestDecodeJSONRefusesThroughTheErrorEnvelope(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		body        string
		maxBytes    int64
		wantStatus  int
		wantCode    httpd.ErrorCode
		wantMessage string
	}{
		{
			name:        "a truncated body",
			body:        `{"name":`,
			maxBytes:    1024,
			wantStatus:  http.StatusBadRequest,
			wantCode:    httpd.CodeInvalidJSON,
			wantMessage: "request body is not valid JSON",
		},
		{
			name:        "an empty body",
			body:        ``,
			maxBytes:    1024,
			wantStatus:  http.StatusBadRequest,
			wantCode:    httpd.CodeInvalidJSON,
			wantMessage: "request body is not valid JSON",
		},
		{
			name:        "a member of the wrong type",
			body:        `{"name":42}`,
			maxBytes:    1024,
			wantStatus:  http.StatusBadRequest,
			wantCode:    httpd.CodeInvalidJSON,
			wantMessage: "request body is not valid JSON",
		},
		{
			name:        "a second value after the first",
			body:        `{"name":"one"}{"name":"two"}`,
			maxBytes:    1024,
			wantStatus:  http.StatusBadRequest,
			wantCode:    httpd.CodeInvalidJSON,
			wantMessage: "request body is not valid JSON",
		},
		{
			name:        "a body over the limit",
			body:        `{"name":"aaaaaaaaaaaaaaaaaaaa"}`,
			maxBytes:    8,
			wantStatus:  http.StatusRequestEntityTooLarge,
			wantCode:    httpd.CodePayloadTooLarge,
			wantMessage: "request body is too large",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tc.body))

			var got payload
			if err := httpd.DecodeJSON(rec, req, tc.maxBytes, &got); err == nil {
				t.Fatal("DecodeJSON = nil, want a refusal")
			}
			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if got, want := members(t, rec.Body.Bytes()), []string{"error"}; !slices.Equal(got, want) {
				t.Errorf("body members = %v, want %v", got, want)
			}

			var body errorEnvelope
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode body: %v", err)
			}
			if body.Error.Code != tc.wantCode {
				t.Errorf("error.code = %q, want %q", body.Error.Code, tc.wantCode)
			}
			// The message is a constant per code: what the caller sent never
			// comes back to it.
			if body.Error.Message != tc.wantMessage {
				t.Errorf("error.message = %q, want %q", body.Error.Message, tc.wantMessage)
			}
		})
	}
}

// TestDecodeJSONReportsTheLimitToTheCaller pins the other half of a refusal:
// the answer is already written, and the cause is still the handler's to log.
func TestDecodeJSONReportsTheLimitToTheCaller(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"aaaaaaaaaaaaaaaaaaaa"}`))

	var got payload
	err := httpd.DecodeJSON(rec, req, 8, &got)

	var tooLarge *http.MaxBytesError
	if !errors.As(err, &tooLarge) {
		t.Fatalf("DecodeJSON = %v, want an error wrapping *http.MaxBytesError", err)
	}
	if tooLarge.Limit != 8 {
		t.Errorf("limit = %d, want 8", tooLarge.Limit)
	}
}

// serveDecode runs a server whose only handler decodes the request body under
// maxBytes and answers with what it decoded. This is the seam: a consumer
// meets DecodeJSON inside a handler the server calls, against the server's own
// ResponseWriter, which a recorder only stands in for.
func serveDecode(t *testing.T, maxBytes int64) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got payload
		if err := httpd.DecodeJSON(w, r, maxBytes, &got); err != nil {
			return // the refusal is already written
		}
		httpd.WriteJSON(w, http.StatusOK, got)
	}))
	t.Cleanup(srv.Close)

	return srv
}

// post sends body to srv and returns the status and the whole answer.
func post(t *testing.T, srv *httptest.Server, body string) (int, []byte) {
	t.Helper()

	resp, err := srv.Client().Post(srv.URL, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()

	answer, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read answer: %v", err)
	}
	return resp.StatusCode, answer
}

// assertRefusal checks the answer is the failure envelope and nothing else.
func assertRefusal(t *testing.T, status int, answer []byte, wantStatus int, wantCode httpd.ErrorCode, wantMessage string) {
	t.Helper()

	if status != wantStatus {
		t.Errorf("status = %d, want %d (%s)", status, wantStatus, answer)
	}
	if got, want := members(t, answer), []string{"error"}; !slices.Equal(got, want) {
		t.Errorf("body members = %v, want %v", got, want)
	}

	var body errorEnvelope
	if err := json.Unmarshal(answer, &body); err != nil {
		t.Fatalf("decode answer: %v", err)
	}
	if body.Error.Code != wantCode {
		t.Errorf("error.code = %q, want %q", body.Error.Code, wantCode)
	}
	if body.Error.Message != wantMessage {
		t.Errorf("error.message = %q, want %q", body.Error.Message, wantMessage)
	}
}

// assertNoEcho checks the refusal repeats nothing of the body it rejected: the
// answer reaches the client, and what that client sent is not described back
// to it.
func assertNoEcho(t *testing.T, answer []byte, sent string) {
	t.Helper()

	if strings.Contains(string(answer), sent) {
		t.Errorf("answer repeats %q of the refused body: %s", sent, answer)
	}
}

// TestDecodeJSONOverAServedRequest pins the acceptance at the byte: a body of
// exactly the limit passes, the same body one byte over it does not.
func TestDecodeJSONOverAServedRequest(t *testing.T) {
	t.Parallel()

	// marker is what the no-echo assertions look for in a refusal.
	const sent = `{"name":"marker"}`

	t.Run("a body of exactly the limit lands in the value", func(t *testing.T) {
		t.Parallel()

		status, answer := post(t, serveDecode(t, int64(len(sent))), sent)

		if status != http.StatusOK {
			t.Fatalf("status = %d, want %d (%s)", status, http.StatusOK, answer)
		}

		var body struct {
			Data payload `json:"data"`
		}
		if err := json.Unmarshal(answer, &body); err != nil {
			t.Fatalf("decode answer: %v", err)
		}
		if body.Data.Name != "marker" {
			t.Errorf("data.name = %q, want %q", body.Data.Name, "marker")
		}
	})

	t.Run("a body one byte over the limit is refused with 413", func(t *testing.T) {
		t.Parallel()

		status, answer := post(t, serveDecode(t, int64(len(sent))-1), sent)

		assertRefusal(t, status, answer, http.StatusRequestEntityTooLarge, httpd.CodePayloadTooLarge, "request body is too large")
		assertNoEcho(t, answer, "marker")
	})

	t.Run("a malformed body is refused with 400", func(t *testing.T) {
		t.Parallel()

		status, answer := post(t, serveDecode(t, 1024), `{"name":"marker"`)

		assertRefusal(t, status, answer, http.StatusBadRequest, httpd.CodeInvalidJSON, "request body is not valid JSON")
		assertNoEcho(t, answer, "marker")
	})
}

// TestDecodeJSONBoundsAtTheLimitItsCallerNamed pins that the limit belongs to
// the caller and not to the package: one body is refused under one limit and
// accepted under another.
func TestDecodeJSONBoundsAtTheLimitItsCallerNamed(t *testing.T) {
	t.Parallel()

	const short = `{"name":"aa"}`
	const long = `{"name":"aaaaaaaaaa"}`

	cases := []struct {
		name       string
		maxBytes   int64
		body       string
		wantStatus int
	}{
		{name: "the short body under the tight limit", maxBytes: int64(len(short)), body: short, wantStatus: http.StatusOK},
		{name: "the long body under the tight limit", maxBytes: int64(len(short)), body: long, wantStatus: http.StatusRequestEntityTooLarge},
		{name: "the short body under the wide limit", maxBytes: int64(len(long)), body: short, wantStatus: http.StatusOK},
		{name: "the long body under the wide limit", maxBytes: int64(len(long)), body: long, wantStatus: http.StatusOK},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			status, answer := post(t, serveDecode(t, tc.maxBytes), tc.body)

			if status != tc.wantStatus {
				t.Fatalf("status = %d, want %d (%s)", status, tc.wantStatus, answer)
			}
		})
	}
}
