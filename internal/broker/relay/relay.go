// Package relay moves bytes for the broker. It splices tunnels and
// proxies upstream requests with injected credentials.
package relay

import (
	"encoding/json"
	"net/http"
	"strconv"
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
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	w.Write(body)
}
