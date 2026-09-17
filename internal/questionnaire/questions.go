package questionnaire

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

// Question is one entry of the configured question list.
type Question struct {
	ID     string `json:"id"`
	Prompt string `json:"prompt"`
}

// LoadQuestions reads and validates a JSON question list from path.
func LoadQuestions(path string) ([]Question, error) {
	if path == "" {
		return nil, errors.New("questions file not set")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read questions file: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var questions []Question
	if err := dec.Decode(&questions); err != nil {
		return nil, fmt.Errorf("parse questions file %s: %w", path, err)
	}
	if err := validateQuestions(questions); err != nil {
		return nil, fmt.Errorf("questions file %s: %w", path, err)
	}
	return questions, nil
}

func validateQuestions(questions []Question) error {
	if len(questions) == 0 {
		return errors.New("no questions configured")
	}
	seen := make(map[string]bool, len(questions))
	for i, q := range questions {
		if q.ID == "" {
			return fmt.Errorf("question %d: empty id", i)
		}
		if seen[q.ID] {
			return fmt.Errorf("question %d: duplicate id %q", i, q.ID)
		}
		seen[q.ID] = true
		if strings.TrimSpace(q.Prompt) == "" {
			return fmt.Errorf("question %q: empty prompt", q.ID)
		}
	}
	return nil
}
