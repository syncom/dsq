package config

import "testing"

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestFromEnv(t *testing.T) {
	cfg, err := FromEnv(env(map[string]string{"QUESTIONS_FILE": "q.json", "STORAGE_DIR": "data"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr() != ":8080" || cfg.SubmitterHeader != "X-Submitter-Id" {
		t.Fatalf("defaults = %+v", cfg)
	}

	cfg, err = FromEnv(env(map[string]string{"QUESTIONS_FILE": "q.json", "STORAGE_DIR": "data", "PORT": "9000", "SUBMITTER_HEADER": "x-remote-user"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr() != ":9000" || cfg.SubmitterHeader != "X-Remote-User" {
		t.Fatalf("overrides = %+v", cfg)
	}

	for name, m := range map[string]map[string]string{
		"missing required": {},
		"bad port":         {"QUESTIONS_FILE": "q", "STORAGE_DIR": "d", "PORT": "http"},
		"bad header":       {"QUESTIONS_FILE": "q", "STORAGE_DIR": "d", "SUBMITTER_HEADER": "X Bad"},
	} {
		if _, err := FromEnv(env(m)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}
