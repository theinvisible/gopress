#!/usr/bin/env bash
# Pulls a fresh copy of a live WordPress site into ./local (gitignored).
# Read-only on the server: the DB dump is streamed, nothing is written there.
#
# Configuration (environment):
#   WP_HOST          ssh target of the WordPress server (required), e.g. root@example.org
#   WP_DB_CONTAINER  Docker container running MariaDB/MySQL (default: wp-db);
#                    it must have MYSQL_USER/MYSQL_PASSWORD/MYSQL_DATABASE set
#   WP_CONTENT       wp-content directory on the server (default: /var/www/html/wp-content)
#   WP_PLUGINS       plugin directories to copy as reference (default: paypal-donations foto-slider-lightbox akismet)
#   WP_THEME         theme directory to copy as reference (default: twentytwelve)
set -euo pipefail

HOST="${WP_HOST:?set WP_HOST to the ssh target of the WordPress server, e.g. WP_HOST=root@example.org}"
DB_CONTAINER="${WP_DB_CONTAINER:-wp-db}"
WPC="${WP_CONTENT:-/var/www/html/wp-content}"
PLUGINS="${WP_PLUGINS:-paypal-donations foto-slider-lightbox akismet}"
THEME="${WP_THEME:-twentytwelve}"
OUT="$(cd "$(dirname "$0")/.." && pwd)/local"

mkdir -p "$OUT/uploads" "$OUT/wp-plugins" "$OUT/wp-themes"

echo ">> DB dump"
ssh "$HOST" "docker exec $DB_CONTAINER sh -c 'MYSQL_PWD=\"\$MYSQL_PASSWORD\" exec mariadb-dump -u\"\$MYSQL_USER\" --single-transaction --default-character-set=utf8mb4 \"\$MYSQL_DATABASE\"'" \
	| gzip > "$OUT/wp-dump.sql.gz.tmp"
mv "$OUT/wp-dump.sql.gz.tmp" "$OUT/wp-dump.sql.gz"

echo ">> uploads"
rsync -a --delete "$HOST:$WPC/uploads/" "$OUT/uploads/"

echo ">> plugin + theme sources (reference for ports)"
for p in $PLUGINS; do
	rsync -a --delete "$HOST:$WPC/plugins/$p/" "$OUT/wp-plugins/$p/"
done
rsync -a --delete "$HOST:$WPC/themes/$THEME/" "$OUT/wp-themes/$THEME/"

ls -lh "$OUT/wp-dump.sql.gz"
du -sh "$OUT/uploads"
