package httpd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

const contentTypeJSON = "application/json"

// The codes DecodeJSON refuses with. A service's own codes are its own: declare
// them as constants of this type next to the handlers that write them.
const (
	CodeInvalidJSON     ErrorCode = "invalid_json"
	CodePayloadTooLarge ErrorCode = "payload_too_large"
)

// Fixed per code, never derived from what arrived: a refused body is described,
// never quoted back to the client that sent it.
const (
	messageInvalidJSON = "request body is not valid JSON"
	messageTooLarge    = "request body is too large"
)

// ErrorCode is the machine-readable name of a failure, the member a client
// branches on. It is its own type so that it and the human message beside it
// cannot be passed in the wrong order.
type ErrorCode string

// success is the body of an answer that carries a payload, failure the body of
// one that carries none. Two types rather than one with two optional members:
// no body can then hold both, and no failure can leak a half-built payload.
type success struct {
	Data any `json:"data"`
}

type failure struct {
	Error failureDetail `json:"error"`
}

type failureDetail struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
}

// WriteJSON answers status with data under the body's single "data" member.
func WriteJSON(w http.ResponseWriter, status int, data any) {
	writeEnvelope(w, status, success{Data: data})
}

// WriteError answers status with code and message under the body's single
// "error" member. code is what a client branches on, message is what a human
// reads; both reach that client, so neither carries a cause it may not see.
func WriteError(w http.ResponseWriter, status int, code ErrorCode, message string) {
	writeEnvelope(w, status, failure{Error: failureDetail{Code: code, Message: message}})
}

// An encoding failure has no answer left to give: the status line and the
// headers are on the wire by the time Encode runs.
func writeEnvelope(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", contentTypeJSON)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// DecodeJSON reads one JSON value from the request body into v, reading at most
// maxBytes bytes. It returns nil when v was filled.
//
// On any other return the refusal is already written through WriteError, with a
// status and a code but nothing of the body it refused: log the error for the
// cause and return without touching w again.
func DecodeJSON(w http.ResponseWriter, r *http.Request, maxBytes int64, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBytes))
	if err := dec.Decode(v); err != nil {
		return refuse(w, err)
	}
	// A request body is one value. Anything after it was never this contract,
	// and reading it as a second value is how the limit still applies to it.
	if err := dec.Decode(new(json.RawMessage)); !errors.Is(err, io.EOF) {
		return refuse(w, err)
	}
	return nil
}

// refuse answers the one failure a decode has, and hands the cause back. A nil
// err is the trailing-value case, which no reader reported.
func refuse(w http.ResponseWriter, err error) error {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		WriteError(w, http.StatusRequestEntityTooLarge, CodePayloadTooLarge, messageTooLarge)
		return fmt.Errorf("httpd: decode body: %w", err)
	}

	WriteError(w, http.StatusBadRequest, CodeInvalidJSON, messageInvalidJSON)
	if err == nil {
		return errors.New("httpd: decode body: a second value follows the first")
	}
	return fmt.Errorf("httpd: decode body: %w", err)
}
