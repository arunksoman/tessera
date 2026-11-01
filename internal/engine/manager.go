package engine

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Manager shares one open connection per profile, caps how many stay open, and closes those
// left idle.
type Manager struct {
	e     *Engine
	max   int
	idle  time.Duration
	now   func() time.Time
	mu    sync.Mutex
	conns map[string]*managed
}

type managed struct {
	conn     *Conn
	refs     int
	lastUsed time.Time
}

func NewManager(e *Engine, maxOpen int, idle time.Duration) *Manager {
	return &Manager{e: e, max: maxOpen, idle: idle, now: time.Now, conns: map[string]*managed{}}
}

// Acquire returns the profile's connection, opening it if needed. Call the release function
// when done. It is safe to release only once.
func (m *Manager) Acquire(ctx context.Context, profile string) (*Conn, func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok := m.conns[profile]
	if !ok {
		if len(m.conns) >= m.max && !m.evictOldest() {
			return nil, nil, fmt.Errorf("%w: %d in use", ErrTooManyConns, len(m.conns))
		}
		// shortcut: opening holds the lock, so a slow connect blocks other acquires. Fine for
		// one interactive user; a server needs per-profile locking.
		c, err := m.e.Open(ctx, profile)
		if err != nil {
			return nil, nil, err
		}
		entry = &managed{conn: c}
		m.conns[profile] = entry
	}
	entry.refs++
	entry.lastUsed = m.now()
	var once sync.Once
	return entry.conn, func() {
		once.Do(func() {
			m.mu.Lock()
			defer m.mu.Unlock()
			entry.refs--
			entry.lastUsed = m.now()
		})
	}, nil
}

func (m *Manager) evictOldest() bool {
	var oldest string
	for name, c := range m.conns {
		if c.refs == 0 && (oldest == "" || c.lastUsed.Before(m.conns[oldest].lastUsed)) {
			oldest = name
		}
	}
	if oldest == "" {
		return false
	}
	m.conns[oldest].conn.Close()
	delete(m.conns, oldest)
	return true
}

// CloseIdle closes unused connections past the idle limit.
func (m *Manager) CloseIdle() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for name, c := range m.conns {
		if c.refs == 0 && m.now().Sub(c.lastUsed) >= m.idle {
			c.conn.Close()
			delete(m.conns, name)
		}
	}
}

// Forget closes a profile's connection if nothing uses it, so changed settings take effect.
func (m *Manager) Forget(profile string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok := m.conns[profile]; ok && c.refs == 0 {
		c.conn.Close()
		delete(m.conns, profile)
	}
}

// Close closes every connection.
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for name, c := range m.conns {
		c.conn.Close()
		delete(m.conns, name)
	}
}
