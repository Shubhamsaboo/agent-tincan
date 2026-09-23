// Package store persists the relay's state in SQLite: the agent directory,
// invite codes, and the request queue. It is pure Go (modernc.org/sqlite), so
// release binaries build with CGO_ENABLED=0.
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	_ "modernc.org/sqlite"

	"github.com/mvanhorn/agent-tincan/internal/envelope"
	"github.com/mvanhorn/agent-tincan/internal/identity"
)

var (
	// ErrNotFound means no request with that id is visible to the caller.
	ErrNotFound = errors.New("request not found")
	// ErrForbidden means the caller is not the agent allowed to do this.
	ErrForbidden = errors.New("not allowed for this agent")
	// ErrWrongState means the request is not in a state that allows this.
	ErrWrongState = errors.New("request is not in a state that allows this")
)

const schema = `
CREATE TABLE IF NOT EXISTS agents (
  name      TEXT PRIMARY KEY,
  node_id   TEXT NOT NULL UNIQUE,
  node_name TEXT NOT NULL,
  joined_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS invites (
  code    TEXT PRIMARY KEY,
  name    TEXT NOT NULL,
  expires INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS requests (
  id          TEXT PRIMARY KEY,
  from_agent  TEXT NOT NULL,
  to_agent    TEXT NOT NULL,
  parent_id   TEXT NOT NULL DEFAULT '',
  trace_id    TEXT NOT NULL,
  hop         INTEGER NOT NULL,
  chain       TEXT NOT NULL,
  kind        TEXT NOT NULL,
  body        TEXT NOT NULL,
  status      TEXT NOT NULL,
  created_at  INTEGER NOT NULL,
  updated_at  INTEGER NOT NULL,
  expires_at  INTEGER NOT NULL,
  lease_until INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS requests_to_status ON requests(to_agent, status);
CREATE TABLE IF NOT EXISTS replies (
  request_id TEXT PRIMARY KEY REFERENCES requests(id),
  from_agent TEXT NOT NULL,
  status     TEXT NOT NULL,
  body       TEXT NOT NULL,
  created_at INTEGER NOT NULL
);
`

// Store is the relay's SQLite store.
type Store struct {
	db  *sql.DB
	now func() time.Time
}

// Open opens (creating if needed) the database at path. Use ":memory:" in
// tests.
func Open(path string) (*Store, error) {
	dsn := path
	if path == ":memory:" {
		// One shared in-memory database per Store.
		dsn = "file:" + randomID() + "?mode=memory&cache=shared"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // SQLite has one writer; serialize in-process
	for _, pragma := range []string{"PRAGMA journal_mode=WAL", "PRAGMA busy_timeout=5000", "PRAGMA foreign_keys=ON"} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("%s: %w", pragma, err)
		}
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("schema: %w", err)
	}
	s := &Store{db: db, now: time.Now}
	if err := s.ensureAudit(); err != nil {
		db.Close()
		return nil, fmt.Errorf("audit schema: %w", err)
	}
	return s, nil
}

// SetClock overrides the clock in tests.
func (s *Store) SetClock(now func() time.Time) { s.now = now }

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// DB exposes the handle for packages that add their own tables (audit).
func (s *Store) DB() *sql.DB { return s.db }

// --- identity.Store ---

