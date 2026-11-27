package mailtemplates

import (
	"fmt"
	"html"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// segmentKind tells what a piece of a parsed message is.
type segmentKind int

const (
	segmentText segmentKind = iota
	segmentField
	segmentStrongOpen
	segmentStrongClose
)

// segment is one piece of a parsed message: literal text, a {{.Field}} or an emphasis marker.
type segment struct {
	kind segmentKind
	text string // the literal text, or the field name
}

// message is a translator string parsed at Load: literal text, {{.Field}} interpolations and
// *emphasis*. Translators never write HTML, so nothing else is accepted.
type message struct {
	segments []segment
	fields   []string
}

// fieldActionRgx matches the only template action a string may hold, {{.Field}} (an exported name).
var fieldActionRgx = regexp.MustCompile(`^\{\{\s*\.([A-Z][A-Za-z0-9]*)\s*\}\}`)

// parseMessage parses a translator string. A literal asterisk is written \*; an unmatched or
// empty *emphasis*, any other escape, any other {{action}} and any < or > fail.
func parseMessage(s string) (*message, error) {
	m := &message{}
	var text strings.Builder
	flush := func() {
		if text.Len() > 0 {
			m.segments = append(m.segments, segment{kind: segmentText, text: text.String()})
			text.Reset()
		}
	}
	strong := false
	for i := 0; i < len(s); {
		switch {
		case strings.HasPrefix(s[i:], "{{"):
			match := fieldActionRgx.FindStringSubmatch(s[i:])
			if match == nil {
				return nil, fmt.Errorf("only {{.Field}} is allowed, near %q", around(s, i))
			}
			flush()
			m.segments = append(m.segments, segment{kind: segmentField, text: match[1]})
			if !slices.Contains(m.fields, match[1]) {
				m.fields = append(m.fields, match[1])
			}
			i += len(match[0])
		case strings.HasPrefix(s[i:], "}}"):
			return nil, fmt.Errorf("stray }}, near %q", around(s, i))
		case s[i] == '\\':
			if i+1 >= len(s) || s[i+1] != '*' {
				return nil, fmt.Errorf("unknown escape (only \\* is allowed), near %q", around(s, i))
			}
			text.WriteString("*")
			i += 2
		case s[i] == '*':
			flush()
			if strong {
				if last := m.segments[len(m.segments)-1]; last.kind == segmentStrongOpen {
					return nil, fmt.Errorf("empty emphasis, near %q", around(s, i))
				}
				m.segments = append(m.segments, segment{kind: segmentStrongClose})
			} else {
				m.segments = append(m.segments, segment{kind: segmentStrongOpen})
			}
			strong = !strong
			i++
		case s[i] == '<' || s[i] == '>':
			return nil, fmt.Errorf("markup is not allowed, near %q", around(s, i))
		default:
			text.WriteString(s[i : i+1])
			i++
		}
	}
	if strong {
		return nil, fmt.Errorf("unmatched * in %q", s)
	}
	flush()
	return m, nil
}

// around returns the part of s near byte offset i, for error messages.
func around(s string, i int) string {
	end := min(len(s), i+20)
	for end < len(s) && !utf8.RuneStart(s[end]) {
		end++
	}
	return s[i:end]
}

// renderMode is how a message is written: as HTML (escaped, emphasis as <strong>) or as text.
type renderMode struct {
	escape      func(string) string
	strongOpen  string
	strongClose string
}

var (
	asHTML = renderMode{escape: html.EscapeString, strongOpen: "<strong>", strongClose: "</strong>"}
	asText = renderMode{escape: func(s string) string { return s }}
)

// render writes the message with each field replaced by value(field), escaped as the mode says.
func (m *message) render(mode renderMode, value func(field string) (string, error)) (string, error) {
	var out strings.Builder
	for _, seg := range m.segments {
		switch seg.kind {
		case segmentText:
			out.WriteString(mode.escape(seg.text))
		case segmentField:
			v, err := value(seg.text)
			if err != nil {
				return "", err
			}
			out.WriteString(mode.escape(v))
		case segmentStrongOpen:
			out.WriteString(mode.strongOpen)
		case segmentStrongClose:
			out.WriteString(mode.strongClose)
		default:
			return "", fmt.Errorf("unknown segment kind %d", seg.kind)
		}
	}
	return out.String(), nil
}

// renderFitted renders the message as text in at most limit characters, truncating the
// interpolated values (never the translated text) with "…", the longest first.
func (m *message) renderFitted(limit int, value func(field string) (string, error)) (string, error) {
	fixed := 0
	var values []string
	for _, seg := range m.segments {
		switch seg.kind {
		case segmentText:
			fixed += utf8.RuneCountInString(seg.text)
		case segmentField:
			v, err := value(seg.text)
			if err != nil {
				return "", err
			}
			values = append(values, v)
		default:
		}
	}
	values = fitValues(values, limit-fixed)
	var out strings.Builder
	next := 0
	for _, seg := range m.segments {
		switch seg.kind {
		case segmentText:
			out.WriteString(seg.text)
		case segmentField:
			out.WriteString(values[next])
			next++
		default:
		}
	}
	return out.String(), nil
}

// fitValues shortens values so that together they take at most budget characters: values shorter
// than an even share keep their length and leave the rest to the longer ones.
func fitValues(values []string, budget int) []string {
	total := 0
	for _, v := range values {
		total += utf8.RuneCountInString(v)
	}
	if total <= budget {
		return values
	}
	order := make([]int, len(values))
	for i := range order {
		order[i] = i
	}
	slices.SortFunc(order, func(a, b int) int {
		return utf8.RuneCountInString(values[a]) - utf8.RuneCountInString(values[b])
	})
	fitted := slices.Clone(values)
	remaining := max(budget, 0)
	for n, i := range order {
		share := remaining / (len(order) - n)
		fitted[i] = Truncate(values[i], share)
		remaining -= utf8.RuneCountInString(fitted[i])
	}
	return fitted
}

// Truncate returns s cut to at most n characters (runes), ending in "…" when it was cut.
func Truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	if n <= 0 {
		return ""
	}
	runes := []rune(s)
	return strings.TrimRight(string(runes[:n-1]), " ") + "…"
}
