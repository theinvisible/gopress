package db

import (
	"context"
	"database/sql"
)

// Menu is a navigation menu.
type Menu struct {
	ID   int64
	Name string
}

// MenuItem is an entry of a navigation menu.
type MenuItem struct {
	ID        int64
	MenuID    int64
	ParentID  int64
	Position  int
	Kind      string // page, post, category, post_tag, custom
	ObjectID  int64
	Title     string
	URL       string
	AttrTitle string
	Target    string
	Classes   string
}

// Menus returns all menus by name.
func Menus(ctx context.Context, q Querier) ([]Menu, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, name FROM menus ORDER BY name COLLATE NOCASE, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Menu
	for rows.Next() {
		var m Menu
		if err := rows.Scan(&m.ID, &m.Name); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// GetMenu returns one menu or sql.ErrNoRows.
func GetMenu(ctx context.Context, q Querier, id int64) (*Menu, error) {
	m := &Menu{}
	err := q.QueryRowContext(ctx, `SELECT id, name FROM menus WHERE id = ?`, id).Scan(&m.ID, &m.Name)
	if err != nil {
		return nil, err
	}
	return m, nil
}

// InsertMenu creates a menu.
func InsertMenu(ctx context.Context, q Querier, m *Menu) error {
	res, err := q.ExecContext(ctx, `INSERT INTO menus (name) VALUES (?)`, m.Name)
	if err != nil {
		return err
	}
	m.ID, err = res.LastInsertId()
	return err
}

// RenameMenu changes the name of a menu.
func RenameMenu(ctx context.Context, q Querier, id int64, name string) error {
	_, err := q.ExecContext(ctx, `UPDATE menus SET name = ? WHERE id = ?`, name, id)
	return err
}

// DeleteMenu deletes a menu with its items.
func DeleteMenu(ctx context.Context, q Querier, id int64) error {
	_, err := q.ExecContext(ctx, `DELETE FROM menus WHERE id = ?`, id)
	return err
}

// MenuItems returns the items of a menu in order.
func MenuItems(ctx context.Context, q Querier, menuID int64) ([]MenuItem, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, menu_id, parent_id, position, kind, object_id, title, url, attr_title, target, classes
		FROM menu_items WHERE menu_id = ? ORDER BY position, id`, menuID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MenuItem
	for rows.Next() {
		var it MenuItem
		if err := rows.Scan(&it.ID, &it.MenuID, &it.ParentID, &it.Position, &it.Kind, &it.ObjectID, &it.Title, &it.URL,
			&it.AttrTitle, &it.Target, &it.Classes); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// SaveMenuItems stores the items of a menu (inside tx): items with an id
// of this menu are updated, items without one are inserted (their id is
// set), all others of the menu are deleted. ParentIndex of each item is
// the index of its parent in items (-1 for top level).
func SaveMenuItems(ctx context.Context, tx *sql.Tx, menuID int64, items []MenuItem, parentIndex []int) error {
	keep := map[int64]bool{}
	for i := range items {
		it := &items[i]
		it.MenuID, it.Position, it.ParentID = menuID, i, 0
		if p := parentIndex[i]; p >= 0 && p < i {
			it.ParentID = items[p].ID
		}
		if it.ID != 0 {
			res, err := tx.ExecContext(ctx, `UPDATE menu_items SET parent_id = ?, position = ?, kind = ?, object_id = ?, title = ?,
				url = ?, attr_title = ?, target = ?, classes = ? WHERE id = ? AND menu_id = ?`, it.ParentID, it.Position, it.Kind,
				it.ObjectID, it.Title, it.URL, it.AttrTitle, it.Target, it.Classes, it.ID, menuID)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n == 1 {
				keep[it.ID] = true
				continue
			}
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO menu_items (menu_id, parent_id, position, kind, object_id, title, url, attr_title, target, classes)
			VALUES (?,?,?,?,?,?,?,?,?,?)`, menuID, it.ParentID, it.Position, it.Kind, it.ObjectID, it.Title, it.URL, it.AttrTitle, it.Target, it.Classes)
		if err != nil {
			return err
		}
		if it.ID, err = res.LastInsertId(); err != nil {
			return err
		}
		keep[it.ID] = true
	}
	existing, err := MenuItems(ctx, tx, menuID)
	if err != nil {
		return err
	}
	for _, it := range existing {
		if !keep[it.ID] {
			if _, err := tx.ExecContext(ctx, `DELETE FROM menu_items WHERE id = ?`, it.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

// DeleteMenuItemsFor removes the menu items that point to a deleted post
// or term; their sub items move up.
func DeleteMenuItemsFor(ctx context.Context, q Querier, kinds []string, objectID int64) error {
	for _, k := range kinds {
		rows, err := q.QueryContext(ctx, `SELECT id, parent_id FROM menu_items WHERE kind = ? AND object_id = ?`, k, objectID)
		if err != nil {
			return err
		}
		var pairs [][2]int64
		for rows.Next() {
			var id, parent int64
			if err := rows.Scan(&id, &parent); err != nil {
				rows.Close()
				return err
			}
			pairs = append(pairs, [2]int64{id, parent})
		}
		rows.Close()
		for _, p := range pairs {
			if _, err := q.ExecContext(ctx, `UPDATE menu_items SET parent_id = ? WHERE parent_id = ?`, p[1], p[0]); err != nil {
				return err
			}
			if _, err := q.ExecContext(ctx, `DELETE FROM menu_items WHERE id = ?`, p[0]); err != nil {
				return err
			}
		}
	}
	return nil
}
