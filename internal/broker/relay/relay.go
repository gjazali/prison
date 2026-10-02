// Package relay moves bytes for the broker. It splices tunnels and
// proxies upstream requests with injected credentials.
package relay

import (
	"encoding/json"
	"net/http"
)

type errorEnvelope struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Message string `json:"message"`
}

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
