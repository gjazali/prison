package relay

import (
	"bytes"
	"regexp"
	"strconv"
)

// ModelPreviewBytes is the number of request body bytes kept for model
// name extraction.
const ModelPreviewBytes = 64 << 10

// usageCarryBytes is the number of trailing bytes kept between chunks
// to catch counters split across reads.
const usageCarryBytes = 128

var (
	modelPattern        = regexp.MustCompile(`"model"\s*:\s*"([^"]{1,128})"`)
	inputTokensPattern  = regexp.MustCompile(`"input_tokens"\s*:\s*(\d+)`)
	outputTokensPattern = regexp.MustCompile(`"output_tokens"\s*:\s*(\d+)`)
	usageMarker         = []byte("_tokens")
)

// UsageScanner extracts token counts from a streaming response. Feed
// it chunks in order. Counts are nil until a match is found.
type UsageScanner struct {
	InputTokens  *int
	OutputTokens *int
	carry        []byte
}

// Feed scans a response chunk for token counters. Keeps the last
// value of each counter found.
func (scanner *UsageScanner) Feed(chunk []byte) {
	scannable := append(scanner.carry, chunk...)
	if bytes.Contains(scannable, usageMarker) {
		if value, ok := lastCount(inputTokensPattern, scannable); ok {
			scanner.InputTokens = &value
		}
		if value, ok := lastCount(outputTokensPattern, scannable); ok {
			scanner.OutputTokens = &value
		}
	}
	if len(scannable) > usageCarryBytes {
		scannable = scannable[len(scannable)-usageCarryBytes:]
	}
	scanner.carry = append([]byte(nil), scannable...)
}

// lastCount returns the integer from the last match of pattern in
// data, or false if no match.
func lastCount(pattern *regexp.Regexp, data []byte) (int, bool) {
	matches := pattern.FindAllSubmatch(data, -1)
	if len(matches) == 0 {
		return 0, false
	}
	value, err := strconv.Atoi(string(matches[len(matches)-1][1]))
	if err != nil {
		return 0, false
	}
	return value, true
}

// ModelFromPreview extracts the model name from a request body
// preview. Returns an empty string if not found.
func ModelFromPreview(preview []byte) string {
	match := modelPattern.FindSubmatch(preview)
	if match == nil {
		return ""
	}
	return string(match[1])
}

// previewCapture keeps the first `limit` bytes and discards the rest.
type previewCapture struct {
	limit int
	data  []byte
}

// observe appends chunk data up to the capture limit.
func (capture *previewCapture) observe(chunk []byte) {
	remaining := capture.limit - len(capture.data)
	if remaining <= 0 {
		return
	}
	if len(chunk) > remaining {
		chunk = chunk[:remaining]
	}
	capture.data = append(capture.data, chunk...)
}
