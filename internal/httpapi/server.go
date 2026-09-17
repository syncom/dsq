// Package httpapi exposes the questionnaire service over HTTP and serves the web UI.
package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"dsq/internal/questionnaire"
)

const (
	maxRequestBytes = 64 << 20
	multipartMemory = 8 << 20
	imagePartPrefix = "image:"
)

type server struct {
	svc             *questionnaire.Service
	submitterHeader string
	logger          *slog.Logger
}

// New returns the HTTP handler. submitterHeader names the trusted header
// carrying the submitter identity; static holds the web UI.
func New(svc *questionnaire.Service, submitterHeader string, static fs.FS, logger *slog.Logger) http.Handler {
	s := &server{svc: svc, submitterHeader: submitterHeader, logger: logger}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/config", s.handleConfig)
	mux.HandleFunc("GET /api/questions", s.handleQuestions)
	mux.HandleFunc("POST /api/questionnaires/{puid}", s.handleSubmit)
	mux.HandleFunc("GET /api/questionnaires/{puid}/latest", s.handleLatest)
	mux.HandleFunc("GET /api/questionnaires/{puid}/versions", s.handleVersions)
	mux.HandleFunc("GET /api/files/{filename}", s.handleFile)
	mux.HandleFunc("GET /api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not found")
	})
	mux.Handle("GET /", staticHandler(static))
	return s.logRequests(securityHeaders(mux))
}

func (s *server) handleConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"submitterHeader": s.submitterHeader})
}

func (s *server) handleQuestions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.svc.Questions())
}

type versionResponse struct {
	PUID        string `json:"puid,omitempty"`
	SubmitterID string `json:"submitterId"`
	Timestamp   string `json:"timestamp"`
	Filename    string `json:"filename"`
}

func (s *server) handleSubmit(w http.ResponseWriter, r *http.Request) {
	// Reserve before reading the body, so a second submission during a slow
	// upload is rejected rather than queued behind it.
	res, err := s.svc.Reserve(r.PathValue("puid"), r.Header.Get(s.submitterHeader))
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	defer res.Release()

	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	if err := r.ParseMultipartForm(multipartMemory); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		writeError(w, http.StatusBadRequest, "expected a multipart/form-data body")
		return
	}
	defer r.MultipartForm.RemoveAll()
	form := r.MultipartForm

	if len(form.Value["answers"]) != 1 {
		writeError(w, http.StatusBadRequest, `expected exactly one "answers" field`)
		return
	}
	var answers map[string]string
	if err := json.Unmarshal([]byte(form.Value["answers"][0]), &answers); err != nil {
		writeError(w, http.StatusBadRequest, `"answers" must be a JSON object of question id to Markdown string`)
		return
	}
	for name := range form.Value {
		if name != "answers" {
			writeError(w, http.StatusBadRequest, "unexpected form field "+jsonQuote(name))
			return
		}
	}

	images := make(map[string]questionnaire.ImageUpload, len(form.File))
	for name, headers := range form.File {
		token, ok := strings.CutPrefix(name, imagePartPrefix)
		if !ok || len(headers) != 1 {
			writeError(w, http.StatusBadRequest, "unexpected file part "+jsonQuote(name))
			return
		}
		fh := headers[0]
		images[token] = func() (io.ReadCloser, error) { return openPart(fh) }
	}

	v, err := res.Submit(r.Context(), answers, images)
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, versionResponse{
		PUID: v.PUID, SubmitterID: v.SubmitterID, Timestamp: v.Timestamp, Filename: v.Filename,
	})
}

func openPart(fh *multipart.FileHeader) (io.ReadCloser, error) { return fh.Open() }

type latestResponse struct {
	SubmitterID string            `json:"submitterId"`
	Timestamp   string            `json:"timestamp"`
	Filename    string            `json:"filename,omitempty"`
	Answers     map[string]string `json:"answers"`
}

func (s *server) handleLatest(w http.ResponseWriter, r *http.Request) {
	submitter := r.Header.Get(s.submitterHeader)
	doc, err := s.svc.Latest(r.Context(), r.PathValue("puid"), submitter)
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if doc == nil {
		writeJSON(w, http.StatusOK, latestResponse{SubmitterID: submitter, Answers: map[string]string{}})
		return
	}
	writeJSON(w, http.StatusOK, latestResponse{SubmitterID: doc.SubmitterID, Timestamp: doc.Timestamp, Filename: doc.Filename, Answers: doc.Answers})
}

func (s *server) handleVersions(w http.ResponseWriter, r *http.Request) {
	versions, err := s.svc.Versions(r.Context(), r.PathValue("puid"))
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	out := make([]versionResponse, len(versions))
	for i, v := range versions {
		out[i] = versionResponse{SubmitterID: v.SubmitterID, Timestamp: v.Timestamp, Filename: v.Filename}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}

func (s *server) handleFile(w http.ResponseWriter, r *http.Request) {
	rc, contentType, err := s.svc.OpenImage(r.Context(), r.PathValue("filename"))
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	defer rc.Close()
	h := w.Header()
	h.Set("Content-Type", contentType)
	h.Set("Cache-Control", "private, max-age=31536000, immutable")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	if rs, ok := rc.(io.ReadSeeker); ok {
		http.ServeContent(w, r, "", time.Time{}, rs)
		return
	}
	io.Copy(w, rc)
}

func (s *server) writeServiceError(w http.ResponseWriter, r *http.Request, err error) {
	var inputErr *questionnaire.InputError
	switch {
	case errors.As(err, &inputErr):
		writeError(w, http.StatusBadRequest, inputErr.Error())
	case errors.Is(err, questionnaire.ErrBusy):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, questionnaire.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	default:
		s.logger.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
		writeError(w, http.StatusInternalServerError, "internal server error")
	}
}

func staticHandler(static fs.FS) http.Handler {
	files := http.FileServerFS(static)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; img-src 'self' blob:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'")
		files.ServeHTTP(w, r)
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func jsonQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.ResponseWriter.Write(b)
}

func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func (s *server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		s.logger.Info("request", "method", r.Method, "path", r.URL.Path,
			"status", rec.status, "duration", time.Since(start).Round(time.Microsecond))
	})
}