func (s *Store) PutAgent(ctx context.Context, a identity.Agent) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// A name moving to a new machine replaces its old binding.
	if _, err := tx.ExecContext(ctx, `DELETE FROM agents WHERE name = ? OR node_id = ?`, a.Name, a.NodeID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agents(name, node_id, node_name, joined_at) VALUES (?, ?, ?, ?)`,
		a.Name, a.NodeID, a.NodeName, a.JoinedAt.UnixMilli()); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) DeleteAgent(ctx context.Context, name string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM agents WHERE name = ?`, name)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (s *Store) Agents(ctx context.Context) ([]identity.Agent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name, node_id, node_name, joined_at FROM agents ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []identity.Agent
	for rows.Next() {
		var a identity.Agent
		var joined int64
		if err := rows.Scan(&a.Name, &a.NodeID, &a.NodeName, &joined); err != nil {
			return nil, err
		}
		a.JoinedAt = time.UnixMilli(joined)
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) PutInvite(ctx context.Context, inv identity.Invite) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR REPLACE INTO invites(code, name, expires) VALUES (?, ?, ?)`,
		inv.Code, inv.Name, inv.Expires.UnixMilli())
	return err
}

func (s *Store) TakeInvite(ctx context.Context, code string) (identity.Invite, bool, error) {
	var inv identity.Invite
	var exp int64
	err := s.db.QueryRowContext(ctx, `DELETE FROM invites WHERE code = ? RETURNING code, name, expires`, code).Scan(&inv.Code, &inv.Name, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return identity.Invite{}, false, nil
	}
	if err != nil {
		return identity.Invite{}, false, err
	}
	inv.Expires = time.UnixMilli(exp)
	return inv, true, nil
}

// --- request queue ---

// Enqueue stores a new request. The caller has already set From, To, Kind,
// Body, TraceID, Hop, Chain, and ParentID. Enqueue assigns ID and CreatedAt.
func (s *Store) Enqueue(ctx context.Context, req envelope.Request, ttl time.Duration) (envelope.Request, error) {
	now := s.now()
	req.ID = randomID()
	req.CreatedAt = now.UTC().Truncate(time.Millisecond)
	if req.TraceID == "" {
		req.TraceID = req.ID
	}
	chain, err := json.Marshal(req.Chain)
	if err != nil {
		return envelope.Request{}, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO requests
		(id, from_agent, to_agent, parent_id, trace_id, hop, chain, kind, body, status, created_at, updated_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		req.ID, req.From, req.To, req.ParentID, req.TraceID, req.Hop, string(chain), string(req.Kind), req.Body,
		string(envelope.StatusQueued), now.UnixMilli(), now.UnixMilli(), now.Add(ttl).UnixMilli())
	if err != nil {
		return envelope.Request{}, err
	}
	return req, nil
}

// Deliver hands up to limit queued requests for agent to its poller, marking
// them delivered under a lease. A request delivered but never claimed returns
// to the queue when the lease runs out, so a crash after polling strands
// nothing.
func (s *Store) Deliver(ctx context.Context, agent string, limit int, lease time.Duration) ([]envelope.Request, error) {
	now := s.now()
	rows, err := s.db.QueryContext(ctx, `UPDATE requests SET status = ?, lease_until = ?, updated_at = ?
		WHERE id IN (SELECT id FROM requests WHERE to_agent = ? AND status = ? AND expires_at > ? ORDER BY created_at LIMIT ?)
		RETURNING `+requestCols,
		string(envelope.StatusDelivered), now.Add(lease).UnixMilli(), now.UnixMilli(),
		agent, string(envelope.StatusQueued), now.UnixMilli(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out, err := scanRequests(rows)
	if err != nil {
		return nil, err
	}
	sortByCreated(out)
	return out, nil
}

// CountQueued returns how many requests are waiting for agent without
// delivering them.
func (s *Store) CountQueued(ctx context.Context, agent string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM requests WHERE to_agent = ? AND status = ? AND expires_at > ?`,
		agent, string(envelope.StatusQueued), s.now().UnixMilli()).Scan(&n)
	return n, err
}

