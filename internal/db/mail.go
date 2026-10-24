package db

import (
	"context"
	"database/sql"
	"errors"
)

// Mail queue statuses.
const (
	MailPending = "pending"
	MailFailed  = "failed"
)

// QueuedMail is a message waiting in the mail queue.
type QueuedMail struct {
	ID        int64
	To        string
	Subject   string
	Body      string // plain-text body; headers are added at delivery
	Created   int64
	NextTry   int64
	Tries     int
	Status    string
	LastError string
}

// EnqueueMail adds a message to the queue.
func EnqueueMail(ctx context.Context, q Querier, m *QueuedMail) error {
	res, err := q.ExecContext(ctx, `INSERT INTO mail_queue (recipient, subject, body, created, next_try)
		VALUES (?, ?, ?, ?, ?)`, m.To, m.Subject, m.Body, m.Created, m.NextTry)
	if err != nil {
		return err
	}
	m.ID, err = res.LastInsertId()
	return err
}

const mailCols = `id, recipient, subject, body, created, next_try, tries, status, last_error`

func scanMails(rows *sql.Rows) ([]QueuedMail, error) {
	defer rows.Close()
	var out []QueuedMail
	for rows.Next() {
		var m QueuedMail
		if err := rows.Scan(&m.ID, &m.To, &m.Subject, &m.Body, &m.Created, &m.NextTry, &m.Tries, &m.Status, &m.LastError); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// DueMail returns up to limit pending messages due at now, oldest first.
func DueMail(ctx context.Context, q Querier, now int64, limit int) ([]QueuedMail, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+mailCols+` FROM mail_queue
		WHERE status = 'pending' AND next_try <= ? ORDER BY next_try, id LIMIT ?`, now, limit)
	if err != nil {
		return nil, err
	}
	return scanMails(rows)
}

// MailQueue lists the queue for the admin, newest first.
func MailQueue(ctx context.Context, q Querier, limit int) ([]QueuedMail, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+mailCols+` FROM mail_queue ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	return scanMails(rows)
}

// MailQueueCounts returns the number of messages per status.
func MailQueueCounts(ctx context.Context, q Querier) (map[string]int, error) {
	rows, err := q.QueryContext(ctx, `SELECT status, COUNT(*) FROM mail_queue GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var s string
		var n int
		if err := rows.Scan(&s, &n); err != nil {
			return nil, err
		}
		out[s] = n
	}
	return out, rows.Err()
}

// DeleteMail removes a message (sent or discarded).
func DeleteMail(ctx context.Context, q Querier, id int64) error {
	_, err := q.ExecContext(ctx, `DELETE FROM mail_queue WHERE id = ?`, id)
	return err
}

// MailAttemptFailed records a failed delivery: the message is retried at
// next, or marked failed when next is 0.
func MailAttemptFailed(ctx context.Context, q Querier, id, next int64, errMsg string) error {
	status := MailPending
	if next == 0 {
		status = MailFailed
	}
	_, err := q.ExecContext(ctx, `UPDATE mail_queue SET tries = tries + 1, next_try = ?, status = ?, last_error = ?
		WHERE id = ?`, next, status, errMsg, id)
	return err
}

// RetryMail puts a failed message back into the queue.
func RetryMail(ctx context.Context, q Querier, id, now int64) error {
	_, err := q.ExecContext(ctx, `UPDATE mail_queue SET status = 'pending', tries = 0, next_try = ? WHERE id = ?`, now, id)
	return err
}

// PruneMail deletes messages created before cutoff (their links have
// expired long ago).
func PruneMail(ctx context.Context, q Querier, cutoff int64) error {
	_, err := q.ExecContext(ctx, `DELETE FROM mail_queue WHERE created < ?`, cutoff)
	return err
}

// UserToken is a one-time link token.
type UserToken struct {
	Hash    []byte
	UserID  int64
	Purpose string
	Expires int64
	Bind    []byte
	Data    string
}

// InsertUserToken stores a token, replacing older tokens of the same
// user and purpose (only the newest link works).
func InsertUserToken(ctx context.Context, q Querier, t *UserToken) error {
	if _, err := q.ExecContext(ctx, `DELETE FROM user_tokens WHERE user_id = ? AND purpose = ?`, t.UserID, t.Purpose); err != nil {
		return err
	}
	_, err := q.ExecContext(ctx, `INSERT INTO user_tokens (hash, user_id, purpose, expires, bind, data)
		VALUES (?, ?, ?, ?, ?, ?)`, t.Hash, t.UserID, t.Purpose, t.Expires, t.Bind, t.Data)
	return err
}

// GetUserToken returns the token with the given hash and purpose.
func GetUserToken(ctx context.Context, q Querier, hash []byte, purpose string) (*UserToken, error) {
	t := &UserToken{}
	err := q.QueryRowContext(ctx, `SELECT hash, user_id, purpose, expires, bind, data FROM user_tokens
		WHERE hash = ? AND purpose = ?`, hash, purpose).Scan(&t.Hash, &t.UserID, &t.Purpose, &t.Expires, &t.Bind, &t.Data)
	if err != nil {
		return nil, err
	}
	return t, nil
}

// ConsumeUserToken deletes the token and reports whether it existed, so
// that of two concurrent uses only one succeeds.
func ConsumeUserToken(ctx context.Context, q Querier, hash []byte) (bool, error) {
	res, err := q.ExecContext(ctx, `DELETE FROM user_tokens WHERE hash = ?`, hash)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// DeleteUserTokens removes the tokens of a user (all purposes when
// purpose is empty).
func DeleteUserTokens(ctx context.Context, q Querier, userID int64, purpose string) error {
	_, err := q.ExecContext(ctx, `DELETE FROM user_tokens WHERE user_id = ? AND (? = '' OR purpose = ?)`, userID, purpose, purpose)
	return err
}

// PruneUserTokens deletes expired tokens.
func PruneUserTokens(ctx context.Context, q Querier, now int64) error {
	_, err := q.ExecContext(ctx, `DELETE FROM user_tokens WHERE expires < ?`, now)
	return err
}

// HasUserToken reports whether the user has an unexpired token of the
// purpose (e.g. a pending invitation).
func HasUserToken(ctx context.Context, q Querier, userID int64, purpose string, now int64) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM user_tokens WHERE user_id = ? AND purpose = ? AND expires >= ?`,
		userID, purpose, now).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return n > 0, err
}
