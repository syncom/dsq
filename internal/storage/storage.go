// Package storage defines the flat, immutable object store used for
// questionnaire files and images, plus a local-filesystem implementation.
package storage

import (
	"context"
	"errors"
	"io"
)

var (
	// ErrExists is returned by Create when an object with the name already exists.
	ErrExists = errors.New("storage: object already exists")
	// ErrNotFound is returned when an object does not exist.
	ErrNotFound = errors.New("storage: object not found")
	// ErrInvalidName is returned for names that are not valid flat object names.
	ErrInvalidName = errors.New("storage: invalid object name")
)

// Store is a flat namespace of immutable objects. Implementations must
// guarantee that Create never overwrites an existing object.
type Store interface {
	// Create stores the contents of r under name. It fails with ErrExists if
	// the name is already taken; a partially written object is never visible.
	Create(ctx context.Context, name string, r io.Reader) error
	// Open returns the contents of the named object, or ErrNotFound.
	Open(ctx context.Context, name string) (io.ReadCloser, error)
	// Exists reports whether the named object exists.
	Exists(ctx context.Context, name string) (bool, error)
	// List returns the names of all objects, sorted.
	List(ctx context.Context) ([]string, error)
}

// ValidName reports whether name is usable as a flat object name: non-empty,
// at most 255 bytes, no path separators or control characters, and not
// starting with a dot (dot-prefixed names are reserved for temporary files).
func ValidName(name string) bool {
	if name == "" || len(name) > 255 || name[0] == '.' {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c == '/' || c == '\\' || c < 0x20 || c == 0x7f {
			return false
		}
	}
	return true
}
