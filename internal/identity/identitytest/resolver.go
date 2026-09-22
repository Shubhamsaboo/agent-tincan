// Package identitytest provides a fake WhoIs resolver for tests. Production
// code never imports it.
package identitytest

import (
	"context"
	"fmt"
	"maps"
	"sync"

	"github.com/mvanhorn/agent-tincan/internal/identity"
)

// Resolver maps remote addresses to nodes.
type Resolver struct {
	mu    sync.Mutex
	nodes map[string]identity.Node
}

// New returns a Resolver seeded with addr -> node.
func New(nodes map[string]identity.Node) *Resolver {
	cp := make(map[string]identity.Node, len(nodes))
	maps.Copy(cp, nodes)
	return &Resolver{nodes: cp}
}

// Set maps addr to node.
func (r *Resolver) Set(addr string, n identity.Node) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nodes[addr] = n
}

// WhoIs implements identity.Resolver.
func (r *Resolver) WhoIs(_ context.Context, addr string) (identity.Node, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n, ok := r.nodes[addr]
	if !ok {
		return identity.Node{}, fmt.Errorf("whois %s: not a tailnet peer", addr)
	}
	return n, nil
}
