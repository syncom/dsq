package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/syncom/dsq/internal/questionnaire"
	"github.com/syncom/dsq/internal/storage"
)

const header = "X-Remote-User"

var pngBytes = append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{1}, 32)...)

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	store, err := storage.NewFS(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc, err := questionnaire.NewService(store, []questionnaire.Question{
		{ID: "q1", Prompt: "One"}, {ID: "q2", Prompt: "Two"},
	}, logger)
	if err != nil {
		t.Fatal(err)
	}
	static := fstest.MapFS{"index.html": {Data: []byte("<h1>ui</h1>")}}
	srv := httptest.NewServer(New(svc, header, static, logger))
	t.Cleanup(srv.Close)
	return srv
}

func submit(t *testing.T, srv *httptest.Server, puid, submitter string, answers map[string]string, images map[string][]byte) *http.Response {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	answersJSON, _ := json.Marshal(answers)
	mw.WriteField("answers", string(answersJSON))
	for token, data := range images {
		fw, _ := mw.CreateFormFile("image:"+token, "blob")
		fw.Write(data)
	}
	mw.Close()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/questionnaires/"+url.PathEscape(puid), &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if submitter != "" {
		req.Header.Set(header, submitter)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func get(t *testing.T, srv *httptest.Server, path, submitter string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
	if submitter != "" {
		req.Header.Set(header, submitter)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func decode[T any](t *testing.T, resp *http.Response) T {
	t.Helper()
	var v T
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return v
}

func TestEndToEnd(t *testing.T) {
	srv := newTestServer(t)
	puid := "prod/42 ü"

	resp := get(t, srv, "/api/questions", "")
	if qs := decode[[]questionnaire.Question](t, resp); len(qs) != 2 || qs[0].ID != "q1" {
		t.Fatalf("questions = %+v", qs)
	}

	resp = get(t, srv, "/api/questionnaires/"+url.PathEscape(puid)+"/latest", "alice")
	if latest := decode[latestResponse](t, resp); resp.StatusCode != 200 || latest.Timestamp != "" || latest.Answers == nil || len(latest.Answers) != 0 {
		t.Fatalf("empty latest = %d %+v", resp.StatusCode, latest)
	}

	resp = submit(t, srv, puid, "alice", map[string]string{"q1": "**hi** ![](pending:img1)"}, map[string][]byte{"img1": pngBytes})
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("submit status = %d: %s", resp.StatusCode, body)
	}
	created := decode[versionResponse](t, resp)
	if created.PUID != puid || created.SubmitterID != "alice" || !strings.HasSuffix(created.Filename, ".md") {
		t.Fatalf("created = %+v", created)
	}

	resp = get(t, srv, "/api/questionnaires/"+url.PathEscape(puid)+"/latest", "alice")
	latest := decode[latestResponse](t, resp)
	imageName := strings.TrimSuffix(created.Filename, ".md") + "-1.png"
	if latest.Timestamp != created.Timestamp || latest.Answers["q1"] != "**hi** ![]("+imageName+")" {
		t.Fatalf("latest = %+v", latest)
	}

	resp = get(t, srv, "/api/files/"+imageName, "")
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/png" || !bytes.Equal(data, pngBytes) {
		t.Fatalf("image = %d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if resp := get(t, srv, "/api/files/"+created.Filename, ""); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("markdown served via files endpoint: %d", resp.StatusCode)
	}

	// Edit reusing the saved image.
	resp = submit(t, srv, puid, "alice", map[string]string{"q1": "edited ![](" + imageName + ")"}, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("edit status = %d", resp.StatusCode)
	}
	submit(t, srv, puid, "bob", map[string]string{"q2": "bob"}, nil)

	resp = get(t, srv, "/api/questionnaires/"+url.PathEscape(puid)+"/versions", "")
	versions := decode[[]versionResponse](t, resp)
	if len(versions) != 3 || versions[0].SubmitterID != "bob" || versions[2].Filename != created.Filename || versions[0].PUID != "" {
		t.Fatalf("versions = %+v", versions)
	}

	resp = get(t, srv, "/", "")
	if body, _ := io.ReadAll(resp.Body); resp.StatusCode != 200 || !strings.Contains(string(body), "ui") {
		t.Fatalf("static = %d %s", resp.StatusCode, body)
	}
}

func TestSubmitDuringUploadIsRejected(t *testing.T) {
	srv := newTestServer(t)

	// Start a submission whose body is still being uploaded.
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/questionnaires/P", pr)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set(header, "alice")
	first := make(chan *http.Response, 1)
	go func() {
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Error(err)
			resp = &http.Response{Body: http.NoBody}
		}
		first <- resp
	}()
	mw.WriteField("answers", `{"q1":"first"}`) // blocks until the server starts reading

	if resp := submit(t, srv, "P", "alice", map[string]string{"q1": "second"}, nil); resp.StatusCode != http.StatusConflict {
		t.Fatalf("concurrent submission status = %d, want 409", resp.StatusCode)
	}
	if resp := submit(t, srv, "P", "bob", map[string]string{"q1": "bob"}, nil); resp.StatusCode != http.StatusCreated {
		t.Fatalf("other submitter status = %d, want 201", resp.StatusCode)
	}

	mw.Close()
	pw.Close()
	resp := <-first
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("first submission status = %d, want 201", resp.StatusCode)
	}
	if resp := submit(t, srv, "P", "alice", map[string]string{"q1": "third"}, nil); resp.StatusCode != http.StatusCreated {
		t.Fatalf("submission after completion status = %d, want 201", resp.StatusCode)
	}
}

func TestSubmitErrors(t *testing.T) {
	srv := newTestServer(t)

	tests := []struct {
		name      string
		submitter string
		answers   map[string]string
		images    map[string][]byte
		want      int
	}{
		{"no submitter header", "", map[string]string{"q1": "x"}, nil, 400},
		{"unknown question", "alice", map[string]string{"zzz": "x"}, nil, 400},
		{"missing image", "alice", map[string]string{"q1": "![](pending:t)"}, nil, 400},
		{"not an image", "alice", map[string]string{"q1": "![](pending:t)"}, map[string][]byte{"t": []byte("hello")}, 400},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := submit(t, srv, "P", tt.submitter, tt.answers, tt.images)
			if resp.StatusCode != tt.want {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tt.want)
			}
			if e := decode[map[string]string](t, resp); e["error"] == "" {
				t.Fatalf("missing error message")
			}
		})
	}

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/questionnaires/P", strings.NewReader(`{"q1":"x"}`))
	req.Header.Set(header, "alice")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("json body status = %d", resp.StatusCode)
	}

	if resp := get(t, srv, "/api/nope", ""); resp.StatusCode != 404 {
		t.Fatalf("unknown api status = %d", resp.StatusCode)
	}
}
