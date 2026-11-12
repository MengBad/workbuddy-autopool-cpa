// Response envelopes: every handler answers with one of these shapes.
package main

import (
	"encoding/json"
	"errors"
)

// -----------------------------------------------------------------------------

func okEnvelope(v any) ([]byte, error) {
	result, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return json.Marshal(envelope{OK: true, Result: result})
}

func errorEnvelope(code, message string) []byte {
	raw, _ := json.Marshal(envelope{OK: false, Error: &envelopeError{Code: code, Message: message}})
	return raw
}

func errorEnvelopeFromError(err error) []byte {
	pluginErr := &envelopeError{Code: "plugin_error", Message: redactSecrets(err.Error())}
	var statusErr interface{ StatusCode() int }
	if errors.As(err, &statusErr) {
		pluginErr.HTTPStatus = statusErr.StatusCode()
	}
	raw, _ := json.Marshal(envelope{OK: false, Error: pluginErr})
	return raw
}
