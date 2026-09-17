package questionnaire

import (
	"regexp"
	"strings"
)

// NormalizeAnswer canonicalizes an answer's Markdown and neutralizes the
// constructs answers must not contain: headers (ATX and setext), thematic
// breaks, links, tables, blockquotes and raw HTML. Neutralizing is done by
// backslash-escaping, so the text is preserved.
//
// This is also what keeps the file format unambiguous: no answer line can
// start with "<", so none can be mistaken for a metadata or question marker.
func NormalizeAnswer(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.Map(func(r rune) rune {
		if (r < 0x20 && r != '\n' && r != '\t') || r == 0x7f {
			return -1
		}
		return r
	}, s)
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = neutralizeLine(line)
	}
	return trimBlankLines(lines)
}

var (
	// Leading list markers ("- ", "1. ", nested "- 1. ") are allowed and skipped
	// so that what follows them is checked like a line start.
	listPrefixRE = regexp.MustCompile(`^(?:[ \t]*(?:[-+*]|\d{1,9}[.)])(?:[ \t]+|$))*[ \t]*`)
	// Thematic breaks and setext underlines ("---", "* * *", "===").
	breakLineRE = regexp.MustCompile(`^[ \t]*(?:(?:-[ \t]*){3,}|(?:\*[ \t]*){3,}|(?:_[ \t]*){3,}|=+[ \t]*)$`)
)

func neutralizeLine(line string) string {
	if breakLineRE.MatchString(line) {
		trimmed := strings.TrimLeft(line, " \t")
		return line[:len(line)-len(trimmed)] + `\` + trimmed
	}
	prefix := listPrefixRE.FindString(line)
	rest := line[len(prefix):]

	var b strings.Builder
	b.Grow(len(line) + 8)
	b.WriteString(prefix)
	escaped := false
	if rest != "" && (rest[0] == '#' || rest[0] == '>') {
		b.WriteByte('\\')
		escaped = true
	}
	prevBang := false
	for i := 0; i < len(rest); i++ {
		c := rest[i]
		if escaped {
			b.WriteByte(c)
			escaped, prevBang = false, false
			continue
		}
		switch c {
		case '\\':
			escaped = true
		case '<', '|':
			b.WriteByte('\\')
		case '[':
			// "![" starts an image, which is allowed; any other "[" could start a link.
			if !prevBang {
				b.WriteByte('\\')
			}
		}
		b.WriteByte(c)
		prevBang = c == '!'
	}
	return b.String()
}

func trimBlankLines(lines []string) string {
	start, end := 0, len(lines)
	for start < end && strings.TrimSpace(lines[start]) == "" {
		start++
	}
	for end > start && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	return strings.Join(lines[start:end], "\n")
}

var imageRefRE = regexp.MustCompile(`!\[[^\]\n]*\]\(([^()\s]*)\)`)

// rewriteImageRefs calls fn for the source of every image reference
// "![alt](src)" in md and replaces the source with fn's result.
func rewriteImageRefs(md string, fn func(src string) (string, error)) (string, error) {
	var b strings.Builder
	last := 0
	for _, m := range imageRefRE.FindAllStringSubmatchIndex(md, -1) {
		if escapedAt(md, m[0]) {
			continue
		}
		repl, err := fn(md[m[2]:m[3]])
		if err != nil {
			return "", err
		}
		b.WriteString(md[last:m[2]])
		b.WriteString(repl)
		last = m[3]
	}
	b.WriteString(md[last:])
	return b.String(), nil
}

// imageRefs returns the sources of all image references in md, in order.
func imageRefs(md string) []string {
	var srcs []string
	rewriteImageRefs(md, func(src string) (string, error) {
		srcs = append(srcs, src)
		return src, nil
	})
	return srcs
}

// escapedAt reports whether the byte at i is preceded by an odd number of backslashes.
func escapedAt(s string, i int) bool {
	n := 0
	for j := i - 1; j >= 0 && s[j] == '\\'; j-- {
		n++
	}
	return n%2 == 1
}
