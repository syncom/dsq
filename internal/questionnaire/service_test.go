package questionnaire

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"dsq/internal/storage"
)

var pngBytes = append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0}, 64)...)

func upload(data []byte) ImageUpload {
	return func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil }
}

func newTestService(t *testing.T) (*Service, *storage.FS) {
	t.Helper()
	store, err := storage.NewFS(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	svc, err := NewService(store, []Question{{ID: "q1", Prompt: "First"}, {ID: "q2", Prompt: "Second"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return svc, store
}

func TestSubmitAndEditFlow(t *testing.T) {
	ctx := context.Background()
	svc, store := newTestService(t)
	clock := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return clock }

	if doc, err := svc.Latest(ctx, "P1", "alice"); err != nil || doc != nil {
		t.Fatalf("Latest before submit = %v, %v", doc, err)
	}

	v1, err := svc.Submit(ctx, Submission{
		PUID: "P1", SubmitterID: "alice",
		Answers: map[string]string{"q1": "hi ![](pending:a) ![](pending:a)", "q2": "![](pending:b)"},
		Images:  map[string]ImageUpload{"a": upload(pngBytes), "b": upload(pngBytes), "unused": upload(pngBytes)},
	})
	if err != nil {
		t.Fatalf("Submit v1: %v", err)
	}
	base := "P1-alice-20260916T120000.000000000Z"
	if v1.Filename != base+".md" {
		t.Fatalf("filename = %q", v1.Filename)
	}
	names, _ := store.List(ctx)
	if want := []string{base + "-1.png", base + "-2.png", base + ".md"}; strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("stored files = %v, want %v", names, want)
	}

	doc, err := svc.Latest(ctx, "P1", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if got := doc.Answers["q1"]; got != "hi ![]("+base+"-1.png) ![]("+base+"-1.png)" {
		t.Fatalf("q1 = %q", got)
	}

	// Edit: the clock went backwards, an old image is reused and a new one added.
	clock = clock.Add(-time.Hour)
	v2, err := svc.Submit(ctx, Submission{
		PUID: "P1", SubmitterID: "alice",
		Answers: map[string]string{"q1": "edited ![](" + base + "-1.png) ![](pending:c)"},
		Images:  map[string]ImageUpload{"c": upload(pngBytes)},
	})
	if err != nil {
		t.Fatalf("Submit v2: %v", err)
	}
	if v2.Timestamp <= v1.Timestamp {
		t.Fatalf("v2 timestamp %s not after v1 %s", v2.Timestamp, v1.Timestamp)
	}
	doc, err = svc.Latest(ctx, "P1", "alice")
	if err != nil {
		t.Fatal(err)
	}
	wantQ1 := "edited ![](" + base + "-1.png) ![](" + strings.TrimSuffix(v2.Filename, ".md") + "-1.png)"
	if doc.Filename != v2.Filename || doc.Answers["q1"] != wantQ1 || doc.Answers["q2"] != "" {
		t.Fatalf("latest after edit = %+v", doc)
	}

	// The original file is untouched.
	rc, err := store.Open(ctx, v1.Filename)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(rc)
	rc.Close()
	if _, answers, _ := Parse(data); !strings.HasPrefix(answers["q1"], "hi ") {
		t.Fatalf("v1 changed: %q", answers["q1"])
	}

	// Another submitter and another PUID.
	if _, err := svc.Submit(ctx, Submission{PUID: "P1", SubmitterID: "bob", Answers: map[string]string{"q1": "bob"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Submit(ctx, Submission{PUID: "P2", SubmitterID: "alice", Answers: map[string]string{"q1": "x"}}); err != nil {
		t.Fatal(err)
	}
	versions, err := svc.Versions(ctx, "P1")
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 3 {
		t.Fatalf("versions = %+v", versions)
	}
	for i := 1; i < len(versions); i++ {
		if versions[i-1].Timestamp < versions[i].Timestamp {
			t.Fatalf("versions not newest first: %+v", versions)
		}
	}

	img, contentType, err := svc.OpenImage(ctx, base+"-2.png")
	if err != nil || contentType != "image/png" {
		t.Fatalf("OpenImage = %v, %v", contentType, err)
	}
	img.Close()
	for _, name := range []string{v1.Filename, "missing.png", "../x.png"} {
		if _, _, err := svc.OpenImage(ctx, name); !errors.Is(err, ErrNotFound) {
			t.Errorf("OpenImage(%q) error = %v, want ErrNotFound", name, err)
		}
	}
}

func TestSubmitIdentityIsExactDespiteSanitizedNames(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)
	clock := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return clock }

	// Both identities sanitize to "a_b-x" and are saved at the same instant.
	va, err := svc.Submit(ctx, Submission{PUID: "a/b", SubmitterID: "x", Answers: map[string]string{"q1": "slash"}})
	if err != nil {
		t.Fatal(err)
	}
	vb, err := svc.Submit(ctx, Submission{PUID: "a b", SubmitterID: "x", Answers: map[string]string{"q1": "space"}})
	if err != nil {
		t.Fatal(err)
	}
	if va.Filename == vb.Filename {
		t.Fatalf("filename collision not resolved: %s", va.Filename)
	}
	for puid, want := range map[string]string{"a/b": "slash", "a b": "space"} {
		doc, err := svc.Latest(ctx, puid, "x")
		if err != nil || doc == nil || doc.Answers["q1"] != want {
			t.Errorf("Latest(%q) = %+v, %v", puid, doc, err)
		}
		if vs, _ := svc.Versions(ctx, puid); len(vs) != 1 {
			t.Errorf("Versions(%q) = %+v", puid, vs)
		}
	}
}

func TestSubmitRejectsBadInput(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)
	if _, err := svc.Submit(ctx, Submission{PUID: "P", SubmitterID: "bob", Answers: map[string]string{"q1": "![](pending:b)"}, Images: map[string]ImageUpload{"b": upload(pngBytes)}}); err != nil {
		t.Fatal(err)
	}
	bobDoc, err := svc.Latest(ctx, "P", "bob")
	if err != nil {
		t.Fatal(err)
	}
	bobImage := strings.TrimSuffix(bobDoc.Filename, ".md") + "-1.png"

	tests := []struct {
		name string
		sub  Submission
	}{
		{"missing submitter", Submission{PUID: "P"}},
		{"missing puid", Submission{SubmitterID: "alice"}},
		{"control char", Submission{PUID: "P\n", SubmitterID: "alice"}},
		{"unknown question", Submission{PUID: "P", SubmitterID: "alice", Answers: map[string]string{"nope": "x"}}},
		{"missing upload", Submission{PUID: "P", SubmitterID: "alice", Answers: map[string]string{"q1": "![](pending:t)"}}},
		{"bad token", Submission{PUID: "P", SubmitterID: "alice", Answers: map[string]string{"q1": "![](pending:a.b)"}, Images: map[string]ImageUpload{"a.b": upload(pngBytes)}}},
		{"not an image", Submission{PUID: "P", SubmitterID: "alice", Answers: map[string]string{"q1": "![](pending:t)"}, Images: map[string]ImageUpload{"t": upload([]byte("<html>"))}}},
		{"external image", Submission{PUID: "P", SubmitterID: "alice", Answers: map[string]string{"q1": "![](http://evil/x.png)"}}},
		{"someone else's image", Submission{PUID: "P", SubmitterID: "alice", Answers: map[string]string{"q1": "![](" + bobImage + ")"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := svc.Submit(ctx, tt.sub)
			var inErr *InputError
			if !errors.As(err, &inErr) {
				t.Fatalf("error = %v, want InputError", err)
			}
		})
	}
	if doc, _ := svc.Latest(ctx, "P", "alice"); doc != nil {
		t.Fatalf("rejected submissions were saved: %+v", doc)
	}
}

func TestSubmitConcurrentSameKeyIsRejected(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)

	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	slow := func() (io.ReadCloser, error) {
		once.Do(func() { close(started); <-release })
		return io.NopCloser(bytes.NewReader(pngBytes)), nil
	}

	done := make(chan error, 1)
	go func() {
		_, err := svc.Submit(ctx, Submission{PUID: "P", SubmitterID: "alice",
			Answers: map[string]string{"q1": "![](pending:t)"}, Images: map[string]ImageUpload{"t": slow}})
		done <- err
	}()
	<-started

	if _, err := svc.Submit(ctx, Submission{PUID: "P", SubmitterID: "alice", Answers: map[string]string{"q1": "second"}}); !errors.Is(err, ErrBusy) {
		t.Fatalf("concurrent submit error = %v, want ErrBusy", err)
	}
	// A different key is not blocked.
	if _, err := svc.Submit(ctx, Submission{PUID: "P", SubmitterID: "bob", Answers: map[string]string{"q1": "bob"}}); err != nil {
		t.Fatalf("other submitter blocked: %v", err)
	}

	close(release)
	if err := <-done; err != nil {
		t.Fatalf("first submit: %v", err)
	}
	if _, err := svc.Submit(ctx, Submission{PUID: "P", SubmitterID: "alice", Answers: map[string]string{"q1": "third"}}); err != nil {
		t.Fatalf("submit after lock released: %v", err)
	}
	if len(svc.locks.held) != 0 {
		t.Fatalf("lock table not empty: %v", svc.locks.held)
	}
}
