package ssh

import "sync"

// Pool is the in-memory registry of live SSH sessions, keyed by ID.
type Pool struct {
	mu       sync.RWMutex
	sessions map[string]*Session
}

func NewPool() *Pool {
	return &Pool{sessions: map[string]*Session{}}
}

func (p *Pool) Add(s *Session) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sessions[s.ID] = s
}

func (p *Pool) Get(id string) (*Session, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	s, ok := p.sessions[id]
	return s, ok
}

func (p *Pool) Remove(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.sessions, id)
}

func (p *Pool) IDs() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]string, 0, len(p.sessions))
	for id := range p.sessions {
		out = append(out, id)
	}
	return out
}
