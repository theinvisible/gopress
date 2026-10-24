package db

import (
	"context"
	"errors"
)

// AccessToken is a personal access token (MCP interface).
type AccessToken struct {
	ID       int64
	UserID   int64
	Name     string
	Prefix   string
	Hash     []byte
	Scopes   string // space separated
	Created  int64
	Expires  int64
	LastUsed int64
	LastIP   string
}

// ErrTooManyTokens is returned by InsertAccessToken when the user already
// has max tokens.
var ErrTooManyTokens = errors.New("db: too many access tokens")

const accessTokenCols = `id, user_id, name, prefix, hash, scopes, created, expires, last_used, last_ip`

func scanAccessToken(s interface{ Scan(...any) error }) (*AccessToken, error) {
	t := &AccessToken{}
	err := s.Scan(&t.ID, &t.UserID, &t.Name, &t.Prefix, &t.Hash, &t.Scopes, &t.Created, &t.Expires, &t.LastUsed, &t.LastIP)
	if err != nil {
		return nil, err
	}
	return t, nil
}

// InsertAccessToken stores t unless the user has max tokens already (run
// it in a transaction so the check and the insert are one step).
func InsertAccessToken(ctx context.Context, q Querier, t *AccessToken, max int) error {
	var n int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM access_tokens WHERE user_id = ?`, t.UserID).Scan(&n); err != nil {
		return err
	}
	if n >= max {
		return ErrTooManyTokens
	}
	res, err := q.ExecContext(ctx, `INSERT INTO access_tokens (user_id, name, prefix, hash, scopes, created, expires)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, t.UserID, t.Name, t.Prefix, t.Hash, t.Scopes, t.Created, t.Expires)
	if err != nil {
		return err
	}
	t.ID, err = res.LastInsertId()
	return err
}

// AccessTokenByHash returns the token with the given hash.
func AccessTokenByHash(ctx context.Context, q Querier, hash []byte) (*AccessToken, error) {
	return scanAccessToken(q.QueryRowContext(ctx, `SELECT `+accessTokenCols+` FROM access_tokens WHERE hash = ?`, hash))
}

// AccessTokens returns the tokens of a user, newest first.
func AccessTokens(ctx context.Context, q Querier, userID int64) ([]*AccessToken, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+accessTokenCols+` FROM access_tokens WHERE user_id = ? ORDER BY id DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*AccessToken
	for rows.Next() {
		t, err := scanAccessToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeleteAccessToken removes a token of a user and reports whether it
// existed.
func DeleteAccessToken(ctx context.Context, q Querier, userID, id int64) (bool, error) {
	res, err := q.ExecContext(ctx, `DELETE FROM access_tokens WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// DeleteAccessTokens removes all tokens of a user and returns how many
// there were.
func DeleteAccessTokens(ctx context.Context, q Querier, userID int64) (int64, error) {
	res, err := q.ExecContext(ctx, `DELETE FROM access_tokens WHERE user_id = ?`, userID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// TouchAccessToken records the last use of a token.
func TouchAccessToken(ctx context.Context, q Querier, id, now int64, ip string) error {
	_, err := q.ExecContext(ctx, `UPDATE access_tokens SET last_used = ?, last_ip = ? WHERE id = ?`, now, ip, id)
	return err
}

// PruneAccessTokens deletes tokens that expired before cutoff.
func PruneAccessTokens(ctx context.Context, q Querier, cutoff int64) error {
	_, err := q.ExecContext(ctx, `DELETE FROM access_tokens WHERE expires < ?`, cutoff)
	return err
}
