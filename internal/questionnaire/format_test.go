package questionnaire

import (
	"maps"
	"strings"
	"testing"
	"time"
)

func TestRenderParseRoundTrip(t *testing.T) {
	questions := []Question{
		{ID: "q1", Prompt: "What do you\nthink?"},
		{ID: "q-2 <weird> id", Prompt: "## Tricky --> prompt"},
		{ID: "q3", Prompt: "Unanswered"},
	}
	meta := Meta{PUID: "prod/1 -->", Submitter: "alice@example.com\"", Timestamp: FormatTimestamp(time.Now())}
	answers := map[string]string{
		"q1":             NormalizeAnswer("**bold** and *italic*\n\n- a\n- b\n\n<!--question-id:cTM=-->\n## fake"),
		"q-2 <weird> id": NormalizeAnswer("![](img.png)"),
	}

	data, err := Render(meta, questions, answers)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "\n## What do you think?\n") {
		t.Errorf("prompt header missing or not single-line:\n%s", text)
	}

	gotMeta, gotAnswers, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if gotMeta != meta {
		t.Errorf("meta = %+v, want %+v", gotMeta, meta)
	}
	want := map[string]string{"q1": answers["q1"], "q-2 <weird> id": answers["q-2 <weird> id"], "q3": ""}
	if !maps.Equal(gotAnswers, want) {
		t.Errorf("answers = %#v\nwant %#v", gotAnswers, want)
	}

	readMeta, err := ReadMeta(strings.NewReader(text))
	if err != nil || readMeta != meta {
		t.Errorf("ReadMeta = %+v, %v", readMeta, err)
	}
}

func TestParseRejectsNonQuestionnaire(t *testing.T) {
	for _, in := range []string{"", "# notes", "<!--questionnaire-meta:!!!-->", "<!--questionnaire-meta:e30=-->"} {
		if _, _, err := Parse([]byte(in)); err == nil {
			t.Errorf("Parse(%q) succeeded", in)
		}
	}
}

func TestTimestampFormat(t *testing.T) {
	ts := time.Date(2026, 9, 16, 7, 5, 3, 120, time.FixedZone("x", 3600))
	s := FormatTimestamp(ts)
	if s != "20260916T060503.000000120Z" {
		t.Fatalf("FormatTimestamp = %q", s)
	}
	back, err := ParseTimestamp(s)
	if err != nil || !back.Equal(ts) {
		t.Fatalf("ParseTimestamp = %v, %v", back, err)
	}
}

func TestSanitizeNamePart(t *testing.T) {
	tests := map[string]string{
		"abc-DEF_1.2":            "abc-DEF_1.2",
		"a/b c":                  "a_b_c",
		"..":                     "_..",
		"":                       "_",
		"héllo":                  "h_llo",
		strings.Repeat("x", 200): strings.Repeat("x", maxNamePart),
	}
	for in, want := range tests {
		if got := sanitizeNamePart(in); got != want {
			t.Errorf("sanitizeNamePart(%q) = %q, want %q", in, got, want)
		}
	}
}
