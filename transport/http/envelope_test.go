package httpd_test

import (
	"encoding/json"
	"errors"
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
