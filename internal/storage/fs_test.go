package storage

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

func TestFSCreateIsImmutable(t *testing.T) {
	ctx := context.Background()
	s, err := NewFS(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	if err := s.Create(ctx, "a.md", strings.NewReader("first")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	err = s.Create(ctx, "a.md", strings.NewReader("second"))
	if !errors.Is(err, ErrExists) {
		t.Fatalf("second Create error = %v, want ErrExists", err)
	}

	rc, err := s.Open(ctx, "a.md")
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if string(got) != "first" {
		t.Fatalf("content = %q, want %q", got, "first")
	}
}

func TestFSConcurrentCreateOnlyOneWins(t *testing.T) {
	ctx := context.Background()
	s, err := NewFS(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const n = 20
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = s.Create(ctx, "race.md", strings.NewReader("x"))
		}()
	}
	wg.Wait()
	wins := 0
	for _, err := range errs {
		switch {
		case err == nil:
			wins++
		case !errors.Is(err, ErrExists):
			t.Errorf("unexpected error: %v", err)
		}
	}
	if wins != 1 {
		t.Fatalf("%d creates succeeded, want exactly 1", wins)
	}
}

func TestFSExistsListAndTemps(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := NewFS(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"b.md", "a.png"} {
		if err := s.Create(ctx, name, strings.NewReader(name)); err != nil {
			t.Fatal(err)
		}
	}
	// A leftover temp file and a subdirectory must not be listed.
	os.WriteFile(filepath.Join(dir, tempPrefix+"junk"), []byte("x"), 0o600)
	os.Mkdir(filepath.Join(dir, "sub"), 0o700)

	names, err := s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"a.png", "b.md"}; !slices.Equal(names, want) {
		t.Fatalf("List = %v, want %v", names, want)
	}
	if ok, _ := s.Exists(ctx, "b.md"); !ok {
		t.Error("Exists(b.md) = false")
	}
	if ok, _ := s.Exists(ctx, "missing.md"); ok {
		t.Error("Exists(missing.md) = true")
	}
	if _, err := s.Open(ctx, "missing.md"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Open(missing) error = %v, want ErrNotFound", err)
	}
}

func TestFSRejectsInvalidNames(t *testing.T) {
	ctx := context.Background()
	s, err := NewFS(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"", ".", "..", "../x", "a/b", `a\b`, ".hidden", "a\x00b", strings.Repeat("x", 256)} {
		if err := s.Create(ctx, name, strings.NewReader("x")); !errors.Is(err, ErrInvalidName) {
			t.Errorf("Create(%q) error = %v, want ErrInvalidName", name, err)
		}
		if _, err := s.Open(ctx, name); !errors.Is(err, ErrInvalidName) {
			t.Errorf("Open(%q) error = %v, want ErrInvalidName", name, err)
		}
	}
}
