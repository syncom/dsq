// Package questionnaire implements saving, versioning and loading of
// questionnaire responses on top of a storage.Store.
package questionnaire

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"dsq/internal/storage"
)

var (
	// ErrBusy is returned when a submission for the same PUID and submitter is in flight.
	ErrBusy = errors.New("a submission for this questionnaire is already in progress")
	// ErrNotFound is returned for unknown files.
	ErrNotFound = errors.New("not found")
)

// InputError describes invalid client input.
type InputError struct{ msg string }

func (e *InputError) Error() string { return e.msg }

func inputErrorf(format string, args ...any) error {
	return &InputError{msg: fmt.Sprintf(format, args...)}
}

const (
	pendingPrefix  = "pending:"
	maxIDBytes     = 512
	maxFileBytes   = 16 << 20
	maxNameRetries = 16
)

var tokenRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

var (
	imageExtByType = map[string]string{
		"image/png":  ".png",
		"image/jpeg": ".jpg",
		"image/gif":  ".gif",
		"image/webp": ".webp",
	}
	imageTypeByExt = map[string]string{
		".png":  "image/png",
		".jpg":  "image/jpeg",
		".gif":  "image/gif",
		".webp": "image/webp",
	}
)

// Version identifies one saved questionnaire file.
type Version struct {
	PUID        string
	SubmitterID string
	Timestamp   string
	Filename    string
}

// Document is a saved version with its answers.
type Document struct {
	Version
	Answers map[string]string
}

// ImageUpload opens the contents of an uploaded image. It may be called
// more than once.
type ImageUpload func() (io.ReadCloser, error)

// Submission is a new version to be saved.
type Submission struct {
	PUID        string
	SubmitterID string
	// Answers maps question ID to Markdown. Images are referenced either as
	// "pending:TOKEN" (uploaded in Images) or by the filename of an image
	// saved with an earlier version of the same questionnaire.
	Answers map[string]string
	Images  map[string]ImageUpload
}

// Service implements the questionnaire business logic.
type Service struct {
	store     storage.Store
	questions []Question
	byID      map[string]Question
	logger    *slog.Logger
	locks     keyedLocks
	now       func() time.Time

	// Files are immutable, so metadata read from them can be cached forever.
	// A nil entry marks a file that is not a questionnaire.
	metaMu    sync.RWMutex
	metaCache map[string]*Meta
}

// NewService returns a service for the given question list.
func NewService(store storage.Store, questions []Question, logger *slog.Logger) (*Service, error) {
	if err := validateQuestions(questions); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	s := &Service{
		store:     store,
		questions: slices.Clone(questions),
		byID:      make(map[string]Question, len(questions)),
		logger:    logger,
		now:       time.Now,
		metaCache: make(map[string]*Meta),
	}
	for _, q := range questions {
		s.byID[q.ID] = q
	}
	return s, nil
}

// Questions returns the configured question list.
func (s *Service) Questions() []Question { return slices.Clone(s.questions) }

// Reservation is the exclusive right to submit one version for a PUID and
// submitter. It must be released.
type Reservation struct {
	svc       *Service
	puid      string
	submitter string
	unlock    func()
	once      sync.Once
}

// Reserve claims the (PUID, submitter) key without blocking. It returns
// ErrBusy if a submission for the key is already in progress, and an
// *InputError for invalid identifiers. Callers reserve before reading the
// request body, so a submission is "in flight" for its whole upload.
func (s *Service) Reserve(puid, submitter string) (*Reservation, error) {
	if err := validateIdentity(puid, submitter); err != nil {
		return nil, err
	}
	unlock, ok := s.locks.tryLock(lockKey{puid, submitter})
	if !ok {
		return nil, ErrBusy
	}
	return &Reservation{svc: s, puid: puid, submitter: submitter, unlock: unlock}, nil
}

// Release gives up the reservation. It is safe to call more than once.
func (r *Reservation) Release() { r.once.Do(r.unlock) }

// Submit saves a new immutable version using a reservation obtained with
// Reserve, then releases it.
func (s *Service) Submit(ctx context.Context, sub Submission) (Version, error) {
	res, err := s.Reserve(sub.PUID, sub.SubmitterID)
	if err != nil {
		return Version{}, err
	}
	defer res.Release()
	return res.Submit(ctx, sub.Answers, sub.Images)
}

// Submit saves a new immutable version for the reserved key. It returns an
// *InputError for invalid input.
func (r *Reservation) Submit(ctx context.Context, answers map[string]string, images map[string]ImageUpload) (Version, error) {
	return r.svc.submit(ctx, Submission{PUID: r.puid, SubmitterID: r.submitter, Answers: answers, Images: images})
}

