package ecosystem

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"prison/internal/config"
)

type ProposeOptions struct {
	SkipDocs bool
}

// ProposeProjectConfig returns the proposed `prison.toml` content and whether
// any changes were made. Existing comments and unrelated keys are preserved.
func ProposeProjectConfig(
	existing []byte, detection *Detection, options ProposeOptions,
) (proposed []byte, changed bool, err error) {
	if detection == nil {
		return existing, false, nil
	}
	egressHosts := proposedEgressHosts(detection, options.SkipDocs)
	if len(detection.Shadow) == 0 && len(detection.SetupCommands) == 0 &&
		len(detection.CheckpointIgnore) == 0 && len(egressHosts) == 0 {
		return existing, false, nil
	}
	if len(strings.TrimSpace(string(existing))) == 0 {
		rendered := renderNewConfig(detection, egressHosts)
		if _, err := config.LoadProject("prison.toml", []byte(rendered)); err != nil {
			return nil, false, fmt.Errorf("generated prison.toml is not valid: %w", err)
		}
		return []byte(rendered), true, nil
	}

	parsed, err := config.LoadProject("prison.toml", existing)
	if err != nil {
		return nil, false, err
	}

	additions := []struct {
		section string
		key     string
		values  []string
	}{
		{"box", "shadow", missingValues(detection.Shadow, parsed.Box.Shadow)},
		{"setup", "commands", missingValues(detection.SetupCommands, parsed.Setup.Commands)},
		{"checkpoint", "ignore", missingValues(detection.CheckpointIgnore, parsed.Checkpoint.Ignore)},
		{"egress", "hosts", missingValues(egressHosts, parsed.Egress.Hosts)},
	}

	text := string(existing)
	for _, item := range additions {
		if len(item.values) == 0 {
			continue
		}
		updated, err := patchTOMLArray(text, item.section, item.key, item.values)
		if err != nil {
			return nil, false, fmt.Errorf("cannot update [%s] %s: %w",
				item.section, item.key, err)
		}
		text = updated
		changed = true
	}
	if !changed {
		return existing, false, nil
	}
	if _, err := config.LoadProject("prison.toml", []byte(text)); err != nil {
		return nil, false, fmt.Errorf("updated prison.toml is not valid: %w", err)
	}
	return []byte(text), true, nil
}

func missingValues(proposed, existing []string) []string {
	var missing []string
	for _, value := range proposed {
		if !slices.Contains(existing, value) {
			missing = append(missing, value)
		}
	}
	return missing
}

func proposedEgressHosts(detection *Detection, skipDocs bool) []string {
	hosts := append([]string(nil), detection.EgressHosts...)
	if !skipDocs {
		hosts = append(hosts, detection.EgressDocs...)
	}
	return hosts
}

func renderNewConfig(detection *Detection, egressHosts []string) string {
	var sections []string
	if len(detection.Shadow) > 0 {
		sections = append(sections,
			"[box]\nshadow = "+formatArray(detection.Shadow))
	}
	if len(detection.SetupCommands) > 0 {
		sections = append(sections,
			"[setup]\ncommands = "+formatArray(detection.SetupCommands))
	}
	if len(detection.CheckpointIgnore) > 0 {
		sections = append(sections,
			"[checkpoint]\nignore = "+formatArray(detection.CheckpointIgnore))
	}
	if len(egressHosts) > 0 {
		sections = append(sections,
			"[egress]\nhosts = "+formatArray(egressHosts))
	}
	if len(sections) == 0 {
		return ""
	}
	return strings.Join(sections, "\n\n") + "\n"
}

func formatArray(values []string) string {
	if len(values) == 1 && len(values[0]) <= 50 {
		return "[" + strconv.Quote(values[0]) + "]"
	}
	var builder strings.Builder
	builder.WriteString("[\n")
	for _, value := range values {
		builder.WriteString("  ")
		builder.WriteString(strconv.Quote(value))
		builder.WriteString(",\n")
	}
	builder.WriteString("]")
	return builder.String()
}

