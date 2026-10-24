# Hello

The example plugin of GoPress. It shows every part of a plugin in a few
lines: a shortcode (`[hello name="Ti"]`) with a stylesheet and a script, a
widget, a settings page, a custom admin page in a sandboxed iframe that
uses the bridge (`gopress.call`, `gopress.media.pick`), a route
(`/plugins/hello/stats`), a dashboard box, a content filter and an editor
button.

Build: `gopress plugin-pack plugins/hello`, then upload the zip under
Plugins › Upload Plugin. See `docs/plugins.md`.
