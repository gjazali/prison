// Package relay handles the broker's byte-level work: CONNECT splicing,
// single-connection HTTP serving, upstream proxying with credential
// injection, and JSON error responses.
package relay

import (
	"encoding/json"
	"net/http"
)

// errorEnvelope is the JSON structure for error responses.
type errorEnvelope struct {
	Error errorDetail `json:"error"`
}

// errorDetail holds the message inside an error envelope.
type errorDetail struct {
	Message string `json:"message"`
}

// WriteError writes a JSON error response with the given status and
// message.
func WriteError(w http.ResponseWriter, status int, message string) {
	body, err := json.Marshal(errorEnvelope{Error: errorDetail{Message: message}})
	if err != nil {
		body = []byte(`{"error":{"message":"internal error"}}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", itoa(len(body)))
	w.WriteHeader(status)
	w.Write(body)
}

// itoa formats a non-negative integer as a string.
func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	var digits [20]byte
	position := len(digits)
	for value > 0 {
		position--
		digits[position] = byte('0' + value%10)
		value /= 10
	}
	return string(digits[position:])
}
