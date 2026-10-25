// Test plugin of the plugin host tests: every export and host function,
// plus misbehavior (endless loop, panic, memory and output bombs, unsafe
// HTML).
package main

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	gp "github.com/theinvisible/gopress/pdk/go"
)

func init() {
	gp.HandleShortcode("hello", func(r gp.ShortcodeRequest) (gp.Render, error) {
		var n int
		gp.KVGet("counter", "hits", &n)
		n++
		if err := gp.KVSet("counter", "hits", n); err != nil {
			return gp.Render{}, err
		}
		set, err := gp.Settings("settings")
		if err != nil {
			return gp.Render{}, err
		}
		return gp.Render{HTML: fmt.Sprintf(`<span class="hello" data-n="%d">%s, %s!</span>`, n, set["greeting"], r.Attr("name", "world")),
			Assets: []string{"assets/hello.css", "assets/hello.js"}}, nil
	})
	gp.HandleShortcode("evil", func(r gp.ShortcodeRequest) (gp.Render, error) {
		return gp.Render{HTML: `<p onclick="x()">a</p><script>alert(1)</script><a href="javascript:alert(1)">b</a>` +
			`<iframe src="https://evil.example"></iframe><form action="https://www.paypal.com/cgi-bin/webscr" method="post">` +
			`<input type="hidden" name="cmd" value="_donations"><button data-x="1" aria-label="d">Donate</button></form>`,
			Assets: []string{"assets/undeclared.js"}}, nil
	})
	gp.HandleShortcode("loop", func(gp.ShortcodeRequest) (gp.Render, error) {
		for {
		}
	})
	gp.HandleShortcode("panic", func(gp.ShortcodeRequest) (gp.Render, error) {
		var m map[string]int
		m["x"] = 1
		return gp.Render{}, nil
	})
	gp.HandleShortcode("alloc", func(gp.ShortcodeRequest) (gp.Render, error) {
		b := make([]byte, 200<<20)
		b[len(b)-1] = 1
		return gp.Render{HTML: fmt.Sprint(len(b))}, nil
	})
	gp.HandleShortcode("big", func(gp.ShortcodeRequest) (gp.Render, error) {
		return gp.Render{HTML: strings.Repeat("x", 3<<20)}, nil
	})
	gp.HandleShortcode("slow", func(gp.ShortcodeRequest) (gp.Render, error) {
		start := time.Now() // time.Sleep returns at once under WASI
		for time.Since(start) < 200*time.Millisecond {
		}
		return gp.Render{HTML: "slow"}, nil
	})
	gp.HandleShortcode("tpl", func(r gp.ShortcodeRequest) (gp.Render, error) {
		out, err := gp.Template("greet.html", map[string]string{"name": r.Attr("name", "you"), "url": r.Attr("url", "/")})
		return gp.Render{HTML: out}, err
	})
	gp.HandleShortcode("fail", func(gp.ShortcodeRequest) (gp.Render, error) {
		return gp.Render{}, errors.New("deliberate failure")
	})
	gp.HandleShortcode("op", func(r gp.ShortcodeRequest) (gp.Render, error) {
		// Runs a host operation given by the test and reports the result.
		var out any
		var err error
		switch r.Attr("do", "") {
		case "fetch":
			out, err = gp.Fetch(gp.HTTPRequest{Method: r.Attr("method", "GET"), URL: r.Attr("url", ""),
				Headers: map[string]string{r.Attr("hname", "X-Test"): r.Attr("hvalue", "1")}})
		case "post":
			out, err = gp.GetPost(10)
		case "posts":
			var list []gp.Post
			var total int
			list, total, err = gp.Posts("post", r.Attr("search", ""), 10, 0)
			out = map[string]any{"posts": list, "total": total}
		case "media":
			out, err = gp.GetMedia(20, 21)
		case "comment":
			out, err = gp.GetComment(1)
		case "spam":
			err = gp.SetCommentStatus(1, gp.Spam)
		case "stash":
			out, err = gp.Stash(r.Attr("kind", ""))
		case "kvbig":
			err = gp.KVSet("big", r.Attr("key", "k"), strings.Repeat("y", 900<<10))
		case "kvlist":
			var entries []gp.Entry
			entries, _, err = gp.KVList(r.Attr("collection", ""), "", 10)
			out = entries
		case "kvbad":
			err = gp.KVSet("_settings", "settings", map[string]any{"greeting": "hacked"})
		case "site":
			out, err = gp.Site()
		case "now":
			out = time.Now().UTC().Format(time.RFC3339)
		case "random":
			b := make([]byte, 16)
			rand.Read(b)
			out = b
		case "fetchtimeout":
			ms, _ := strconv.Atoi(r.Attr("ms", "0"))
			out, err = gp.Fetch(gp.HTTPRequest{URL: r.Attr("url", ""), TimeoutMS: ms})
		case "mail":
			err = gp.MailAdmins(r.Attr("subject", "Hi"), r.Attr("body", "Body"))
		case "notify":
			err = gp.NotifyAdmins(r.Attr("text", "Hello"), r.Attr("kind", ""), r.Attr("cap", ""))
		case "livepub":
			err = gp.LivePublish(r.Attr("page", "app"), map[string]string{"n": r.Attr("n", "1"), "big": strings.Repeat("<", len(r.Attr("big", "")))})
		case "progress":
			err = gp.Progress(50, "half")
		case "setsettings":
			err = gp.SetSettings("settings", map[string]any{"greeting": "x"})
		}
		if err != nil {
			return gp.Render{HTML: "error: " + err.Error()}, nil
		}
		b, _ := json.Marshal(out)
		return gp.Render{HTML: string(b)}, nil
	})
	gp.HandleWidget("tester_widget", func(r gp.WidgetRequest) (gp.Render, error) {
		return gp.Render{HTML: r.TitleHTML + fmt.Sprintf("<p>widget %v</p>", r.Settings["text"])}, nil
	})
	gp.HandleFilter("the_content", func(r gp.FilterRequest) (gp.FilterResult, error) {
		return gp.FilterResult{Value: r.Value + "<p class=\"tester-footer\">filtered</p>"}, nil
	})
	gp.HandleComment(func(c gp.CommentCheck) (string, error) {
		if err := gp.KVSet("events", "last_check", c); err != nil {
			return "", err
		}
		switch {
		case strings.Contains(c.Comment.Content, "viagra"):
			return gp.Spam, nil
		case strings.Contains(c.Comment.Content, "discard-me"):
			return gp.Discard, nil
		case strings.Contains(c.Comment.Content, "slow-check"):
			for {
			}
		}
		return gp.Keep, nil
	})
	gp.HandleAction("comment_status_changed", func(data json.RawMessage) error {
		return gp.KVSet("events", "last_status", data)
	})
	gp.HandleRoute("/ping", func(r gp.Request) gp.Response {
		return gp.Response{Status: 200, Headers: map[string]string{"Content-Type": "application/json", "Set-Cookie": "x=1",
			"Location": "https://evil.example"}, Body: fmt.Sprintf(`{"method":%q,"body":%q,"q":%q}`, r.Method, r.Body, r.Query["a"])}
	})
	gp.HandleTable("items", func(r gp.AdminRequest) (gp.Table, error) {
		return gp.Table{Rows: []gp.Row{{ID: "1", Cells: map[string]string{"name": "<b>One</b>"}}}, Total: 1,
			Actions: []gp.Option{{Value: "delete", Label: "Delete"}}}, nil
	})
	gp.HandleSettingsSaved("settings", func(r gp.AdminRequest) (string, error) {
		st, err := gp.Settings("settings")
		if err != nil {
			return "", err
		}
		if st["greeting"] == "bad" {
			return "", errors.New("the greeting is bad")
		}
		return fmt.Sprintf("checked by %s", r.User.Login), nil
	})
	gp.HandleSubmit("items", func(r gp.AdminRequest) (string, error) {
		return fmt.Sprintf("%s %v by %s", r.Action, r.IDs, r.User.Login), nil
	})
	gp.HandleAPI("app", func(r gp.AdminRequest) (any, error) {
		return map[string]any{"method": r.Method, "params": r.Params, "user": r.User.Login}, nil
	})
	gp.HandleDashboard("stats", func(r gp.DashboardRequest) (gp.Render, error) {
		return gp.Render{HTML: "<p>stats for " + r.User.Login + "<script>x</script></p>"}, nil
	})
	gp.HandleMigrate(func() (string, error) {
		if err := gp.Progress(10, "starting"); err != nil {
			return "", err
		}
		if err := gp.SetSettings("settings", map[string]any{"greeting": "Imported", "count": 5}); err != nil {
			return "", err
		}
		if err := gp.SetSettings("settings", map[string]any{"count": 99}); err == nil {
			return "", errors.New("out of range settings accepted")
		}
		if err := gp.SetSettings("settings", map[string]any{"evil": 1}); err == nil {
			return "", errors.New("unknown field accepted")
		}
		items, err := gp.Stash("post:tester_item")
		if err != nil {
			return "", err
		}
		for k, v := range items {
			if err := gp.KVSet("items", k, v); err != nil {
				return "", err
			}
		}
		return fmt.Sprintf("%d items imported", len(items)), nil
	})
}

func main() {}
