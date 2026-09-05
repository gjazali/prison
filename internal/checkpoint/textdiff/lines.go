package textdiff

import "strings"

// Split breaks text into lines on "\n". Drops the trailing empty
// element that a final newline would produce. Returns the lines
// and whether text ended with a newline. Empty text yields nil.
func Split(text string) (lines []string, trailingNewline bool) {
	if text == "" {
		return nil, false
	}
	trailingNewline = strings.HasSuffix(text, "\n")
	if trailingNewline {
		text = text[:len(text)-1]
	}
	return strings.Split(text, "\n"), trailingNewline
}

// Join concatenates lines with "\n" between them. Appends a final
// newline when trailingNewline is true and there is at least one
// line. Returns "" for no lines.
func Join(lines []string, trailingNewline bool) string {
	if len(lines) == 0 {
		return ""
	}
	text := strings.Join(lines, "\n")
	if trailingNewline {
		text += "\n"
	}
	return text
}