// submit requires the lock for sub's key to be held.
func (s *Service) submit(ctx context.Context, sub Submission) (Version, error) {
	answers := make(map[string]string, len(sub.Answers))
	for id, md := range sub.Answers {
		if _, ok := s.byID[id]; !ok {
			return Version{}, inputErrorf("unknown question id %q", id)
		}
		answers[id] = NormalizeAnswer(md)
	}

	history, err := s.versions(ctx, func(m *Meta) bool {
		return m.PUID == sub.PUID && m.Submitter == sub.SubmitterID
	})
	if err != nil {
		return Version{}, err
	}
	pending, err := s.checkImageRefs(ctx, answers, sub.Images, history)
	if err != nil {
		return Version{}, err
	}
	exts := make(map[string]string, len(pending))
	for _, token := range pending {
		ext, err := sniffImage(sub.Images[token])
		if err != nil {
			return Version{}, err
		}
		exts[token] = ext
	}

	// A new version must sort after every earlier one, even if the clock stepped back.
	ts := s.now().UTC()
	if len(history) > 0 {
		if prev, err := ParseTimestamp(history[0].Timestamp); err == nil && !ts.After(prev) {
			ts = prev.Add(time.Nanosecond)
		}
	}
	// Different identities can sanitize to the same filename; on a collision
	// move to the next nanosecond.
	for range maxNameRetries {
		v, err := s.write(ctx, sub, answers, pending, exts, ts)
		if !errors.Is(err, storage.ErrExists) {
			return v, err
		}
		ts = ts.Add(time.Nanosecond)
	}
	return Version{}, errors.New("could not allocate a unique filename")
}

// checkImageRefs validates every image reference in answers and returns the
// referenced upload tokens in order of first appearance.
func (s *Service) checkImageRefs(ctx context.Context, answers map[string]string, uploads map[string]ImageUpload, history []Version) ([]string, error) {
	var known map[string]bool // images from earlier versions, loaded on first need
	var pending []string
	seen := make(map[string]bool)
	for _, q := range s.questions {
		for _, src := range imageRefs(answers[q.ID]) {
			if token, ok := strings.CutPrefix(src, pendingPrefix); ok {
				if !tokenRE.MatchString(token) {
					return nil, inputErrorf("invalid image token %q", token)
				}
				if uploads[token] == nil {
					return nil, inputErrorf("image %q is referenced but was not uploaded", token)
				}
				if !seen[token] {
					seen[token] = true
					pending = append(pending, token)
				}
				continue
			}
			if known == nil {
				var err error
				if known, err = s.imagesIn(ctx, history); err != nil {
					return nil, err
				}
			}
			if !known[src] {
				return nil, inputErrorf("image %q is not part of an earlier version of this questionnaire", src)
			}
		}
	}
	return pending, nil
}

func (s *Service) write(ctx context.Context, sub Submission, answers map[string]string, pending []string, exts map[string]string, ts time.Time) (Version, error) {
	stamp := FormatTimestamp(ts)
	base := basename(sub.PUID, sub.SubmitterID, stamp)
	mdName := base + ".md"
	if exists, err := s.store.Exists(ctx, mdName); err != nil {
		return Version{}, err
	} else if exists {
		return Version{}, storage.ErrExists
	}

	names := make(map[string]string, len(pending))
	for i, token := range pending {
		name := fmt.Sprintf("%s-%d%s", base, i+1, exts[token])
		if err := s.storeImage(ctx, name, sub.Images[token]); err != nil {
			return Version{}, err
		}
		names[token] = name
	}

	final := make(map[string]string, len(answers))
	for id, md := range answers {
		out, err := rewriteImageRefs(md, func(src string) (string, error) {
			if token, ok := strings.CutPrefix(src, pendingPrefix); ok {
				return names[token], nil
			}
			return src, nil
		})
		if err != nil {
			return Version{}, err
		}
		final[id] = out
	}

	meta := Meta{PUID: sub.PUID, Submitter: sub.SubmitterID, Timestamp: stamp}
	doc, err := Render(meta, s.questions, final)
	if err != nil {
		return Version{}, err
	}
	if err := s.store.Create(ctx, mdName, bytes.NewReader(doc)); err != nil {
		return Version{}, err
	}
	s.cacheMeta(mdName, &meta)
	s.logger.Info("saved questionnaire", "file", mdName, "images", len(pending))
	return Version{PUID: meta.PUID, SubmitterID: meta.Submitter, Timestamp: stamp, Filename: mdName}, nil
}

func (s *Service) storeImage(ctx context.Context, name string, open ImageUpload) error {
	rc, err := open()
	if err != nil {
		return fmt.Errorf("open upload: %w", err)
	}
	defer rc.Close()
	return s.store.Create(ctx, name, rc)
}

