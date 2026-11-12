package main

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestOkEnvelopeWrapsResult(t *testing.T) {
	raw, err := okEnvelope(identifierResponse{Identifier: "workbuddy"})
	if err != nil {
		t.Fatalf("okEnvelope: %v", err)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !env.OK {
		t.Fatal("ok=false on a success envelope")
	}
	if env.Error != nil {
		t.Fatalf("error present on success: %+v", env.Error)
	}
	var got identifierResponse
	if err := json.Unmarshal(env.Result, &got); err != nil {
		t.Fatalf("result is not identifierResponse: %v", err)
	}
	if got.Identifier != "workbuddy" {
		t.Fatalf("identifier = %q", got.Identifier)
	}
}

// A value json cannot marshal must surface as an error, not as a half-written
// envelope: the host would otherwise treat garbage as a successful reply.
func TestOkEnvelopePropagatesMarshalError(t *testing.T) {
	_, err := okEnvelope(make(chan int))
	if err == nil {
		t.Fatal("expected an error for an unmarshalable value")
	}
}

func TestErrorEnvelopeShape(t *testing.T) {
	var env envelope
	if err := json.Unmarshal(errorEnvelope("unknown_method", "nope"), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.OK {
		t.Fatal("ok=true on an error envelope")
	}
	if env.Error == nil || env.Error.Code != "unknown_method" || env.Error.Message != "nope" {
		t.Fatalf("error = %+v", env.Error)
	}
}

// statusError carries an HTTP status through to the client so CPA can answer
// with the upstream's real code instead of a generic 500.
type statusError struct {
	code int
	msg  string
}

func (e statusError) Error() string   { return e.msg }
func (e statusError) StatusCode() int { return e.code }

func TestErrorEnvelopeFromErrorCarriesStatusCode(t *testing.T) {
	var env envelope
	raw := errorEnvelopeFromError(statusError{code: 429, msg: "rate limited"})
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Error == nil {
		t.Fatal("no error in envelope")
	}
	if env.Error.HTTPStatus != 429 {
		t.Fatalf("http_status = %d, want 429", env.Error.HTTPStatus)
	}
	if env.Error.Code != "plugin_error" {
		t.Fatalf("code = %q", env.Error.Code)
	}
}

func TestErrorEnvelopeFromErrorWithoutStatus(t *testing.T) {
	var env envelope
	if err := json.Unmarshal(errorEnvelopeFromError(errors.New("plain")), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Error == nil || env.Error.HTTPStatus != 0 {
		t.Fatalf("error = %+v, want no http_status", env.Error)
	}
	if env.Error.Message != "plain" {
		t.Fatalf("message = %q", env.Error.Message)
	}
}
