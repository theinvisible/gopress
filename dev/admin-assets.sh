#!/bin/sh
# Copies the WordPress admin stylesheets (GPL-2.0-or-later) that the GoPress
# admin uses from a WordPress core checkout into web/admin/static/wp/,
# keeping the WordPress directory layout so relative url()s keep working.
# Images referenced by the stylesheets are copied too, except WordPress
# logos (trademark).
#
#   dev/admin-assets.sh [path/to/wordpress]   (default: local/wp-core)
set -eu
SRC=${1:-local/wp-core}
DST=web/admin/static/wp
[ -f "$SRC/wp-includes/version.php" ] || { echo "no WordPress core in $SRC" >&2; exit 1; }

rm -rf "$DST"
CSS="
wp-includes/css/dashicons.min.css
wp-includes/css/admin-bar.min.css
wp-includes/css/buttons.min.css
wp-includes/css/media-views.min.css
wp-includes/css/dist/base-styles/admin-schemes.min.css
wp-admin/css/common.min.css
wp-admin/css/forms.min.css
wp-admin/css/admin-menu.min.css
wp-admin/css/dashboard.min.css
wp-admin/css/list-tables.min.css
wp-admin/css/edit.min.css
wp-admin/css/revisions.min.css
wp-admin/css/media.min.css
wp-admin/css/l10n.min.css
wp-admin/css/login.min.css
wp-admin/css/wp-tooltip.min.css
"
for s in blue coffee ectoplasm light midnight modern ocean sunrise; do
	CSS="$CSS wp-admin/css/colors/$s/colors.min.css"
done
for f in $CSS; do
	mkdir -p "$DST/$(dirname "$f")"
	cp "$SRC/$f" "$DST/$f"
done
# Dashicons are embedded in dashicons.min.css as a data: URI (woff); the
# eot/ttf fallbacks are not shipped.
# Referenced images, resolved relative to each stylesheet.
for f in $CSS; do
	dir=$(dirname "$f")
	grep -o 'url([^)]*)' "$SRC/$f" | sed -e 's/^url(//' -e 's/)$//' -e "s/[\"']//g" -e 's/[?#].*//' |
		grep -v '^data:' | grep -v -i 'wordpress-logo\|w-logo' | grep -v 'fonts/dashicons' | sort -u |
		while read -r ref; do
			p=$(cd "$SRC/$dir" && realpath -m --relative-to="$(cd "$OLDPWD" && realpath "$SRC")" "$ref")
			[ -f "$SRC/$p" ] || continue
			mkdir -p "$DST/$(dirname "$p")"
			cp "$SRC/$p" "$DST/$p"
		done
done
ver=$(sed -n "s/^\$wp_version = '\(.*\)';/\1/p" "$SRC/wp-includes/version.php")
cat > "$DST/README.md" <<EOT
# WordPress admin styles

Copied from WordPress $ver by \`dev/admin-assets.sh\`. WordPress is free
software licensed under the GPL-2.0-or-later (https://wordpress.org/about/license/),
Dashicons under the GPL-2.0-or-later as well. Do not edit these files;
GoPress' own admin styles live in \`web/admin/static/admin.css\`.
EOT
du -sh "$DST"