// Latest returns the most recent version for puid and submitter, or nil if
// there is none.
func (s *Service) Latest(ctx context.Context, puid, submitter string) (*Document, error) {
	if err := validateIdentity(puid, submitter); err != nil {
		return nil, err
	}
	history, err := s.versions(ctx, func(m *Meta) bool {
		return m.PUID == puid && m.Submitter == submitter
	})
	if err != nil || len(history) == 0 {
		return nil, err
	}
	_, answers, err := s.readDocument(ctx, history[0].Filename)
	if err != nil {
		return nil, err
	}
	return &Document{Version: history[0], Answers: answers}, nil
}

// Versions returns all saved versions for puid across submitters, newest first.
func (s *Service) Versions(ctx context.Context, puid string) ([]Version, error) {
	if err := validateID("PUID", puid); err != nil {
		return nil, err
	}
	return s.versions(ctx, func(m *Meta) bool { return m.PUID == puid })
}

// OpenImage opens a saved image by filename and returns its content type.
func (s *Service) OpenImage(ctx context.Context, name string) (io.ReadCloser, string, error) {
	contentType, ok := imageTypeByExt[path.Ext(name)]
	if !ok || !storage.ValidName(name) {
		return nil, "", ErrNotFound
	}
	rc, err := s.store.Open(ctx, name)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, "", ErrNotFound
	}
	return rc, contentType, err
}

// versions scans the store for questionnaire files matching keep, newest first.
func (s *Service) versions(ctx context.Context, keep func(*Meta) bool) ([]Version, error) {
	names, err := s.store.List(ctx)
	if err != nil {
		return nil, err
	}
	var out []Version
	for _, name := range names {
		if !strings.HasSuffix(name, ".md") {
			continue
		}
		meta, err := s.meta(ctx, name)
		if err != nil {
			return nil, err
		}
		if meta != nil && keep(meta) {
			out = append(out, Version{PUID: meta.PUID, SubmitterID: meta.Submitter, Timestamp: meta.Timestamp, Filename: name})
		}
	}
	slices.SortFunc(out, func(a, b Version) int {
		return cmp.Or(cmp.Compare(b.Timestamp, a.Timestamp), cmp.Compare(b.Filename, a.Filename))
	})
	return out, nil
}

func (s *Service) meta(ctx context.Context, name string) (*Meta, error) {
	s.metaMu.RLock()
	meta, ok := s.metaCache[name]
	s.metaMu.RUnlock()
	if ok {
		return meta, nil
	}
	rc, err := s.store.Open(ctx, name)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, nil // removed out of band since listing
	}
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	m, err := ReadMeta(rc)
	switch {
	case errors.Is(err, ErrNotQuestionnaire):
		s.logger.Warn("ignoring unrecognized markdown file", "file", name, "err", err)
		s.cacheMeta(name, nil)
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
	s.cacheMeta(name, &m)
	return &m, nil
}

func (s *Service) cacheMeta(name string, meta *Meta) {
	s.metaMu.Lock()
	s.metaCache[name] = meta
	s.metaMu.Unlock()
}

func (s *Service) readDocument(ctx context.Context, name string) (Meta, map[string]string, error) {
	rc, err := s.store.Open(ctx, name)
	if err != nil {
		return Meta{}, nil, err
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, maxFileBytes))
	if err != nil {
		return Meta{}, nil, fmt.Errorf("read %s: %w", name, err)
	}
	return Parse(data)
}

// imagesIn returns the set of image filenames referenced by the given versions.
func (s *Service) imagesIn(ctx context.Context, versions []Version) (map[string]bool, error) {
	known := make(map[string]bool)
	for _, v := range versions {
		_, answers, err := s.readDocument(ctx, v.Filename)
		if err != nil {
			return nil, err
		}
		for _, md := range answers {
			for _, src := range imageRefs(md) {
				known[src] = true
			}
		}
	}
	return known, nil
}

func sniffImage(open ImageUpload) (string, error) {
	rc, err := open()
	if err != nil {
		return "", fmt.Errorf("open upload: %w", err)
	}
	defer rc.Close()
	buf := make([]byte, 512)
	n, err := io.ReadFull(rc, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read upload: %w", err)
	}
	if n == 0 {
		return "", inputErrorf("empty image upload")
	}
	contentType := http.DetectContentType(buf[:n])
	ext, ok := imageExtByType[contentType]
	if !ok {
		return "", inputErrorf("unsupported image type %q (allowed: PNG, JPEG, GIF, WebP)", contentType)
	}
	return ext, nil
}

func validateIdentity(puid, submitter string) error {
	if err := validateID("PUID", puid); err != nil {
		return err
	}
	return validateID("submitter ID", submitter)
}

func validateID(field, v string) error {
	switch {
	case v == "":
		return inputErrorf("%s is required", field)
	case len(v) > maxIDBytes:
		return inputErrorf("%s is longer than %d bytes", field, maxIDBytes)
	case !utf8.ValidString(v):
		return inputErrorf("%s is not valid UTF-8", field)
	case strings.IndexFunc(v, unicode.IsControl) >= 0:
		return inputErrorf("%s contains control characters", field)
	}
	return nil
}
