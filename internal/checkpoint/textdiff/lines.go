package textdiff

import "strings"

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