func patchTOMLArray(text, section, key string, additions []string) (string, error) {
	lines := splitLines(text)
	sectionStart, sectionEnd := findTableRange(lines, section)
	if sectionStart < 0 {
		var builder strings.Builder
		builder.WriteString(text)
		if text != "" && !strings.HasSuffix(text, "\n") {
			builder.WriteByte('\n')
		}
		if strings.TrimSpace(text) != "" && !strings.HasSuffix(text, "\n\n") {
			builder.WriteByte('\n')
		}
		builder.WriteString("[")
		builder.WriteString(section)
		builder.WriteString("]\n")
		builder.WriteString(key)
		builder.WriteString(" = ")
		builder.WriteString(formatArray(additions))
		builder.WriteByte('\n')
		return builder.String(), nil
	}

	keyLine := findKeyLine(lines, sectionStart+1, sectionEnd, key)
	if keyLine < 0 {
		insertAt := sectionEnd
		for insertAt > sectionStart+1 && strings.TrimSpace(lines[insertAt-1]) == "" {
			insertAt--
		}
		assignment := key + " = " + formatArray(additions) + "\n"
		if insertAt > 0 && !strings.HasSuffix(lines[insertAt-1], "\n") {
			lines[insertAt-1] += "\n"
		}
		updated := make([]string, 0, len(lines)+1)
		updated = append(updated, lines[:insertAt]...)
		updated = append(updated, assignment)
		updated = append(updated, lines[insertAt:]...)
		return strings.Join(updated, ""), nil
	}

	startOffset := lineOffset(lines, keyLine)
	openBracket, closeBracket, ok := findArrayBounds(text, startOffset)
	if !ok {
		return "", fmt.Errorf("cannot locate array bounds for %s", key)
	}
	inside := text[openBracket+1 : closeBracket]
	multiline := strings.Contains(inside, "\n")
	trimmedInside := strings.TrimSpace(stripTOMLComments(inside))
	var insertion strings.Builder
	if multiline {
		indent := detectArrayIndent(inside)
		beforeClose := text[:closeBracket]
		lastNewline := strings.LastIndex(beforeClose, "\n")
		prefixOnCloseLine := beforeClose[lastNewline+1:]
		body := beforeClose[:lastNewline+1]
		if trimmedInside != "" && !endsWithCommaOutsideComment(inside) {
			body = appendCommaToLastElement(body, openBracket+1)
		}
		for _, value := range additions {
			insertion.WriteString(indent)
			insertion.WriteString(strconv.Quote(value))
			insertion.WriteString(",\n")
		}
		return body + insertion.String() + prefixOnCloseLine + text[closeBracket:], nil
	}

	quoted := make([]string, 0, len(additions))
	for _, value := range additions {
		quoted = append(quoted, strconv.Quote(value))
	}
	if trimmedInside == "" {
		return text[:openBracket+1] + strings.Join(quoted, ", ") + text[closeBracket:], nil
	}
	prefix := strings.TrimRight(inside, " \t")
	suffix := inside[len(prefix):]
	separator := ", "
	if strings.HasSuffix(prefix, ",") {
		separator = " "
	}
	return text[:openBracket+1] + prefix + separator + strings.Join(quoted, ", ") + suffix + text[closeBracket:], nil
}

func splitLines(text string) []string {
	if text == "" {
		return nil
	}
	var lines []string
	start := 0
	for index := 0; index < len(text); index++ {
		if text[index] == '\n' {
			lines = append(lines, text[start:index+1])
			start = index + 1
		}
	}
	if start < len(text) {
		lines = append(lines, text[start:])
	}
	return lines
}

func lineOffset(lines []string, target int) int {
	offset := 0
	for index := 0; index < target && index < len(lines); index++ {
		offset += len(lines[index])
	}
	return offset
}

func findTableRange(lines []string, wanted string) (start, end int) {
	start = -1
	end = len(lines)
	for index, line := range lines {
		header, ok := parseTableHeader(line)
		if !ok {
			continue
		}
		if start >= 0 {
			end = index
			break
		}
		if header == wanted {
			start = index
		}
	}
	return start, end
}

