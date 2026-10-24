// Hello is the example plugin of GoPress: a shortcode with a stylesheet
// and a script, a widget, a settings page, a custom admin page in a
// sandboxed iframe, a route and an editor button. Build it with
//
//	gopress plugin-pack plugins/hello
package main

import (
	"encoding/json"
	"fmt"
	"html"
	"strings"

	gp "github.com/theinvisible/gopress/pdk/go"
)

func init() {
	// [hello name="Ti"] greets and counts how often it was rendered.
	gp.HandleShortcode("hello", func(r gp.ShortcodeRequest) (gp.Render, error) {
		set, err := gp.Settings("settings")
		if err != nil {
			return gp.Render{}, err
		}
		hits := bump()
		greeting, _ := set["greeting"].(string)
		return gp.Render{
			HTML: fmt.Sprintf(`<p class="hello-greeting" data-hits="%d">%s, %s!</p>`, hits,
				html.EscapeString(greeting), html.EscapeString(r.Attr("name", "world"))),
			Assets: []string{"assets/hello.css", "assets/hello.js"},
		}, nil
	})

	gp.HandleWidget("hello", func(r gp.WidgetRequest) (gp.Render, error) {
		var hits int
		gp.KVGet("stats", "hits", &hits)
		return gp.Render{HTML: r.TitleHTML + fmt.Sprintf(`<p class="hello-widget">The greeting was shown %d times.</p>`, hits),
			Assets: []string{"assets/hello.css"}}, nil
	})

	// GET /plugins/hello/stats answers with the counter as JSON.
	gp.HandleRoute("/stats", func(r gp.Request) gp.Response {
		var hits int
		gp.KVGet("stats", "hits", &hits)
		return gp.Response{Status: 200, Headers: map[string]string{"Content-Type": "application/json"},
			Body: fmt.Sprintf(`{"hits":%d}`, hits)}
	})

	// The "Hello board" page (admin/board.html) calls these through
	// gopress.call(method, params).
	gp.HandleAPI("board", func(r gp.AdminRequest) (any, error) {
		switch r.Method {
		case "stats":
			var hits int
			gp.KVGet("stats", "hits", &hits)
			var pinned []int64
			gp.KVGet("board", "pinned", &pinned)
			return map[string]any{"hits": hits, "pinned": pinned, "user": r.User.DisplayName}, nil
		case "pin":
			var ids []int64
			if err := json.Unmarshal(r.Params, &ids); err != nil || len(ids) > 50 {
				return nil, fmt.Errorf("invalid image list")
			}
			return true, gp.KVSet("board", "pinned", ids)
		case "reset":
			return true, gp.KVSet("stats", "hits", 0)
		}
		return nil, fmt.Errorf("unknown method %q", r.Method)
	})

	gp.HandleDashboard("summary", func(r gp.DashboardRequest) (gp.Render, error) {
		var hits int
		gp.KVGet("stats", "hits", &hits)
		return gp.Render{HTML: fmt.Sprintf("<p>Hello was shown <strong>%d</strong> times.</p>", hits)}, nil
	})

	gp.HandleFilter("the_content", func(r gp.FilterRequest) (gp.FilterResult, error) {
		set, err := gp.Settings("settings")
		if err != nil || set["signature"] != true || !strings.Contains(r.Value, "hello-greeting") {
			return gp.FilterResult{Value: r.Value}, err
		}
		return gp.FilterResult{Value: r.Value + `<p class="hello-signature">— greeted by the Hello plugin</p>`}, nil
	})
}

// bump increments the counter (a lost update under load does not matter
// for an example).
func bump() int {
	var hits int
	gp.KVGet("stats", "hits", &hits)
	hits++
	gp.KVSet("stats", "hits", hits)
	return hits
}

func main() {}
