// Package config reads deploy-time configuration from the environment.
package config

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// Config is the service configuration.
type Config struct {
	Port            int
	QuestionsFile   string
	StorageDir      string
	SubmitterHeader string
}

// Addr returns the listen address.
func (c Config) Addr() string { return ":" + strconv.Itoa(c.Port) }

// FromEnv builds a Config using getenv (normally os.Getenv).
func FromEnv(getenv func(string) string) (Config, error) {
	cfg := Config{
		Port:            8080,
		QuestionsFile:   getenv("QUESTIONS_FILE"),
		StorageDir:      getenv("STORAGE_DIR"),
		SubmitterHeader: "X-Submitter-Id",
	}
	var errs []error
	if v := getenv("PORT"); v != "" {
		port, err := strconv.Atoi(v)
		if err != nil || port < 1 || port > 65535 {
			errs = append(errs, fmt.Errorf("PORT: invalid port %q", v))
		}
		cfg.Port = port
	}
	if cfg.QuestionsFile == "" {
		errs = append(errs, errors.New("QUESTIONS_FILE is required"))
	}
	if cfg.StorageDir == "" {
		errs = append(errs, errors.New("STORAGE_DIR is required"))
	}
	if v := getenv("SUBMITTER_HEADER"); v != "" {
		if !validHeaderName(v) {
			errs = append(errs, fmt.Errorf("SUBMITTER_HEADER: invalid header name %q", v))
		}
		cfg.SubmitterHeader = v
	}
	cfg.SubmitterHeader = http.CanonicalHeaderKey(cfg.SubmitterHeader)
	return cfg, errors.Join(errs...)
}

func validHeaderName(s string) bool {
	return s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", r))
	}) < 0
}