func parseTableHeader(line string) (string, bool) {
	trimmed := strings.TrimSpace(stripLineComment(line))
	if !strings.HasPrefix(trimmed, "[") || strings.HasPrefix(trimmed, "[[") ||
		!strings.HasSuffix(trimmed, "]") {
		return "", false
	}
	return strings.TrimSpace(trimmed[1 : len(trimmed)-1]), true
}

func findKeyLine(lines []string, start, end int, key string) int {
	for index := start; index < end; index++ {
		trimmed := strings.TrimSpace(lines[index])
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		left, _, found := strings.Cut(trimmed, "=")
		if !found {
			continue
		}
		if strings.TrimSpace(left) == key {
			return index
		}
	}
	return -1
}

func findArrayBounds(text string, from int) (open, close int, ok bool) {
	equals := strings.IndexByte(text[from:], '=')
	if equals < 0 {
		return 0, 0, false
	}
	index := from + equals + 1
	for index < len(text) && (text[index] == ' ' || text[index] == '\t') {
		index++
	}
	if index >= len(text) || text[index] != '[' {
		return 0, 0, false
	}
	open = index
	depth := 0
	inBasic := false
	inLiteral := false
	escaped := false
	inComment := false
	for index < len(text) {
		ch := text[index]
		switch {
		case inComment:
			if ch == '\n' {
				inComment = false
			}
		case inBasic:
			if escaped {
				escaped = false
			} else if ch == '\\' {
				escaped = true
			} else if ch == '"' {
				inBasic = false
			}
		case inLiteral:
			if ch == '\'' {
				inLiteral = false
			}
		default:
			switch ch {
			case '#':
				inComment = true
			case '"':
				inBasic = true
			case '\'':
				inLiteral = true
			case '[':
				depth++
			case ']':
				depth--
				if depth == 0 {
					return open, index, true
				}
			}
		}
		index++
	}
	return 0, 0, false
}

func stripLineComment(line string) string {
	inBasic := false
	inLiteral := false
	escaped := false
	for index := 0; index < len(line); index++ {
		ch := line[index]
		switch {
		case inBasic:
			if escaped {
				escaped = false
			} else if ch == '\\' {
				escaped = true
			} else if ch == '"' {
				inBasic = false
			}
		case inLiteral:
			if ch == '\'' {
				inLiteral = false
			}
		default:
			if ch == '"' {
				inBasic = true
			} else if ch == '\'' {
				inLiteral = true
			} else if ch == '#' {
				return line[:index]
			}
		}
	}
	return line
}

func stripTOMLComments(text string) string {
	var builder strings.Builder
	for _, line := range splitLines(text) {
		builder.WriteString(stripLineComment(line))
	}
	return builder.String()
}

func detectArrayIndent(inside string) string {
	for _, line := range splitLines(inside) {
		stripped := strings.TrimSpace(line)
		if stripped == "" || strings.HasPrefix(stripped, "#") {
			continue
		}
		spaces := len(line) - len(strings.TrimLeft(line, " \t"))
		if spaces > 0 {
			return line[:spaces]
		}
	}
	return "  "
}

func endsWithCommaOutsideComment(inside string) bool {
	trimmed := strings.TrimSpace(stripTOMLComments(inside))
	return strings.HasSuffix(trimmed, ",")
}

func appendCommaToLastElement(body string, minOffset int) string {
	lines := splitLines(body)
	offset := len(body)
	for index := len(lines) - 1; index >= 0; index-- {
		line := lines[index]
		offset -= len(line)
		if offset+len(line) <= minOffset {
			break
		}
		code := stripLineComment(line)
		trimmed := strings.TrimRight(code, " \t\r\n")
		if trimmed == "" {
			continue
		}
		commentAndSpace := line[len(trimmed):]
		lines[index] = trimmed + "," + commentAndSpace
		return strings.Join(lines, "")
	}
	return body
}
