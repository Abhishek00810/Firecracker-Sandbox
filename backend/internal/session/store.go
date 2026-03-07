package session

import (
	"backend/internal/cgroup"
	"backend/internal/executor/firecracker"
	"fmt"
	"sync"
	"time"
)

type Session struct {
	ID        string
	VM        *firecracker.MicroVM
	Cgroup    *cgroup.Cgroup
	Tier      string
	CreatedAt time.Time
	LastUsed  time.Time
	mu        sync.Mutex // serializes concurrent runs on same session
}

type Store struct {
	mu       sync.RWMutex
	sessions map[string]*Session
	max      int // hard cap for each session = one live vm = ram cost
}

func NewStore(max int) *Store {
	return &Store{
		sessions: make(map[string]*Session),
		max:      max,
	}
}

func (s *Store) Add(sess *Session) error {
	s.mu.Lock()

	defer s.mu.Unlock()

	if len(s.sessions) >= s.max {
		return fmt.Errorf("session liimit reached (%d)", s.max)
	}

	s.sessions[sess.ID] = sess
	return nil
}

func (s *Store) Get(id string) (*Session, bool) {
	s.mu.RLock()

	defer s.mu.RUnlock()

	sess, ok := s.sessions[id]

	return sess, ok
}

func (s *Store) Delete(id string) (*Session, bool) {
	s.mu.Lock()

	defer s.mu.Unlock()

	sess, ok := s.sessions[id]

	if ok {
		delete(s.sessions, id)
	}

	return sess, ok
}

// EvictIdle returns all sessions idle longer than maxIdle and removes them from store
func (s *Store) EvictIdle(maxIdle time.Duration) []*Session {
	s.mu.Lock()
	defer s.mu.Unlock()

	var evicted []*Session
	for id, sess := range s.sessions {
		if time.Since(sess.LastUsed) > maxIdle {
			evicted = append(evicted, sess)
			delete(s.sessions, id)
		}
	}
	return evicted
}

func (s *Store) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.sessions)
}

func (s *Store) All() []*Session {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Session, 0, len(s.sessions))
	for _, sess := range s.sessions {
		out = append(out, sess)
	}
	return out
}
