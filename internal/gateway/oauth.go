// Package gateway lets ChatGPT, which runs in OpenAI's cloud and cannot reach
// the tailnet, join as an agent. It serves the Agent Tincan MCP tools over
// Streamable HTTP on a public Funnel hostname, behind the smallest OAuth 2.1
// authorization server ChatGPT's connectors accept: one owner, PKCE, and a
// login page that takes a one-time code Matt mints with `tincan connect`.
//
// Only the MCP endpoint and the OAuth endpoints are served. The relay's agent
// API is never reachable from the public side.
package gateway

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"time"
)

const oauthSchema = `
CREATE TABLE IF NOT EXISTS gw_clients (
  client_id     TEXT PRIMARY KEY,
  redirect_uris TEXT NOT NULL,
  created_at    INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS gw_codes (
  hash       TEXT PRIMARY KEY,
  kind       TEXT NOT NULL,          -- login | auth
  agent      TEXT NOT NULL,
  client_id  TEXT NOT NULL DEFAULT '',
  redirect   TEXT NOT NULL DEFAULT '',
  challenge  TEXT NOT NULL DEFAULT '',
  expires_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS gw_tokens (
  hash       TEXT PRIMARY KEY,
  kind       TEXT NOT NULL,          -- access | refresh
  agent      TEXT NOT NULL,
  client_id  TEXT NOT NULL,
  expires_at INTEGER NOT NULL
);
`

// Lifetimes.
const (
	LoginCodeTTL = 10 * time.Minute
	authCodeTTL  = time.Minute
	accessTTL    = time.Hour
	refreshTTL   = 30 * 24 * time.Hour
)

var errInvalid = errors.New("invalid or expired")

// OAuth stores clients, one-time codes, and tokens. Only hashes of codes and
// tokens are stored.
type OAuth struct {
	db  *sql.DB
	now func() time.Time
}

// NewOAuth creates the OAuth tables in db.
func NewOAuth(db *sql.DB, now func() time.Time) (*OAuth, error) {
	if now == nil {
		now = time.Now
	}
	if _, err := db.Exec(oauthSchema); err != nil {
		return nil, err
	}
	return &OAuth{db: db, now: now}, nil
}

func secret() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func hash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// MintLoginCode creates the one-time code Matt types on the login page.
func (o *OAuth) MintLoginCode(ctx context.Context, agent string) (string, error) {
	code := shortCode()
	_, err := o.db.ExecContext(ctx, `INSERT INTO gw_codes(hash, kind, agent, expires_at) VALUES (?, 'login', ?, ?)`,
		hash(code), agent, o.now().Add(LoginCodeTTL).UnixMilli())
	return code, err
}

// RegisterClient implements dynamic client registration.
func (o *OAuth) RegisterClient(ctx context.Context, redirectURIs string) (string, error) {
	id := "tincan-" + secret()[:16]
	_, err := o.db.ExecContext(ctx, `INSERT INTO gw_clients(client_id, redirect_uris, created_at) VALUES (?, ?, ?)`, id, redirectURIs, o.now().UnixMilli())
	return id, err
}

// ClientRedirects returns a registered client's redirect URIs (newline separated).
func (o *OAuth) ClientRedirects(ctx context.Context, clientID string) (string, error) {
	var uris string
	err := o.db.QueryRowContext(ctx, `SELECT redirect_uris FROM gw_clients WHERE client_id = ?`, clientID).Scan(&uris)
	if errors.Is(err, sql.ErrNoRows) {
		return "", errInvalid
	}
	return uris, err
}

// takeCode consumes a one-time code of the given kind.
func (o *OAuth) takeCode(ctx context.Context, code, kind string) (agent, clientID, redirect, challenge string, err error) {
	var exp int64
	err = o.db.QueryRowContext(ctx, `DELETE FROM gw_codes WHERE hash = ? AND kind = ? RETURNING agent, client_id, redirect, challenge, expires_at`,
		hash(code), kind).Scan(&agent, &clientID, &redirect, &challenge, &exp)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && o.now().UnixMilli() > exp) {
		return "", "", "", "", errInvalid
	}
	return agent, clientID, redirect, challenge, err
}

