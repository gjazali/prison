package config

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// ProjectPatch describes updates to apply to a project's `prison.toml`.
// Pointer fields replace the key when non-nil; slice `Add*` fields append
// entries that are not already present.
type ProjectPatch struct {
	Inmates          *[]string
	Ports            *[]int
	Sudo             *bool
	AddShadow        []string
	AddSetupCommands []string
	AddIgnore        []string
	AddEgressHosts   []string
}

// ApplyProjectPatch applies a patch to existing `prison.toml` bytes (or renders
// a new file when existing is empty) while preserving unrelated keys and
// comments. The result is validated with LoadProject.
func ApplyProjectPatch(existing []byte, patch ProjectPatch) ([]byte, bool, error) {
	if len(strings.TrimSpace(string(existing))) == 0 {
		rendered := renderNewProjectConfig(patch)
		if rendered == "" {
			return existing, false, nil
		}
		if _, err := LoadProject("prison.toml", []byte(rendered)); err != nil {
			return nil, false, fmt.Errorf("generated prison.toml is not valid: %w", err)
		}
		return []byte(rendered), true, nil
	}

	parsed, err := LoadProject("prison.toml", existing)
	if err != nil {
		return nil, false, err
	}

	text := string(existing)
	changed := false

	if patch.Inmates != nil {
		want := dedupeStrings(*patch.Inmates)
		if parsed.Prison.Inmates == nil || !slices.Equal(*parsed.Prison.Inmates, want) {
			updated, err := setTOMLValue(text, "prison", "inmates", formatStringArray(want))
			if err != nil {
				return nil, false, fmt.Errorf("cannot update [prison] inmates: %w", err)
			}
			text = updated
			changed = true
		}
	}
	if patch.Ports != nil {
		want := dedupeInts(*patch.Ports)
		if parsed.Box.Ports == nil || !slices.Equal(*parsed.Box.Ports, want) {
			updated, err := setTOMLValue(text, "box", "ports", formatIntArray(want))
			if err != nil {
				return nil, false, fmt.Errorf("cannot update [box] ports: %w", err)
			}
			text = updated
			changed = true
		}
	}
	if patch.Sudo != nil {
		if parsed.Box.Sudo == nil || *parsed.Box.Sudo != *patch.Sudo {
			updated, err := setTOMLValue(text, "box", "sudo", strconv.FormatBool(*patch.Sudo))
			if err != nil {
				return nil, false, fmt.Errorf("cannot update [box] sudo: %w", err)
			}
			text = updated
			changed = true
		}
	}

	additions := []struct {
		section string
		key     string
		values  []string
	}{
		{"box", "shadow", missingStrings(patch.AddShadow, parsed.Box.Shadow)},
		{"setup", "commands", missingStrings(patch.AddSetupCommands, parsed.Setup.Commands)},
		{"checkpoint", "ignore", missingStrings(patch.AddIgnore, parsed.Checkpoint.Ignore)},
		{"egress", "hosts", missingStrings(patch.AddEgressHosts, parsed.Egress.Hosts)},
	}
	for _, item := range additions {
		if len(item.values) == 0 {
			continue
		}
		updated, err := appendTOMLStringArray(text, item.section, item.key, item.values)
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
	if _, err := LoadProject("prison.toml", []byte(text)); err != nil {
		return nil, false, fmt.Errorf("updated prison.toml is not valid: %w", err)
	}
	return []byte(text), true, nil
}

func renderNewProjectConfig(patch ProjectPatch) string {
	var sections []string

	if patch.Inmates != nil {
		sections = append(sections,
			"[prison]\ninmates = "+formatStringArray(dedupeStrings(*patch.Inmates)))
	}

	var boxLines []string
	if patch.Ports != nil {
		boxLines = append(boxLines, "ports = "+formatIntArray(dedupeInts(*patch.Ports)))
	}
	if patch.Sudo != nil {
		boxLines = append(boxLines, "sudo = "+strconv.FormatBool(*patch.Sudo))
	}
	if shadow := dedupeStrings(patch.AddShadow); len(shadow) > 0 {
		boxLines = append(boxLines, "shadow = "+formatStringArray(shadow))
	}
	if len(boxLines) > 0 {
		sections = append(sections, "[box]\n"+strings.Join(boxLines, "\n"))
	}

	if commands := dedupeStrings(patch.AddSetupCommands); len(commands) > 0 {
		sections = append(sections, "[setup]\ncommands = "+formatStringArray(commands))
	}
	if ignore := dedupeStrings(patch.AddIgnore); len(ignore) > 0 {
		sections = append(sections, "[checkpoint]\nignore = "+formatStringArray(ignore))
	}
	if hosts := dedupeStrings(patch.AddEgressHosts); len(hosts) > 0 {
		sections = append(sections, "[egress]\nhosts = "+formatStringArray(hosts))
	}

	if len(sections) == 0 {
		return ""
	}
	return strings.Join(sections, "\n\n") + "\n"
}

func dedupeStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}

