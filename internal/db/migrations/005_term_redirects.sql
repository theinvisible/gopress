-- Old slugs of categories and tags: their archive URLs redirect to the
-- new ones (posts have slug_redirects).
CREATE TABLE term_slug_redirects (
	taxonomy TEXT NOT NULL,
	old_slug TEXT NOT NULL,
	term_id  INTEGER NOT NULL REFERENCES terms (id) ON DELETE CASCADE,
	PRIMARY KEY (taxonomy, old_slug)
);
