package questionnaire

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Meta is the exact identity of a saved questionnaire version, embedded at
// the top of its Markdown file.
type Meta struct {
	PUID      string `json:"puid"`
	Submitter string `json:"submitter"`
	Timestamp string `json:"timestamp"`
}

const (
	metaPrefix     = "<!--questionnaire-meta:"
	questionPrefix = "<!--question-id:"
	commentSuffix  = "-->"
	maxMetaLine    = 64 << 10
)

// ErrNotQuestionnaire is returned when a file lacks a valid metadata header.
var ErrNotQuestionnaire = errors.New("not a questionnaire file")

// Render produces the Markdown file for one version. Answers must already be
// normalized with NormalizeAnswer.
func Render(meta Meta, questions []Question, answers map[string]string) ([]byte, error) {
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	b.WriteString(metaPrefix + base64.StdEncoding.EncodeToString(metaJSON) + commentSuffix + "\n")
	for _, q := range questions {
		b.WriteString("\n")
		b.WriteString(questionPrefix + base64.StdEncoding.EncodeToString([]byte(q.ID)) + commentSuffix + "\n")
		b.WriteString("## " + strings.Join(strings.Fields(q.Prompt), " ") + "\n")
		if a := answers[q.ID]; a != "" {
			b.WriteString("\n" + a + "\n")
		}
	}
	return b.Bytes(), nil
}

// Parse reads a file produced by Render. Answers are keyed by the question
// ID markers; the human-readable headers are ignored.
func Parse(data []byte) (Meta, map[string]string, error) {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	meta, err := parseMetaLine(lines[0])
	if err != nil {
		return Meta{}, nil, err
	}

	answers := make(map[string]string)
	var (
		curID      string
		cur        []string
		inQuestion bool
		skipHeader bool
	)
	flush := func() {
		if _, dup := answers[curID]; inQuestion && !dup {
			answers[curID] = trimBlankLines(cur)
		}
	}
	for _, line := range lines[1:] {
		if id, ok := parseQuestionMarker(line); ok {
			flush()
			curID, cur, inQuestion, skipHeader = id, nil, true, true
			continue
		}
		if skipHeader {
			skipHeader = false
			if line == "##" || strings.HasPrefix(line, "## ") {
				continue
			}
		}
		if inQuestion {
			cur = append(cur, line)
		}
	}
	flush()
	return meta, answers, nil
}

// ReadMeta reads only the metadata header from the start of r.
func ReadMeta(r io.Reader) (Meta, error) {
	br := bufio.NewReader(io.LimitReader(r, maxMetaLine))
	line, err := br.ReadString('\n')
	if err != nil && err != io.EOF {
		return Meta{}, err
	}
	return parseMetaLine(strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r"))
}

func parseMetaLine(line string) (Meta, error) {
	payload, ok := commentPayload(line, metaPrefix)
	if !ok {
		return Meta{}, ErrNotQuestionnaire
	}
	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return Meta{}, fmt.Errorf("%w: bad metadata encoding: %v", ErrNotQuestionnaire, err)
	}
	var meta Meta
	if err := json.Unmarshal(raw, &meta); err != nil {
		return Meta{}, fmt.Errorf("%w: bad metadata: %v", ErrNotQuestionnaire, err)
	}
	if meta.PUID == "" || meta.Submitter == "" {
		return Meta{}, fmt.Errorf("%w: incomplete metadata", ErrNotQuestionnaire)
	}
	if _, err := ParseTimestamp(meta.Timestamp); err != nil {
		return Meta{}, fmt.Errorf("%w: bad timestamp %q", ErrNotQuestionnaire, meta.Timestamp)
	}
	return meta, nil
}

func parseQuestionMarker(line string) (string, bool) {
	payload, ok := commentPayload(line, questionPrefix)
	if !ok {
		return "", false
	}
	id, err := base64.StdEncoding.DecodeString(payload)
	if err != nil || len(id) == 0 {
		return "", false
	}
	return string(id), true
}

func commentPayload(line, prefix string) (string, bool) {
	rest, ok := strings.CutPrefix(line, prefix)
	if !ok {
		return "", false
	}
	return strings.CutSuffix(rest, commentSuffix)
}
