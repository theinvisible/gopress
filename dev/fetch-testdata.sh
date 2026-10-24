#!/usr/bin/env bash
# Pulls a fresh copy of the live WordPress site into ./local (gitignored).
# Read-only on the server: the DB dump is streamed, nothing is written there.
set -euo pipefail

HOST="${WP_HOST:-root@hadler.me}"
WPC="/data/wordpress/wp-content"
OUT="$(cd "$(dirname "$0")/.." && pwd)/local"

mkdir -p "$OUT/uploads" "$OUT/wp-plugins" "$OUT/wp-themes"

echo ">> DB dump"
ssh "$HOST" 'docker exec wp-db sh -c '\''exec mariadb-dump -u"$MYSQL_USER" -p"$MYSQL_PASSWORD" --single-transaction --default-character-set=utf8mb4 "$MYSQL_DATABASE"'\''' \
	| gzip > "$OUT/wp-dump.sql.gz.tmp"
mv "$OUT/wp-dump.sql.gz.tmp" "$OUT/wp-dump.sql.gz"

echo ">> uploads"
rsync -a --delete "$HOST:$WPC/uploads/" "$OUT/uploads/"

echo ">> plugin + theme sources (reference for ports)"
for p in paypal-donations foto-slider-lightbox akismet; do
	rsync -a --delete "$HOST:$WPC/plugins/$p/" "$OUT/wp-plugins/$p/"
done
rsync -a --delete "$HOST:$WPC/themes/twentytwelve/" "$OUT/wp-themes/twentytwelve/"

ls -lh "$OUT/wp-dump.sql.gz"
du -sh "$OUT/uploads"
