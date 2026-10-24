package db

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// GetUser returns the user with id, or sql.ErrNoRows.
func GetUser(ctx context.Context, q Querier, id int64) (*User, error) {
	us, err := queryUsers(ctx, q, `SELECT `+userColumns+` FROM users WHERE id = ?`, id)
	if err != nil {
		return nil, err
	}
	if len(us) == 0 {
		return nil, sql.ErrNoRows
	}
	return &us[0], nil
}

// UserByLogin finds a user by login name or email (case-insensitive, like
// wp_authenticate). Returns sql.ErrNoRows if there is none or the email is
// ambiguous.
func UserByLogin(ctx context.Context, q Querier, name string) (*User, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, sql.ErrNoRows
	}
	us, err := queryUsers(ctx, q, `SELECT `+userColumns+` FROM users WHERE login = ? COLLATE NOCASE`, name)
	if err != nil {
		return nil, err
	}
	if len(us) == 0 && strings.Contains(name, "@") {
		us, err = queryUsers(ctx, q, `SELECT `+userColumns+` FROM users WHERE email = ? COLLATE NOCASE`, name)
		if err != nil {
			return nil, err
		}
	}
	if len(us) != 1 {
		return nil, sql.ErrNoRows
	}
	return &us[0], nil
}

// SetUserPassHash stores a new password hash.
func SetUserPassHash(ctx context.Context, q Querier, userID int64, hash string) error {
	_, err := q.ExecContext(ctx, `UPDATE users SET pass_hash = ? WHERE id = ?`, hash, userID)
	return err
}

// UpdateUserProfile stores the editable profile fields of u.
func UpdateUserProfile(ctx context.Context, q Querier, u *User) error {
	_, err := q.ExecContext(ctx, `UPDATE users SET email = ?, url = ?, display_name = ?, description = ? WHERE id = ?`,
		u.Email, u.URL, u.DisplayName, u.Description, u.ID)
	return err
}

