-- Navigation menus (WordPress keeps them as nav_menu terms and
-- nav_menu_item posts). Theme locations are in the option
-- nav_menu_locations ({"primary": menu id}).
CREATE TABLE menus (
	id   INTEGER PRIMARY KEY,
	name TEXT NOT NULL
);

CREATE TABLE menu_items (
	id         INTEGER PRIMARY KEY,
	menu_id    INTEGER NOT NULL REFERENCES menus (id) ON DELETE CASCADE,
	parent_id  INTEGER NOT NULL DEFAULT 0,
	position   INTEGER NOT NULL DEFAULT 0,
	kind       TEXT NOT NULL,             -- page, post, category, post_tag, custom
	object_id  INTEGER NOT NULL DEFAULT 0,
	title      TEXT NOT NULL DEFAULT '',  -- label; empty: the object's title
	url        TEXT NOT NULL DEFAULT '',  -- custom links
	attr_title TEXT NOT NULL DEFAULT '',
	target     TEXT NOT NULL DEFAULT '',
	classes    TEXT NOT NULL DEFAULT ''
);
CREATE INDEX menu_items_menu ON menu_items (menu_id, position);
CREATE INDEX menu_items_object ON menu_items (kind, object_id);
