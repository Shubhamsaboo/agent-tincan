package identity

import (
	"context"
	"sort"
	"sync"
)

// MemoryStore is an in-memory Store for tests and ephemeral runs.
type MemoryStore struct {
	mu      sync.Mutex
	agents  map[string]Agent
	invites map[string]Invite
}

// NewMemoryStore returns an empty MemoryStore.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{agents: map[string]Agent{}, invites: map[string]Invite{}}
}

func (m *MemoryStore) PutAgent(_ context.Context, a Agent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.agents[a.Name] = a
	return nil
}

func (m *MemoryStore) DeleteAgent(_ context.Context, name string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.agents[name]
	delete(m.agents, name)
	return ok, nil
}

func (m *MemoryStore) Agents(_ context.Context) ([]Agent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Agent, 0, len(m.agents))
	for _, a := range m.agents {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (m *MemoryStore) AgentByNode(_ context.Context, nodeID string) (Agent, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, a := range m.agents {
		if a.NodeID == nodeID {
			return a, true, nil
		}
	}
	return Agent{}, false, nil
}

func (m *MemoryStore) AgentByName(_ context.Context, name string) (Agent, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.agents[name]
	return a, ok, nil
}

func (m *MemoryStore) PutInvite(_ context.Context, inv Invite) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.invites[inv.Code] = inv
	return nil
}

func (m *MemoryStore) TakeInvite(_ context.Context, code string) (Invite, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	inv, ok := m.invites[code]
	delete(m.invites, code)
	return inv, ok, nil
}