// UserMeta returns a user meta value ("" if unset).
func UserMeta(ctx context.Context, q Querier, userID int64, key string) (string, error) {
	var v string
	err := q.QueryRowContext(ctx, `SELECT value FROM user_meta WHERE user_id = ? AND key = ?`, userID, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// SetUserMeta sets a user meta value.
func SetUserMeta(ctx context.Context, q Querier, userID int64, key, value string) error {
	_, err := q.ExecContext(ctx, `INSERT INTO user_meta (user_id, key, value) VALUES (?,?,?)
		ON CONFLICT (user_id, key) DO UPDATE SET value = excluded.value`, userID, key, value)
	return err
}

// Session is a login session (see migrations/002_admin.sql).
type Session struct {
	IDHash    []byte
	UserID    int64
	State     string
	Created   int64
	LastSeen  int64
	Expires   int64
	IdleSecs  int64
	Remember  bool
	ReauthAt  int64
	Attempts  int
	IP        string
	UserAgent string
}

// Session states.
const (
	SessionMFA    = "mfa"    // password verified, second factor pending
	SessionEnroll = "enroll" // password verified, no second factor set up
	SessionFull   = "full"
)

const sessionColumns = `id_hash, user_id, state, created, last_seen, expires, idle_secs, remember, reauth_at, attempts, ip, user_agent`

func scanSession(sc interface{ Scan(...any) error }, s *Session) error {
	return sc.Scan(&s.IDHash, &s.UserID, &s.State, &s.Created, &s.LastSeen, &s.Expires, &s.IdleSecs,
		&s.Remember, &s.ReauthAt, &s.Attempts, &s.IP, &s.UserAgent)
}

// InsertSession stores a new session.
func InsertSession(ctx context.Context, q Querier, s *Session) error {
	_, err := q.ExecContext(ctx, `INSERT INTO sessions (`+sessionColumns+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		s.IDHash, s.UserID, s.State, s.Created, s.LastSeen, s.Expires, s.IdleSecs, s.Remember,
		s.ReauthAt, s.Attempts, s.IP, s.UserAgent)
	return err
}

// GetSession returns the session with idHash, or sql.ErrNoRows.
func GetSession(ctx context.Context, q Querier, idHash []byte) (*Session, error) {
	var s Session
	if err := scanSession(q.QueryRowContext(ctx, `SELECT `+sessionColumns+` FROM sessions WHERE id_hash = ?`, idHash), &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// UserSessions returns the full sessions of a user, most recent first.
func UserSessions(ctx context.Context, q Querier, userID int64) ([]Session, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+sessionColumns+` FROM sessions WHERE user_id = ? AND state = ? ORDER BY last_seen DESC`,
		userID, SessionFull)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		var s Session
		if err := scanSession(rows, &s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// TouchSession updates last_seen.
func TouchSession(ctx context.Context, q Querier, idHash []byte, now int64) error {
	_, err := q.ExecContext(ctx, `UPDATE sessions SET last_seen = ? WHERE id_hash = ?`, now, idHash)
	return err
}

// SetSessionReauth records a successful re-authentication.
func SetSessionReauth(ctx context.Context, q Querier, idHash []byte, now int64) error {
	_, err := q.ExecContext(ctx, `UPDATE sessions SET reauth_at = ? WHERE id_hash = ?`, now, idHash)
	return err
}

// IncSessionAttempts counts a failed second-factor attempt and returns the
// new count.
func IncSessionAttempts(ctx context.Context, q Querier, idHash []byte) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, `UPDATE sessions SET attempts = attempts + 1 WHERE id_hash = ? RETURNING attempts`, idHash).Scan(&n)
	return n, err
}

// DeleteSession removes one session.
func DeleteSession(ctx context.Context, q Querier, idHash []byte) error {
	_, err := q.ExecContext(ctx, `DELETE FROM sessions WHERE id_hash = ?`, idHash)
	return err
}

// DeleteUserSessions removes all sessions of a user except keep (may be nil).
func DeleteUserSessions(ctx context.Context, q Querier, userID int64, keep []byte) error {
	_, err := q.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ? AND id_hash != ?`, userID, keep)
	return err
}

// DeleteExpiredSessions removes sessions past their absolute or idle timeout.
func DeleteExpiredSessions(ctx context.Context, q Querier, now int64) error {
	_, err := q.ExecContext(ctx, `DELETE FROM sessions WHERE expires <= ? OR last_seen + idle_secs <= ?`, now, now)
	return err
}

// TOTP returns the encrypted TOTP secret and the last used time step of a
// user; ok is false if TOTP is not set up.
func TOTP(ctx context.Context, q Querier, userID int64) (secretEnc []byte, lastStep int64, ok bool, err error) {
	err = q.QueryRowContext(ctx, `SELECT secret_enc, last_step FROM totp WHERE user_id = ?`, userID).Scan(&secretEnc, &lastStep)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, 0, false, nil
	}
	return secretEnc, lastStep, err == nil, err
}

// SetTOTP stores (replaces) a user's TOTP secret.
func SetTOTP(ctx context.Context, q Querier, userID int64, secretEnc []byte, lastStep, now int64) error {
	_, err := q.ExecContext(ctx, `INSERT INTO totp (user_id, secret_enc, last_step, created) VALUES (?,?,?,?)
		ON CONFLICT (user_id) DO UPDATE SET secret_enc = excluded.secret_enc, last_step = excluded.last_step, created = excluded.created`,
		userID, secretEnc, lastStep, now)
	return err
}

// UseTOTPStep records step as used if it is newer than the last used one.
// It reports false for a replayed or older step.
func UseTOTPStep(ctx context.Context, q Querier, userID, step int64) (bool, error) {
	res, err := q.ExecContext(ctx, `UPDATE totp SET last_step = ? WHERE user_id = ? AND last_step < ?`, step, userID, step)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// DeleteTOTP removes TOTP for a user.
func DeleteTOTP(ctx context.Context, q Querier, userID int64) error {
	_, err := q.ExecContext(ctx, `DELETE FROM totp WHERE user_id = ?`, userID)
	return err
}

// WebAuthnCredential is a stored passkey.
type WebAuthnCredential struct {
	ID         []byte
	UserID     int64
	Name       string
	Credential string // JSON
	Created    int64
	LastUsed   int64
}

// WebAuthnCredentials returns the passkeys of a user.
func WebAuthnCredentials(ctx context.Context, q Querier, userID int64) ([]WebAuthnCredential, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, user_id, name, credential, created, last_used
		FROM webauthn_credentials WHERE user_id = ? ORDER BY created`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WebAuthnCredential
	for rows.Next() {
		var c WebAuthnCredential
		if err := rows.Scan(&c.ID, &c.UserID, &c.Name, &c.Credential, &c.Created, &c.LastUsed); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// InsertWebAuthnCredential stores a new passkey.
func InsertWebAuthnCredential(ctx context.Context, q Querier, c *WebAuthnCredential) error {
	_, err := q.ExecContext(ctx, `INSERT INTO webauthn_credentials (id, user_id, name, credential, created, last_used)
		VALUES (?,?,?,?,?,?)`, c.ID, c.UserID, c.Name, c.Credential, c.Created, c.LastUsed)
	return err
}

// UpdateWebAuthnCredential stores the updated credential JSON (sign count,
// flags) after a login.
func UpdateWebAuthnCredential(ctx context.Context, q Querier, id []byte, credential string, now int64) error {
	_, err := q.ExecContext(ctx, `UPDATE webauthn_credentials SET credential = ?, last_used = ? WHERE id = ?`, credential, now, id)
	return err
}

// RenameWebAuthnCredential renames a passkey of userID.
func RenameWebAuthnCredential(ctx context.Context, q Querier, userID int64, id []byte, name string) error {
	_, err := q.ExecContext(ctx, `UPDATE webauthn_credentials SET name = ? WHERE id = ? AND user_id = ?`, name, id, userID)
	return err
}

// DeleteWebAuthnCredential removes a passkey of userID.
func DeleteWebAuthnCredential(ctx context.Context, q Querier, userID int64, id []byte) error {
	_, err := q.ExecContext(ctx, `DELETE FROM webauthn_credentials WHERE id = ? AND user_id = ?`, id, userID)
	return err
}

// SetRecoveryCodes replaces a user's recovery codes.
func SetRecoveryCodes(ctx context.Context, q Querier, userID int64, hashes [][]byte) error {
	if _, err := q.ExecContext(ctx, `DELETE FROM recovery_codes WHERE user_id = ?`, userID); err != nil {
		return err
	}
	for _, h := range hashes {
		if _, err := q.ExecContext(ctx, `INSERT INTO recovery_codes (user_id, code_hash) VALUES (?,?)`, userID, h); err != nil {
			return err
		}
	}
	return nil
}

// UseRecoveryCode marks an unused code as used; it reports whether one
// matched.
func UseRecoveryCode(ctx context.Context, q Querier, userID int64, hash []byte, now int64) (bool, error) {
	res, err := q.ExecContext(ctx, `UPDATE recovery_codes SET used_at = ? WHERE user_id = ? AND code_hash = ? AND used_at = 0`,
		now, userID, hash)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// RecoveryCodesLeft counts the unused recovery codes of a user.
func RecoveryCodesLeft(ctx context.Context, q Querier, userID int64) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM recovery_codes WHERE user_id = ? AND used_at = 0`, userID).Scan(&n)
	return n, err
}

// DeleteSecondFactors removes all second factors of a user (CLI reset).
func DeleteSecondFactors(ctx context.Context, q Querier, userID int64) error {
	for _, t := range []string{"totp", "webauthn_credentials", "recovery_codes"} {
		if _, err := q.ExecContext(ctx, `DELETE FROM `+t+` WHERE user_id = ?`, userID); err != nil {
			return err
		}
	}
	return nil
}

// LoginFailure is the failed-login state of one login name.
type LoginFailure struct {
	Count       int
	LockedUntil int64
	Last        int64
}

// GetLoginFailure returns the state for key (zero if none).
func GetLoginFailure(ctx context.Context, q Querier, key string) (LoginFailure, error) {
	var f LoginFailure
	err := q.QueryRowContext(ctx, `SELECT count, locked_until, last FROM login_failures WHERE key = ?`, key).
		Scan(&f.Count, &f.LockedUntil, &f.Last)
	if errors.Is(err, sql.ErrNoRows) {
		return LoginFailure{}, nil
	}
	return f, err
}

// SetLoginFailure stores the state for key.
func SetLoginFailure(ctx context.Context, q Querier, key string, f LoginFailure) error {
	_, err := q.ExecContext(ctx, `INSERT INTO login_failures (key, count, locked_until, last) VALUES (?,?,?,?)
		ON CONFLICT (key) DO UPDATE SET count = excluded.count, locked_until = excluded.locked_until, last = excluded.last`,
		key, f.Count, f.LockedUntil, f.Last)
	return err
}

// ClearLoginFailure forgets the failures of key.
func ClearLoginFailure(ctx context.Context, q Querier, key string) error {
	_, err := q.ExecContext(ctx, `DELETE FROM login_failures WHERE key = ?`, key)
	return err
}

// PruneLoginFailures removes entries last touched before cutoff.
func PruneLoginFailures(ctx context.Context, q Querier, cutoff int64) error {
	_, err := q.ExecContext(ctx, `DELETE FROM login_failures WHERE last < ? AND locked_until < ?`, cutoff, cutoff)
	return err
}

// AuditEntry is one audit log line.
type AuditEntry struct {
	ID         int64
	Time       int64
	UserID     int64
	IP         string
	Action     string
	ObjectType string
	ObjectID   int64
	Detail     string // JSON object
}

// InsertAudit appends an audit log entry.
func InsertAudit(ctx context.Context, q Querier, e *AuditEntry) error {
	if e.Detail == "" {
		e.Detail = "{}"
	}
	_, err := q.ExecContext(ctx, `INSERT INTO audit_log (time, user_id, ip, action, object_type, object_id, detail)
		VALUES (?,?,?,?,?,?,?)`, e.Time, e.UserID, e.IP, e.Action, e.ObjectType, e.ObjectID, e.Detail)
	return err
}

// AuditLog returns the newest entries, optionally for one user (userID > 0).
func AuditLog(ctx context.Context, q Querier, userID int64, limit, offset int) ([]AuditEntry, error) {
	query := `SELECT id, time, user_id, ip, action, object_type, object_id, detail FROM audit_log`
	var args []any
	if userID > 0 {
		query += ` WHERE user_id = ?`
		args = append(args, userID)
	}
	query += ` ORDER BY id DESC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		if err := rows.Scan(&e.ID, &e.Time, &e.UserID, &e.IP, &e.Action, &e.ObjectType, &e.ObjectID, &e.Detail); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// PruneAudit removes entries older than cutoff.
func PruneAudit(ctx context.Context, q Querier, cutoff int64) error {
	_, err := q.ExecContext(ctx, `DELETE FROM audit_log WHERE time < ?`, cutoff)
	return err
}
