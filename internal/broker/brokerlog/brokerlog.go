// Package brokerlog reads and writes the broker's JSON-lines request
// log.
package brokerlog

import "time"

const (
	KindEgress = "egress"
	KindInmate = "inmate"
	KindRoute  = "route"
	KindSign   = "sign"
)

const (
	DefaultMaxBytes int64 = 16 << 20
	DefaultKeep           = 3
)

// Entry uses pointers for token counts so that a reported zero is
// different from an absent count.
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