// Claim marks a request as being worked on by its target, under a lease.
func (s *Store) Claim(ctx context.Context, id, agent string, lease time.Duration) (envelope.Request, error) {
	req, _, err := s.lookup(ctx, id)
	if err != nil {
		return envelope.Request{}, err
	}
	if req.To != agent {
		return envelope.Request{}, ErrForbidden
	}
	now := s.now()
	res, err := s.db.ExecContext(ctx, `UPDATE requests SET status = ?, lease_until = ?, updated_at = ?
		WHERE id = ? AND status IN (?, ?, ?) AND expires_at > ?`,
		string(envelope.StatusClaimed), now.Add(lease).UnixMilli(), now.UnixMilli(), id,
		string(envelope.StatusQueued), string(envelope.StatusDelivered), string(envelope.StatusClaimed), now.UnixMilli())
	if err != nil {
		return envelope.Request{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return envelope.Request{}, ErrWrongState
	}
	req, _, err = s.lookup(ctx, id)
	return req, err
}

// Reply stores the target's answer and closes the request.
func (s *Store) Reply(ctx context.Context, id, agent string, rep envelope.Reply) (envelope.Reply, error) {
	req, _, err := s.lookup(ctx, id)
	if err != nil {
		return envelope.Reply{}, err
	}
	if req.To != agent {
		return envelope.Reply{}, ErrForbidden
	}
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return envelope.Reply{}, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE requests SET status = ?, lease_until = 0, updated_at = ?
		WHERE id = ? AND status IN (?, ?, ?)`,
		string(rep.Status), now.UnixMilli(), id,
		string(envelope.StatusQueued), string(envelope.StatusDelivered), string(envelope.StatusClaimed))
	if err != nil {
		return envelope.Reply{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return envelope.Reply{}, ErrWrongState
	}
	rep.RequestID, rep.From, rep.CreatedAt = id, agent, now.UTC().Truncate(time.Millisecond)
	if _, err := tx.ExecContext(ctx, `INSERT INTO replies(request_id, from_agent, status, body, created_at) VALUES (?, ?, ?, ?, ?)`,
		id, agent, string(rep.Status), rep.Body, now.UnixMilli()); err != nil {
		return envelope.Reply{}, err
	}
	return rep, tx.Commit()
}

// Result is a request with its current status and reply, if any.
type Result struct {
	Request envelope.Request `json:"request"`
	Status  envelope.Status  `json:"status"`
	Reply   *envelope.Reply  `json:"reply,omitempty"`
}

// Get returns a request for its sender or its target.
func (s *Store) Get(ctx context.Context, id, agent string) (Result, error) {
	req, status, err := s.lookup(ctx, id)
	if err != nil {
		return Result{}, err
	}
	if req.From != agent && req.To != agent {
		return Result{}, ErrNotFound
	}
	out := Result{Request: req, Status: status}
	var rep envelope.Reply
	var created int64
	var st string
	err = s.db.QueryRowContext(ctx, `SELECT from_agent, status, body, created_at FROM replies WHERE request_id = ?`, id).
		Scan(&rep.From, &st, &rep.Body, &created)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return Result{}, err
	default:
		rep.RequestID, rep.Status, rep.CreatedAt = id, envelope.Status(st), time.UnixMilli(created).UTC()
		out.Reply = &rep
	}
	return out, nil
}

// Cancel lets the sender withdraw a request the target has not claimed.
func (s *Store) Cancel(ctx context.Context, id, agent string) error {
	req, _, err := s.lookup(ctx, id)
	if err != nil {
		return err
	}
	if req.From != agent {
		return ErrForbidden
	}
	res, err := s.db.ExecContext(ctx, `UPDATE requests SET status = ?, updated_at = ? WHERE id = ? AND status IN (?, ?)`,
		string(envelope.StatusCancelled), s.now().UnixMilli(), id, string(envelope.StatusQueued), string(envelope.StatusDelivered))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrWrongState
	}
	return nil
}

// CancelAllTo cancels every open request addressed to agent (used when an
// agent is removed). It returns the cancelled ids.
func (s *Store) CancelAllTo(ctx context.Context, agent string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `UPDATE requests SET status = ?, updated_at = ? WHERE to_agent = ? AND status IN (?, ?, ?) RETURNING id`,
		string(envelope.StatusCancelled), s.now().UnixMilli(), agent,
		string(envelope.StatusQueued), string(envelope.StatusDelivered), string(envelope.StatusClaimed))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// Transition is one state change made by Sweep.
type Transition struct {
	ID     string
	From   string
	To     string
	Status envelope.Status
}

// Sweep expires requests past their TTL and returns requests whose delivery
// or claim lease ran out to the queue.
func (s *Store) Sweep(ctx context.Context) ([]Transition, error) {
	now := s.now().UnixMilli()
	var out []Transition
	collect := func(query string, args ...any) error {
		rows, err := s.db.QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var t Transition
			var st string
			if err := rows.Scan(&t.ID, &t.From, &t.To, &st); err != nil {
				return err
			}
			t.Status = envelope.Status(st)
			out = append(out, t)
		}
		return rows.Err()
	}
	if err := collect(`UPDATE requests SET status = ?, lease_until = 0, updated_at = ?
		WHERE status IN (?, ?) AND expires_at <= ? RETURNING id, from_agent, to_agent, status`,
		string(envelope.StatusExpired), now, string(envelope.StatusQueued), string(envelope.StatusDelivered), now); err != nil {
		return nil, err
	}
	if err := collect(`UPDATE requests SET status = ?, lease_until = 0, updated_at = ?
		WHERE status IN (?, ?) AND lease_until > 0 AND lease_until <= ? RETURNING id, from_agent, to_agent, status`,
		string(envelope.StatusQueued), now, string(envelope.StatusDelivered), string(envelope.StatusClaimed), now); err != nil {
		return nil, err
	}
	return out, nil
}

// OpenClaim returns the most recent request agent has claimed and not yet
// answered, if any. The relay uses it to continue a chain when a sender
// forgets to name a parent.
func (s *Store) OpenClaim(ctx context.Context, agent string) (envelope.Request, bool, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+requestCols+` FROM requests WHERE to_agent = ? AND status = ?
		ORDER BY updated_at DESC LIMIT 1`, agent, string(envelope.StatusClaimed))
	req, _, err := scanRequest(row)
	if errors.Is(err, sql.ErrNoRows) {
		return envelope.Request{}, false, nil
	}
	return req, err == nil, err
}

// Request returns a stored request regardless of caller (relay-internal).
func (s *Store) Request(ctx context.Context, id string) (envelope.Request, envelope.Status, error) {
	return s.lookup(ctx, id)
}

const requestCols = `id, from_agent, to_agent, parent_id, trace_id, hop, chain, kind, body, status, created_at`

type scanner interface{ Scan(dest ...any) error }

func scanRequest(sc scanner) (envelope.Request, envelope.Status, error) {
	var r envelope.Request
	var chain, kind, status string
	var created int64
	if err := sc.Scan(&r.ID, &r.From, &r.To, &r.ParentID, &r.TraceID, &r.Hop, &chain, &kind, &r.Body, &status, &created); err != nil {
		return envelope.Request{}, "", err
	}
	if err := json.Unmarshal([]byte(chain), &r.Chain); err != nil {
		return envelope.Request{}, "", err
	}
	r.Kind, r.CreatedAt = envelope.Kind(kind), time.UnixMilli(created).UTC()
	return r, envelope.Status(status), nil
}

func scanRequests(rows *sql.Rows) ([]envelope.Request, error) {
	var out []envelope.Request
	for rows.Next() {
		r, _, err := scanRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) lookup(ctx context.Context, id string) (envelope.Request, envelope.Status, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+requestCols+` FROM requests WHERE id = ?`, id)
	r, st, err := scanRequest(row)
	if errors.Is(err, sql.ErrNoRows) {
		return envelope.Request{}, "", ErrNotFound
	}
	return r, st, err
}

func sortByCreated(rs []envelope.Request) {
	slices.SortStableFunc(rs, func(a, b envelope.Request) int { return a.CreatedAt.Compare(b.CreatedAt) })
}

func randomID() string {
	b := make([]byte, 10)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
