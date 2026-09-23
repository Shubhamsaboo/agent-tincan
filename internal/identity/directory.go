// Package identity decides which agent sent a request. An agent is a name Matt
// chose, bound to one tailnet node by a one-time invite code. There are no
// keys: Tailscale authenticates the node, and the directory maps node to name.
package identity

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

// InviteTTL is how long an invite code stays valid.
const InviteTTL = 10 * time.Minute

// LocalAdmin is the remote address the relay passes for commands that arrive
// on its local admin socket. It always has admin rights.
const LocalAdmin = "local-admin"

var (
	ErrNotJoined    = errors.New("not a joined agent")
	ErrNotAdmin     = errors.New("admin commands must come from an admin device")
	ErrBadInvite    = errors.New("invite code is invalid, used, or expired")
	ErrUnknownAgent = errors.New("no such agent")
	ErrNodeTaken    = errors.New("this machine is already joined as another agent")
)

var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// Agent is a joined agent.
type Agent struct {
	Name     string    `json:"name"`
	NodeID   string    `json:"node_id"`
	NodeName string    `json:"node_name"`
	JoinedAt time.Time `json:"joined_at"`
}

// Invite is a pending one-time code.
type Invite struct {
	Code    string
	Name    string
	Expires time.Time
}

// Store persists agents and invites. The relay backs it with SQLite; tests
// use NewMemoryStore.
type Store interface {
	PutAgent(ctx context.Context, a Agent) error
	DeleteAgent(ctx context.Context, name string) (bool, error)
	Agents(ctx context.Context) ([]Agent, error)
	// AgentByNode and AgentByName are point lookups for the hot request path.
	AgentByNode(ctx context.Context, nodeID string) (Agent, bool, error)
	AgentByName(ctx context.Context, name string) (Agent, bool, error)
	PutInvite(ctx context.Context, inv Invite) error
	// TakeInvite removes and returns the invite, so a code works once.
	TakeInvite(ctx context.Context, code string) (Invite, bool, error)
}

// Config tunes a Directory.
type Config struct {
	// Admins lists the machine names allowed to invite and remove agents.
	Admins []string
	// AdminLogins, when set, also requires an admin node's owning Tailscale
	// login to be listed. Every node on a single-user tailnet shares one login,
	// so this narrows admin rights but cannot replace the machine list.
	AdminLogins []string
	// Now overrides the clock in tests.
	Now func() time.Time
}

// Directory maps tailnet nodes to agent names.
type Directory struct {
	store Store
	who   Resolver
	cfg   Config
	mu    sync.Mutex // serializes join so one node cannot bind two names
}

// NewDirectory builds a Directory.
func NewDirectory(store Store, who Resolver, cfg Config) *Directory {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Directory{store: store, who: who, cfg: cfg}
}

// Attribute returns the agent name for the node behind remoteAddr.
func (d *Directory) Attribute(ctx context.Context, remoteAddr string) (string, error) {
	n, err := d.who.WhoIs(ctx, remoteAddr)
	if err != nil {
		return "", err
	}
	a, ok, err := d.store.AgentByNode(ctx, n.ID)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("%s: %w", n.Name, ErrNotJoined)
	}
	return a.Name, nil
}

// Has reports whether name is a joined agent.
func (d *Directory) Has(ctx context.Context, name string) (bool, error) {
	_, ok, err := d.store.AgentByName(ctx, name)
	return ok, err
}

// Invite creates a one-time code that joins the next machine to use it as
// name. Only admin devices may invite.
func (d *Directory) Invite(ctx context.Context, remoteAddr, name string) (string, error) {
	if err := d.requireAdmin(ctx, remoteAddr); err != nil {
		return "", err
	}
	if !nameRE.MatchString(name) {
		return "", fmt.Errorf("agent name %q must be 1-32 lowercase letters, digits, or dashes", name)
	}
	code, err := NewCode()
	if err != nil {
		return "", err
	}
	inv := Invite{Code: code, Name: name, Expires: d.cfg.Now().Add(InviteTTL)}
	if err := d.store.PutInvite(ctx, inv); err != nil {
		return "", err
	}
	return code, nil
}

// Join binds the node behind remoteAddr to the invite's name. Re-inviting an
// existing name moves it to the new machine.
func (d *Directory) Join(ctx context.Context, remoteAddr, code string) (string, error) {
	n, err := d.who.WhoIs(ctx, remoteAddr)
	if err != nil {
		return "", err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	bound, taken, err := d.store.AgentByNode(ctx, n.ID)
	if err != nil {
		return "", err
	}
	inv, ok, err := d.store.TakeInvite(ctx, strings.ToUpper(strings.TrimSpace(code)))
	if err != nil {
		return "", err
	}
	if !ok || d.cfg.Now().After(inv.Expires) {
		return "", ErrBadInvite
	}
	if taken && bound.Name != inv.Name {
		// A mistaken join from an already-joined machine must not burn the
		// code. Join holds d.mu, so no other join can observe the gap.
		if err := d.store.PutInvite(ctx, inv); err != nil {
			return "", err
		}
		return "", fmt.Errorf("%s is %q: %w", n.Name, bound.Name, ErrNodeTaken)
	}
	a := Agent{Name: inv.Name, NodeID: n.ID, NodeName: n.Name, JoinedAt: d.cfg.Now()}
	if err := d.store.PutAgent(ctx, a); err != nil {
		return "", err
	}
	return a.Name, nil
}

// Remove unbinds an agent. The relay cancels its queued requests.
func (d *Directory) Remove(ctx context.Context, remoteAddr, name string) error {
	if err := d.requireAdmin(ctx, remoteAddr); err != nil {
		return err
	}
	ok, err := d.store.DeleteAgent(ctx, name)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%s: %w", name, ErrUnknownAgent)
	}
	return nil
}

// Agents lists joined agents.
func (d *Directory) Agents(ctx context.Context) ([]Agent, error) {
	return d.store.Agents(ctx)
}

// IsAdmin reports whether remoteAddr may run admin commands.
func (d *Directory) IsAdmin(ctx context.Context, remoteAddr string) bool {
	return d.requireAdmin(ctx, remoteAddr) == nil
}

func (d *Directory) requireAdmin(ctx context.Context, remoteAddr string) error {
	if remoteAddr == LocalAdmin {
		return nil
	}
	n, err := d.who.WhoIs(ctx, remoteAddr)
	if err != nil {
		return err
	}
	// Admin = named in --admin, untagged (tagged nodes are services, never
	// people), and, when --admin-login is set, owned by a listed login.
	if !slices.Contains(d.cfg.Admins, n.Name) {
		return fmt.Errorf("%s: %w", n.Name, ErrNotAdmin)
	}
	if len(n.Tags) > 0 {
		return fmt.Errorf("%s is tagged %s: %w", n.Name, strings.Join(n.Tags, ","), ErrNotAdmin)
	}
	if len(d.cfg.AdminLogins) > 0 && !slices.Contains(d.cfg.AdminLogins, n.User) {
		return fmt.Errorf("%s is owned by %q: %w", n.Name, n.User, ErrNotAdmin)
	}
	return nil
}

// codeAlphabet drops 0, 1, I, and O so codes read cleanly aloud.
const codeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// NewCode returns a random one-time code shaped XXXX-XXXX.
func NewCode() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	out := make([]byte, 0, 9)
	for i, c := range b {
		if i == 4 {
			out = append(out, '-')
		}
		out = append(out, codeAlphabet[int(c)%len(codeAlphabet)])
	}
	return string(out), nil
}
