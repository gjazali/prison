// Package brokerlog reads and writes the broker's JSON-lines request
// log. All rows share one struct shape.
package brokerlog

import "time"

// Row kind values.
const (
	KindEgress = "egress"
	KindInmate = "inmate"
	KindRoute  = "route"
	KindSign   = "sign"
)

const (
	// DefaultMaxBytes is the byte size that triggers rotation.
	DefaultMaxBytes int64 = 16 << 20
	// DefaultKeep is the number of rotated files kept beside the live one.
	DefaultKeep = 3
)

// Entry is one logged row. Time and Kind are always set. Other fields
// are filled only by the kinds that use them. Token counts are
// pointers so a reported zero is distinct from absent.
type Entry struct {
	Time         time.Time `json:"time"`
	Kind         string    `json:"kind"`
	Project      string    `json:"project,omitempty"`
	Inmate       string    `json:"inmate,omitempty"`
	Secret       string    `json:"secret,omitempty"`
	Client       string    `json:"client,omitempty"`
	Method       string    `json:"method,omitempty"`
	Host         string    `json:"host,omitempty"`
	Port         int       `json:"port,omitempty"`
	Path         string    `json:"path,omitempty"`
	Outcome      string    `json:"outcome,omitempty"`
	Status       int       `json:"status,omitempty"`
	Model        string    `json:"model,omitempty"`
	InputTokens  *int      `json:"input_tokens,omitempty"`
	OutputTokens *int      `json:"output_tokens,omitempty"`
	Bytes        int64     `json:"bytes,omitempty"`
	DurationMS   int64     `json:"duration_ms,omitempty"`
	Error        string    `json:"error,omitempty"`
}
