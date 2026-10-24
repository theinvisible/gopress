package db

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// AdminUserQuery selects users for the users list table.
type AdminUserQuery struct {
	Role    string
	Search  string // login, email, display name or URL contains
	OrderBy string // "login" (default), "email", "name", "registered", "posts"
	Desc    bool
	Limit   int
	Offset  int
}

// UserRow is a user with list-table extras.
type UserRow struct {
	User
	Posts     int   // posts and pages (not trashed)
	LastLogin int64 // unix time of the last successful login, 0 if none
}

func (uq AdminUserQuery) where() (string, []any) {
	conds := []string{"1 = 1"}
	var args []any
	if uq.Role != "" {
		conds = append(conds, "role = ?")
		args = append(args, uq.Role)
	}
	if s := strings.TrimSpace(uq.Search); s != "" {
		like := "%" + escapeLike(s) + "%"
		conds = append(conds, `(login LIKE ? ESCAPE '\' OR email LIKE ? ESCAPE '\' OR display_name LIKE ? ESCAPE '\' OR url LIKE ? ESCAPE '\')`)
		args = append(args, like, like, like, like)
	}
	return strings.Join(conds, " AND "), args
}

// AdminUsers returns one page of users and the total.
func AdminUsers(ctx context.Context, q Querier, uq AdminUserQuery) ([]UserRow, int, error) {
	where, args := uq.where()
	var total int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	dir := "ASC"
	if uq.Desc {
		dir = "DESC"
	}
	var order string
	switch uq.OrderBy {
	case "email":
		order = "email COLLATE NOCASE " + dir
	case "name":
		order = "display_name COLLATE NOCASE " + dir
	case "registered":
		order = "registered_gmt " + dir
	case "posts":
		order = "posts " + dir
	default:
		order = "login COLLATE NOCASE " + dir
	}
	query := `SELECT ` + userColumns + `,
		(SELECT COUNT(*) FROM posts p WHERE p.author_id = users.id AND p.type IN ('post', 'page') AND p.status NOT IN ('trash', 'auto-draft')) AS posts,
		(SELECT COALESCE(MAX(time), 0) FROM audit_log a WHERE a.user_id = users.id AND a.action = 'login') AS last_login
		FROM users WHERE ` + where + ` ORDER BY ` + order + `, id`
	if uq.Limit > 0 {
		query += " LIMIT ? OFFSET ?"
		args = append(args, uq.Limit, uq.Offset)
	}
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []UserRow
	for rows.Next() {
		var u UserRow
		if err := rows.Scan(&u.ID, &u.Login, &u.Nicename, &u.Email, &u.URL, &u.DisplayName, &u.Description, &u.PassHash,
			&u.Role, &u.RegisteredGMT, &u.Posts, &u.LastLogin); err != nil {
			return nil, 0, err
		}
		out = append(out, u)
	}
	return out, total, rows.Err()
}

// RoleCounts returns the number of users per role.
func RoleCounts(ctx context.Context, q Querier) (map[string]int, error) {
	rows, err := q.QueryContext(ctx, `SELECT role, COUNT(*) FROM users GROUP BY role`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var r string
		var n int
		if err := rows.Scan(&r, &n); err != nil {
			return nil, err
		}
		out[r] = n
	}
	return out, rows.Err()
}

// SetUserRole changes the role of a user.
func SetUserRole(ctx context.Context, q Querier, userID int64, role string) error {
	_, err := q.ExecContext(ctx, `UPDATE users SET role = ? WHERE id = ?`, role, userID)
	return err
}

// LoginTaken reports whether login, nicename or email is used by another
// user than except (case-insensitive).
func LoginTaken(ctx context.Context, q Querier, field, value string, except int64) (bool, error) {
	var query string
	switch field {
	case "login":
		query = `SELECT COUNT(*) FROM users WHERE login = ? COLLATE NOCASE AND id != ?`
	case "nicename":
		query = `SELECT COUNT(*) FROM users WHERE nicename = ? COLLATE NOCASE AND id != ?`
	case "email":
		query = `SELECT COUNT(*) FROM users WHERE email = ? COLLATE NOCASE AND id != ?`
	default:
		return false, errors.New("db: unknown user field")
	}
	var n int
	err := q.QueryRowContext(ctx, query, value, except).Scan(&n)
	return n > 0, err
}

// UserContentCount returns the number of posts of all types (including
// attachments and trash) written by the user.
func UserContentCount(ctx context.Context, q Querier, userID int64) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM posts WHERE author_id = ?`, userID).Scan(&n)
	return n, err
}

// ErrUserHasContent is returned by DeleteUser when the user's content
// must be attributed to someone else first.
var ErrUserHasContent = errors.New("the user has content")

// DeleteUser deletes a user. Their posts, pages and media, revisions and
// comments are attributed to reassignTo; without one, a user with
// content cannot be deleted. Sessions, factors, meta and tokens go with
// the user (foreign keys); autosaves are dropped. Run it in a
// transaction.
func DeleteUser(ctx context.Context, tx *sql.Tx, userID, reassignTo int64) error {
	if reassignTo == 0 {
		n, err := UserContentCount(ctx, tx, userID)
		if err != nil {
			return err
		}
		if n > 0 {
			return ErrUserHasContent
		}
	} else {
		if _, err := tx.ExecContext(ctx, `UPDATE posts SET author_id = ? WHERE author_id = ?`, reassignTo, userID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE revisions SET author_id = ? WHERE author_id = ? AND kind = 'revision'`, reassignTo, userID); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM revisions WHERE author_id = ? AND kind = 'autosave'`, userID); err != nil {
		return err
	}
	// Comments keep their author name and email but lose the account link.
	if _, err := tx.ExecContext(ctx, `UPDATE comments SET user_id = 0 WHERE user_id = ?`, userID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, userID)
	return err
}