func dedupeInts(values []int) []int {
	seen := make(map[int]bool, len(values))
	result := make([]int, 0, len(values))
	for _, value := range values {
		if seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}

func missingStrings(proposed, existing []string) []string {
	seen := make(map[string]bool, len(existing))
	for _, value := range existing {
		seen[value] = true
	}
	var missing []string
	for _, value := range proposed {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		missing = append(missing, value)
	}
	return missing
}

func formatStringArray(values []string) string {
	if len(values) == 0 {
		return "[]"
	}
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

func formatIntArray(values []int) string {
	if len(values) == 0 {
		return "[]"
	}
	parts := make([]string, len(values))
	for index, value := range values {
		parts[index] = strconv.Itoa(value)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func setTOMLValue(text, section, key, formatted string) (string, error) {
	lines := splitConfigLines(text)
	sectionStart, sectionEnd := findTOMLTableRange(lines, section)
	if sectionStart < 0 {
		return appendTOMLSection(text, section, key, formatted), nil
	}
	keyLine := findTOMLKeyLine(lines, sectionStart+1, sectionEnd, key)
	if keyLine < 0 {
		return insertTOMLKey(lines, sectionStart, sectionEnd, key, formatted), nil
	}
	startOffset := configLineOffset(lines, keyLine)
	endOffset, ok := findTOMLValueEnd(text, startOffset)
	if !ok {
		return "", fmt.Errorf("cannot locate value bounds for %s", key)
	}
	equals := strings.IndexByte(text[startOffset:endOffset], '=')
	if equals < 0 {
		return "", fmt.Errorf("cannot locate assignment for %s", key)
	}
	prefix := strings.TrimRight(text[startOffset:startOffset+equals+1], " \t")
	return text[:startOffset] + prefix + " " + formatted + text[endOffset:], nil
}

func appendTOMLStringArray(text, section, key string, additions []string) (string, error) {
	lines := splitConfigLines(text)
	sectionStart, sectionEnd := findTOMLTableRange(lines, section)
	if sectionStart < 0 {
		return appendTOMLSection(text, section, key, formatStringArray(additions)), nil
	}
	keyLine := findTOMLKeyLine(lines, sectionStart+1, sectionEnd, key)
	if keyLine < 0 {
		return insertTOMLKey(lines, sectionStart, sectionEnd, key, formatStringArray(additions)), nil
	}

	startOffset := configLineOffset(lines, keyLine)
	openBracket, closeBracket, ok := findTOMLArrayBounds(text, startOffset)
	if !ok {
		return "", fmt.Errorf("cannot locate array bounds for %s", key)
	}
	inside := text[openBracket+1 : closeBracket]
	multiline := strings.Contains(inside, "\n")
	trimmedInside := strings.TrimSpace(stripTOMLBlockComments(inside))
	if multiline {
		indent := detectTOMLArrayIndent(inside)
		beforeClose := text[:closeBracket]
		lastNewline := strings.LastIndex(beforeClose, "\n")
		prefixOnCloseLine := beforeClose[lastNewline+1:]
		body := beforeClose[:lastNewline+1]
		if trimmedInside != "" && !strings.HasSuffix(trimmedInside, ",") {
			body = appendCommaToLastTOMLElement(body, openBracket+1)
		}
		var insertion strings.Builder
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

func appendTOMLSection(text, section, key, formatted string) string {
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
	builder.WriteString(formatted)
	builder.WriteByte('\n')
	return builder.String()
}

func insertTOMLKey(lines []string, sectionStart, sectionEnd int, key, formatted string) string {
	insertAt := sectionEnd
	for insertAt > sectionStart+1 && strings.TrimSpace(lines[insertAt-1]) == "" {
		insertAt--
	}
	assignment := key + " = " + formatted + "\n"
	if insertAt > 0 && !strings.HasSuffix(lines[insertAt-1], "\n") {
		lines[insertAt-1] += "\n"
	}
	updated := make([]string, 0, len(lines)+1)
	updated = append(updated, lines[:insertAt]...)
	updated = append(updated, assignment)
	updated = append(updated, lines[insertAt:]...)
	return strings.Join(updated, "")
}

func findTOMLValueEnd(text string, from int) (int, bool) {
	equals := strings.IndexByte(text[from:], '=')
	if equals < 0 {
		return 0, false
	}
	index := from + equals + 1
	for index < len(text) && (text[index] == ' ' || text[index] == '\t') {
		index++
	}
	if index >= len(text) {
		return index, true
	}
	if text[index] == '[' {
		_, closeBracket, ok := findTOMLArrayBounds(text, from)
		if !ok {
			return 0, false
		}
		return closeBracket + 1, true
	}
	// Scalar value: preserve any trailing comment on the line.
	inBasic := false
	inLiteral := false
	escaped := false
	end := index
	for index < len(text) && text[index] != '\n' && text[index] != '\r' {
		ch := text[index]
		switch {
		case inBasic:
			if escaped {
				escaped = false
			} else if ch == '\\' {
				escaped = true
			} else if ch == '"' {
				inBasic = false
			}
			end = index + 1
		case inLiteral:
			if ch == '\'' {
				inLiteral = false
			}
			end = index + 1
		default:
			if ch == '#' {
				return end, true
			}
			if ch == '"' {
				inBasic = true
			} else if ch == '\'' {
				inLiteral = true
			}
			if ch != ' ' && ch != '\t' {
				end = index + 1
			}
		}
		index++
	}
	return end, true
}

func splitConfigLines(text string) []string {
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

func configLineOffset(lines []string, target int) int {
	offset := 0
	for index := 0; index < target && index < len(lines); index++ {
		offset += len(lines[index])
	}
	return offset
}

func findTOMLTableRange(lines []string, wanted string) (start, end int) {
	start = -1
	end = len(lines)
	for index, line := range lines {
		header, ok := parseTOMLTableHeader(line)
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

func parseTOMLTableHeader(line string) (string, bool) {
	trimmed := strings.TrimSpace(stripTOMLLineComment(line))
	if !strings.HasPrefix(trimmed, "[") || strings.HasPrefix(trimmed, "[[") ||
		!strings.HasSuffix(trimmed, "]") {
		return "", false
	}
	return strings.TrimSpace(trimmed[1 : len(trimmed)-1]), true
}

func findTOMLKeyLine(lines []string, start, end int, key string) int {
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

func findTOMLArrayBounds(text string, from int) (open, close int, ok bool) {
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

func stripTOMLLineComment(line string) string {
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

func stripTOMLBlockComments(text string) string {
	var builder strings.Builder
	for _, line := range splitConfigLines(text) {
		builder.WriteString(stripTOMLLineComment(line))
	}
	return builder.String()
}

func detectTOMLArrayIndent(inside string) string {
	for _, line := range splitConfigLines(inside) {
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

func appendCommaToLastTOMLElement(body string, minOffset int) string {
	lines := splitConfigLines(body)
	offset := len(body)
	for index := len(lines) - 1; index >= 0; index-- {
		line := lines[index]
		offset -= len(line)
		if offset+len(line) <= minOffset {
			break
		}
		code := stripTOMLLineComment(line)
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