// ExchangeLogin trades a login code for an authorization code bound to the
// client, redirect URI, and PKCE challenge.
func (o *OAuth) ExchangeLogin(ctx context.Context, loginCode, clientID, redirect, challenge string) (string, error) {
	agent, _, _, _, err := o.takeCode(ctx, loginCode, "login")
	if err != nil {
		return "", err
	}
	code := secret()
	_, err = o.db.ExecContext(ctx, `INSERT INTO gw_codes(hash, kind, agent, client_id, redirect, challenge, expires_at) VALUES (?, 'auth', ?, ?, ?, ?, ?)`,
		hash(code), agent, clientID, redirect, challenge, o.now().Add(authCodeTTL).UnixMilli())
	return code, err
}

// Tokens is a token response.
type Tokens struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
}

// ExchangeAuthCode verifies PKCE and issues tokens.
func (o *OAuth) ExchangeAuthCode(ctx context.Context, code, clientID, redirect, verifier string) (Tokens, error) {
	agent, cid, red, challenge, err := o.takeCode(ctx, code, "auth")
	if err != nil {
		return Tokens{}, err
	}
	if cid != clientID || red != redirect || pkce(verifier) != challenge {
		return Tokens{}, errInvalid
	}
	return o.issue(ctx, agent, clientID)
}

// Refresh rotates a refresh token.
func (o *OAuth) Refresh(ctx context.Context, refresh, clientID string) (Tokens, error) {
	var agent, cid string
	var exp int64
	err := o.db.QueryRowContext(ctx, `DELETE FROM gw_tokens WHERE hash = ? AND kind = 'refresh' RETURNING agent, client_id, expires_at`, hash(refresh)).Scan(&agent, &cid, &exp)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (o.now().UnixMilli() > exp || cid != clientID)) {
		return Tokens{}, errInvalid
	}
	if err != nil {
		return Tokens{}, err
	}
	return o.issue(ctx, agent, clientID)
}

func (o *OAuth) issue(ctx context.Context, agent, clientID string) (Tokens, error) {
	t := Tokens{AccessToken: secret(), TokenType: "Bearer", ExpiresIn: int(accessTTL.Seconds()), RefreshToken: secret()}
	now := o.now()
	for _, row := range []struct {
		tok, kind string
		ttl       time.Duration
	}{{t.AccessToken, "access", accessTTL}, {t.RefreshToken, "refresh", refreshTTL}} {
		if _, err := o.db.ExecContext(ctx, `INSERT INTO gw_tokens(hash, kind, agent, client_id, expires_at) VALUES (?, ?, ?, ?, ?)`,
			hash(row.tok), row.kind, agent, clientID, now.Add(row.ttl).UnixMilli()); err != nil {
			return Tokens{}, err
		}
	}
	return t, nil
}

// Validate returns the agent an access token belongs to.
func (o *OAuth) Validate(ctx context.Context, access string) (string, error) {
	var agent string
	var exp int64
	err := o.db.QueryRowContext(ctx, `SELECT agent, expires_at FROM gw_tokens WHERE hash = ? AND kind = 'access'`, hash(access)).Scan(&agent, &exp)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && o.now().UnixMilli() > exp) {
		return "", errInvalid
	}
	return agent, err
}

// RevokeAgent deletes every token and pending code for an agent.
func (o *OAuth) RevokeAgent(ctx context.Context, agent string) error {
	if _, err := o.db.ExecContext(ctx, `DELETE FROM gw_tokens WHERE agent = ?`, agent); err != nil {
		return err
	}
	_, err := o.db.ExecContext(ctx, `DELETE FROM gw_codes WHERE agent = ?`, agent)
	return err
}

func pkce(verifier string) string {
	h := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

const codeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

func shortCode() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	out := make([]byte, 0, 9)
	for i, c := range b {
		if i == 4 {
			out = append(out, '-')
		}
		out = append(out, codeAlphabet[int(c)%len(codeAlphabet)])
	}
	return string(out)
}
