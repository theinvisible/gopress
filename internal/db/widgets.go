package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// InactiveWidgets is the sidebar of widgets that are kept but not shown.
const InactiveWidgets = "wp_inactive_widgets"

// AllWidgets returns all widgets grouped by sidebar, in display order.
func AllWidgets(ctx context.Context, q Querier) (map[string][]Widget, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, sidebar, position, type, settings FROM widgets ORDER BY sidebar, position, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]Widget{}
	for rows.Next() {
		var w Widget
		var settings string
		if err := rows.Scan(&w.ID, &w.Sidebar, &w.Position, &w.Type, &settings); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(settings), &w.Settings); err != nil {
			return nil, fmt.Errorf("widget %s settings: %w", w.ID, err)
		}
		out[w.Sidebar] = append(out[w.Sidebar], w)
	}
	return out, rows.Err()
}

// GetWidget returns one widget.
func GetWidget(ctx context.Context, q Querier, id string) (*Widget, error) {
	var w Widget
	var settings string
	err := q.QueryRowContext(ctx, `SELECT id, sidebar, position, type, settings FROM widgets WHERE id = ?`, id).
		Scan(&w.ID, &w.Sidebar, &w.Position, &w.Type, &settings)
	if err != nil {
		return nil, err
	}
	return &w, json.Unmarshal([]byte(settings), &w.Settings)
}

// UpdateWidgetSettings replaces the settings of a widget.
func UpdateWidgetSettings(ctx context.Context, q Querier, id string, settings map[string]any) error {
	b, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	res, err := q.ExecContext(ctx, `UPDATE widgets SET settings = ? WHERE id = ?`, string(b), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// DeleteWidget removes a widget.
func DeleteWidget(ctx context.Context, q Querier, id string) error {
	_, err := q.ExecContext(ctx, `DELETE FROM widgets WHERE id = ?`, id)
	return err
}

// NextWidgetID returns a free id for a new widget of typ ("search-3").
func NextWidgetID(ctx context.Context, q Querier, typ string) (string, error) {
	rows, err := q.QueryContext(ctx, `SELECT id FROM widgets WHERE type = ?`, typ)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	max := 1
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return "", err
		}
		if i := strings.LastIndexByte(id, '-'); i >= 0 {
			if n, err := strconv.Atoi(id[i+1:]); err == nil && n > max {
				max = n
			}
		}
	}
	return typ + "-" + strconv.Itoa(max+1), rows.Err()
}

// ErrUnknownWidget is returned by ArrangeWidgets for ids that do not exist.
var ErrUnknownWidget = errors.New("unknown widget")

// ArrangeWidgets moves the listed widgets into their sidebars in the given
// order (inside tx). Widgets that are not listed keep their place.
func ArrangeWidgets(ctx context.Context, tx *sql.Tx, layout map[string][]string) error {
	for sidebar, ids := range layout {
		for i, id := range ids {
			res, err := tx.ExecContext(ctx, `UPDATE widgets SET sidebar = ?, position = ? WHERE id = ?`, sidebar, i, id)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				return ErrUnknownWidget
			}
		}
	}
	return nil
}

// WidgetCount returns the number of widgets in a sidebar.
func WidgetCount(ctx context.Context, q Querier, sidebar string) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM widgets WHERE sidebar = ?`, sidebar).Scan(&n)
	return n, err
}
