package edgeauth

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// ReplayExecer is the part of *sql.DB (or *sql.Tx, *sql.Conn) SQLReplayGuard
// needs. It is satisfied by every database/sql handle.
type ReplayExecer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

var replayTableName = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

// SQLReplayGuard is a ReplayGuard backed by a PostgreSQL table, so every
// replica and every restart of an origin shares one nonce store. Create the
// table with ReplayTableDDL.
type SQLReplayGuard struct {
	db    ReplayExecer
	ttl   time.Duration
	query string
	now   func() time.Time
}

// ReplayTableDDL returns the statement that creates the nonce table. Expired
// rows are overwritten in place by Seen, so the table stays bounded by the
// number of distinct (kid, nonce) pairs seen within ttl plus unreclaimed
// rows; a periodic `DELETE ... WHERE expires_at <= now()` keeps it small.
func ReplayTableDDL(table string) (string, error) {
	if !replayTableName.MatchString(table) {
		return "", fmt.Errorf("edgeauth: invalid replay table name %q", table)
	}
	return "CREATE TABLE IF NOT EXISTS " + table + " (" +
		"nonce_key text PRIMARY KEY, expires_at timestamptz NOT NULL)", nil
}

// NewSQLReplayGuard returns a ReplayGuard that remembers each (kid, nonce)
// pair for ttl in table. Pass 2 × Config.SignatureWindow (or more) as ttl; a
// ttl of 0 uses 2 × the default 5-minute window. Seen reports true (fail
// closed) when the store errors or the arguments are unusable.
func NewSQLReplayGuard(db ReplayExecer, table string, ttl time.Duration) (*SQLReplayGuard, error) {
	if db == nil {
		return nil, fmt.Errorf("edgeauth: replay database is required")
	}
	if !replayTableName.MatchString(table) {
		return nil, fmt.Errorf("edgeauth: invalid replay table name %q", table)
	}
	if ttl <= 0 {
		ttl = 2 * defaultSignatureWindow
	}
	// One atomic statement: a fresh key inserts, an expired key is reclaimed,
	// an unexpired key matches no row, so RowsAffected is 0 for a replay.
	q := "INSERT INTO " + table + " (nonce_key, expires_at) VALUES ($1, $2) " +
		"ON CONFLICT (nonce_key) DO UPDATE SET expires_at = EXCLUDED.expires_at " +
		"WHERE " + table + ".expires_at <= $3"
	return &SQLReplayGuard{db: db, ttl: ttl, query: q, now: time.Now}, nil
}

// Seen implements ReplayGuard.
func (g *SQLReplayGuard) Seen(ctx context.Context, kid, nonce string) bool {
	nonce = strings.TrimSpace(nonce)
	if nonce == "" {
		return true
	}
	now := g.now()
	res, err := g.db.ExecContext(ctx, g.query, strings.TrimSpace(kid)+"\x00"+nonce, now.Add(g.ttl), now)
	if err != nil {
		return true
	}
	n, err := res.RowsAffected()
	if err != nil {
		return true
	}
	return n == 0
}
