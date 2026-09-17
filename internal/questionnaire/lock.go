package questionnaire

import "sync"

type lockKey struct {
	puid, submitter string
}

// keyedLocks is a non-blocking lock per (PUID, submitter). Entries are
// removed on unlock, so the table only holds in-flight submissions.
type keyedLocks struct {
	mu   sync.Mutex
	held map[lockKey]struct{}
}

// tryLock acquires the lock for key, or reports false if it is already held.
func (l *keyedLocks) tryLock(key lockKey) (unlock func(), ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, busy := l.held[key]; busy {
		return nil, false
	}
	if l.held == nil {
		l.held = make(map[lockKey]struct{})
	}
	l.held[key] = struct{}{}
	return func() {
		l.mu.Lock()
		delete(l.held, key)
		l.mu.Unlock()
	}, true
}
