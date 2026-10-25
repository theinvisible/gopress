# Photo Slider & Lightbox

Photo galleries as a slider with a lightbox. This is the GoPress port of the WordPress plugin "Foto Slider & Lightbox", and it is bundled with GoPress.

- **Shortcode:** `[fotogalerie id="12"]` shows gallery 12. `captions="1"`/`"0"` overrides the gallery's caption setting.
- **Slider:** one image at a time, with arrows, dots and the keyboard.
- **Lightbox:** a click on an image opens it. It has zoom, a thumbnail strip and a counter. ESC closes it, focus stays inside while it is open, and it respects reduced motion.
- **Admin:** "Photo Galleries" in the admin menu lists, creates, edits and deletes galleries. Images come from the media library, and you sort them by dragging or with the arrow buttons. Authors edit their own galleries; editors and administrators edit all of them.
- **Editor:** the "Photo gallery" button inserts the shortcode of a chosen gallery.
- **Import:** on activation, the galleries of the WordPress plugin (`fslb_gallery` posts with their image ids and caption setting) are imported with their ids. Existing shortcodes therefore keep working.

## Changes from the WordPress plugin

- The gallery data travels in data attributes of the slides, not in a JSON `<script>` (GoPress does not let plugin HTML contain scripts). The lightbox only uses http(s) and same-site image URLs.
- Galleries have no draft state. Images that are not in the media library, or that belong to unpublished posts, are left out.
- The block editor's block became the shortcode during the WordPress import. Its alignment and anchor options are gone.

## Permissions

- **Data:** reads media details (`media:read`) and the imported `fslb_gallery` posts.
- **Frontend assets:** Swiper and the plugin's script and stylesheet, only on pages that show a gallery.

## Build

`gopress plugin-pack plugins/foto-slider-lightbox` (or `make plugins` for all bundled plugins). The tests run with `go test` in this directory.
