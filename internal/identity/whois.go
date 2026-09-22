package identity

import (
	"context"
	"fmt"
	"strings"

	"tailscale.com/client/local"
)

// Node is the tailnet machine a connection came from.
type Node struct {
	ID   string // Tailscale stable node id; the key agents are bound to
	Name string // short machine name, for display and the admin list
	User string // owning login
	Tags []string
}

// Resolver answers which tailnet node a connection came from.
type Resolver interface {
	WhoIs(ctx context.Context, remoteAddr string) (Node, error)
}

// LocalResolver asks a tailscaled LocalAPI. The relay uses the tsnet
// server's LocalClient in tsnet mode and the host tailscaled in --listen mode.
type LocalResolver struct {
	lc *local.Client
}

// NewLocalResolver wraps an existing LocalAPI client (for example
// tsnet.Server.LocalClient()).
func NewLocalResolver(lc *local.Client) *LocalResolver {
	return &LocalResolver{lc: lc}
}

// NewLocalResolverAt talks to the host tailscaled at socket. An empty socket
// means the platform default.
func NewLocalResolverAt(socket string) *LocalResolver {
	lc := &local.Client{}
	if socket != "" {
		lc.Socket = socket
		lc.UseSocketOnly = true
	}
	return &LocalResolver{lc: lc}
}

// WhoIs implements Resolver.
func (r *LocalResolver) WhoIs(ctx context.Context, remoteAddr string) (Node, error) {
	res, err := r.lc.WhoIs(ctx, remoteAddr)
	if err != nil {
		return Node{}, fmt.Errorf("whois %s: %w", remoteAddr, err)
	}
	if res.Node == nil {
		return Node{}, fmt.Errorf("whois %s: no node", remoteAddr)
	}
	n := Node{ID: string(res.Node.StableID), Name: shortName(res.Node.Name), Tags: res.Node.Tags}
	if res.UserProfile != nil {
		n.User = res.UserProfile.LoginName
	}
	return n, nil
}

// Probe fails when the LocalAPI is unreachable. --listen mode calls it at
// startup and refuses to run rather than skip attribution.
func (r *LocalResolver) Probe(ctx context.Context) error {
	if _, err := r.lc.StatusWithoutPeers(ctx); err != nil {
		return fmt.Errorf("tailscaled LocalAPI unreachable: %w", err)
	}
	return nil
}

// shortName turns "grok-bot.tail2b6977.ts.net." into "grok-bot".
func shortName(fqdn string) string {
	name, _, _ := strings.Cut(strings.TrimSuffix(fqdn, "."), ".")
	return name
}
