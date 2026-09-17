package questionnaire

import (
	"slices"
	"testing"
)

func TestNormalizeAnswer(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"plain", "hello **world**", "hello **world**"},
		{"crlf and blank edges", "\r\n\r\nline one\r\n\r\nline two\r\n\n", "line one\n\nline two"},
		{"atx header", "# Title", `\# Title`},
		{"header inside list item", "- ## sub", `- \## sub`},
		{"nested list markers", "1. - # x", `1. - \# x`},
		{"setext underline", "Title\n===", "Title\n\\==="},
		{"thematic break", "a\n\n---\n\n* * *", "a\n\n\\---\n\n\\* * *"},
		{"blockquote", "> quote", `\> quote`},
		{"html and marker injection", "<!--question-id:eA==-->", `\<!--question-id:eA==-->`},
		{"inline html", "a <b>x</b>", `a \<b>x\</b>`},
		{"link", "see [here](http://x)", `see \[here](http://x)`},
		{"image kept", "![](a.png) and ![alt](pending:t1)", "![](a.png) and ![alt](pending:t1)"},
		{"escaped bang is not an image", `\![](a.png)`, `\!\[](a.png)`},
		{"table", "| a | b |\n|---|---|", "\\| a \\| b \\|\n\\|---\\|---\\|"},
		{"already escaped is idempotent", `\# \< \[ \|`, `\# \< \[ \|`},
		{"control chars dropped", "a\x00b\x1bc", "abc"},
		{"list kept", "- one\n- two\n  1. nested", "- one\n- two\n  1. nested"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NormalizeAnswer(tt.in)
			if got != tt.want {
				t.Errorf("NormalizeAnswer(%q)\n got %q\nwant %q", tt.in, got, tt.want)
			}
			if again := NormalizeAnswer(got); again != got {
				t.Errorf("not idempotent: %q -> %q", got, again)
			}
		})
	}
}

func TestImageRefs(t *testing.T) {
	md := "a ![](one.png) b\n- ![x](pending:tok)\n\\![](escaped.png) \\\\![](real.png)"
	got := imageRefs(md)
	want := []string{"one.png", "pending:tok", "real.png"}
	if !slices.Equal(got, want) {
		t.Fatalf("imageRefs = %q, want %q", got, want)
	}
}
