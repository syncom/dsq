package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const tempPrefix = ".tmp-"

// FS is a Store backed by a single local directory.
//
// Immutability is enforced by writing each object to a temporary file and
// hard-linking it into place; link(2) fails if the destination exists, so
// an existing object is never overwritten, even across processes.
type FS struct {
	dir string
}

var _ Store = (*FS)(nil)

// NewFS returns a store rooted at dir, creating the directory if needed.
// Temporary files older than an hour (left behind by a crash) are removed.
func NewFS(dir string) (*FS, error) {
	if dir == "" {
		return nil, errors.New("storage: directory not set")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("storage: resolve %q: %w", dir, err)
	}
	if err := os.MkdirAll(abs, 0o750); err != nil {
		return nil, fmt.Errorf("storage: create %q: %w", abs, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("storage: stat %q: %w", abs, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("storage: %q is not a directory", abs)
	}
	s := &FS{dir: abs}
	s.removeStaleTemps(time.Hour)
	return s, nil
}

// Dir returns the absolute storage directory.
func (s *FS) Dir() string { return s.dir }

func (s *FS) Create(ctx context.Context, name string, r io.Reader) error {
	if !ValidName(name) {
		return fmt.Errorf("%w: %q", ErrInvalidName, name)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.dir, tempPrefix+"*")
	if err != nil {
		return fmt.Errorf("storage: create temp file: %w", err)
	}
	tmpName := tmp.Name()
	// Once linked, the final name keeps the data alive; the temp name always goes.
	defer os.Remove(tmpName)

	if _, err := io.Copy(tmp, &ctxReader{ctx: ctx, r: r}); err != nil {
		tmp.Close()
		return fmt.Errorf("storage: write %s: %w", name, err)
	}
	if err := tmp.Chmod(0o640); err != nil {
		tmp.Close()
		return fmt.Errorf("storage: chmod %s: %w", name, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("storage: sync %s: %w", name, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("storage: close %s: %w", name, err)
	}
	if err := os.Link(tmpName, filepath.Join(s.dir, name)); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("%w: %s", ErrExists, name)
		}
		return fmt.Errorf("storage: link %s: %w", name, err)
	}
	syncDir(s.dir)
	return nil
}

func (s *FS) Open(ctx context.Context, name string) (io.ReadCloser, error) {
	if !ValidName(name) {
		return nil, fmt.Errorf("%w: %q", ErrInvalidName, name)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := os.Open(filepath.Join(s.dir, name))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, name)
		}
		return nil, fmt.Errorf("storage: open %s: %w", name, err)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("storage: stat %s: %w", name, err)
	}
	if !info.Mode().IsRegular() {
		f.Close()
		return nil, fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	return f, nil
}

func (s *FS) Exists(ctx context.Context, name string) (bool, error) {
	if !ValidName(name) {
		return false, fmt.Errorf("%w: %q", ErrInvalidName, name)
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	info, err := os.Lstat(filepath.Join(s.dir, name))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("storage: stat %s: %w", name, err)
	}
	return info.Mode().IsRegular(), nil
}

func (s *FS) List(ctx context.Context) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("storage: list: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.Type().IsRegular() && ValidName(e.Name()) {
			names = append(names, e.Name())
		}
	}
	return names, nil
}

func (s *FS) removeStaleTemps(maxAge time.Duration) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-maxAge)
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), tempPrefix) {
			continue
		}
		if info, err := e.Info(); err == nil && info.ModTime().Before(cutoff) {
			os.Remove(filepath.Join(s.dir, e.Name()))
		}
	}
}

// syncDir makes a new directory entry durable. Best-effort: not all
// platforms support fsync on directories.
func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
}

type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
