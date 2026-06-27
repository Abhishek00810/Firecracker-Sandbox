package session

import (
	"backend/internal/cgroup"
	"backend/internal/executor/firecracker"
	"fmt"
	"net"
	"sync"
	"time"
)

type Session struct {
	ID               string
	VM               *firecracker.MicroVM
	Cgroup           *cgroup.Cgroup
	Tier             string
	VCPUs            int // allocated compute shape — the billing basis
	MemoryMB         int
	DiskGB           int
	Internet         bool // egress allowed (false = network-isolated)
	IdleTimeout      time.Duration // reaper evicts after this idle time (per-session)
	MaxLifetime      time.Duration // hard ceiling regardless of activity (0 = no limit)
	CreatedAt        time.Time
	LastUsed         time.Time
	RunCount         int
	TotalExecutionMs float64
	LastExitCode     *int
	mu               sync.Mutex            // serializes concurrent runs on same session
	PooledVM         *firecracker.PooledVM // non-nil when VM came from a pool
	Pool             *firecracker.VMPool   // which pool to release back to (matches PooledVM)
	VsockConn        net.Conn              // persistent vsock connection, reused across all calls
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

// EvictIdle returns sessions idle longer than their own IdleTimeout OR alive longer than
// their own MaxLifetime, and removes them from the store. A zero value on a session
// disables that check for that session.
func (s *Store) EvictIdle() []*Session {
	s.mu.Lock()
	defer s.mu.Unlock()

	var evicted []*Session
	for id, sess := range s.sessions {
		idleExpired := sess.IdleTimeout > 0 && time.Since(sess.LastUsed) > sess.IdleTimeout
		lifetimeExpired := sess.MaxLifetime > 0 && time.Since(sess.CreatedAt) > sess.MaxLifetime
		if idleExpired || lifetimeExpired {
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
