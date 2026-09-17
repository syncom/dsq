package questionnaire

import (
	"strings"
	"time"
)

// TimestampLayout is fixed-width UTC with nanoseconds, so timestamps sort
// lexicographically in chronological order.
const TimestampLayout = "20060102T150405.000000000Z"

// FormatTimestamp formats t in TimestampLayout.
func FormatTimestamp(t time.Time) string { return t.UTC().Format(TimestampLayout) }

// ParseTimestamp parses a timestamp produced by FormatTimestamp.
func ParseTimestamp(s string) (time.Time, error) { return time.Parse(TimestampLayout, s) }

// maxNamePart bounds each identifier's share of a filename so the full name
// (two parts, timestamp, image suffix) stays well under 255 bytes.
const maxNamePart = 80

// basename returns the filename stem "{puid}-{submitter}-{timestamp}".
// Identifiers are sanitized and may collide; the exact values live in the
// file's metadata, which is what all lookups use.
func basename(puid, submitter, timestamp string) string {
	return sanitizeNamePart(puid) + "-" + sanitizeNamePart(submitter) + "-" + timestamp
}

func sanitizeNamePart(s string) string {
	var b strings.Builder
	for _, r := range s {
		if b.Len() >= maxNamePart {
			break
		}
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()
	if out == "" || out[0] == '.' {
		out = "_" + out
	}
	return out
}
