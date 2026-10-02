package relay

import (
	"bytes"
	"regexp"
	"strconv"
)

const ModelPreviewBytes = 64 << 10

// usageCarryBytes catches counters that a chunk boundary splits.
const usageCarryBytes = 128

var (
	modelPattern        = regexp.MustCompile(`"model"\s*:\s*"([^"]{1,128})"`)
	inputTokensPattern  = regexp.MustCompile(`"input_tokens"\s*:\s*(\d+)`)
	outputTokensPattern = regexp.MustCompile(`"output_tokens"\s*:\s*(\d+)`)
	usageMarker         = []byte("_tokens")
)

type UsageScanner struct {
	InputTokens  *int
	OutputTokens *int
	carry        []byte
}

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

func ModelFromPreview(preview []byte) string {
	match := modelPattern.FindSubmatch(preview)
	if match == nil {
		return ""
	}
	return string(match[1])
}

type previewCapture struct {
	limit int
	data  []byte
}

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
