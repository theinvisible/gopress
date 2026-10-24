// Command parity compares GoPress with the reference WordPress page by page.
//
//	go run ./dev/parity -ref http://127.0.0.1:8081 -got http://127.0.0.1:8080
//
// It collects URLs from the reference sitemaps plus archives, paging,
// comment pages, feeds and redirects, fetches each from both servers
// without following redirects and compares status, redirect target,
// <title>, body classes and normalized #content, #secondary and the menu.
// Diffs are written to local/parity/.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"net/http"

	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"golang.org/x/net/html"
)

var (
	ref     = flag.String("ref", "http://127.0.0.1:8081", "reference WordPress base URL")
	got     = flag.String("got", "http://127.0.0.1:8080", "GoPress base URL")
	out     = flag.String("out", "local/parity", "report directory")
	only    = flag.String("only", "", "only check paths containing this string")
	verbose = flag.Bool("v", false, "print every URL")
)

var client = &http.Client{
	Timeout:       30 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

type page struct {
	status   int
	location string
	ctype    string
	body     []byte
}

func fetch(base, path string) (*page, error) {
	resp, err := client.Get(base + path)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return &page{status: resp.StatusCode, location: resp.Header.Get("Location"), ctype: resp.Header.Get("Content-Type"), body: body}, nil
}

func main() {
	flag.Parse()
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fail(err)
	}
	paths, err := collect()
	if err != nil {
		fail(err)
	}
	var failures []string
	checked := 0
	for _, p := range paths {
		if *only != "" && !strings.Contains(p, *only) {
			continue
		}
		checked++
		diffs, err := compare(p)
		if err != nil {
			diffs = []string{"error: " + err.Error()}
		}
		if len(diffs) == 0 {
			if *verbose {
				fmt.Println("ok  ", p)
			}
			continue
		}
		failures = append(failures, p)
		fmt.Printf("DIFF %s: %s\n", p, strings.Join(firstLines(diffs), "; "))
		name := strings.Trim(regexp.MustCompile(`[^a-zA-Z0-9]+`).ReplaceAllString(p, "_"), "_")
		if name == "" {
			name = "home"
		}
		os.WriteFile(filepath.Join(*out, name+".diff"), []byte(strings.Join(diffs, "\n\n")), 0o644)
	}
	fmt.Printf("\n%d URLs checked, %d differ\n", checked, len(failures))
	if len(failures) > 0 {
		os.Exit(1)
	}
}

func firstLines(diffs []string) []string {
	var out []string
	for _, d := range diffs {
		out = append(out, strings.SplitN(d, "\n", 2)[0])
	}
	return out
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(2)
}

var reLoc = regexp.MustCompile(`<loc>([^<]+)</loc>`)

// collect builds the URL list from the reference site.
func collect() ([]string, error) {
	set := map[string]bool{}
	add := func(u string) {
		u = strings.TrimPrefix(u, *ref)
		if u == "" {
			u = "/"
		}
		set[u] = true
	}
	idx, err := fetch(*ref, "/wp-sitemap.xml")
	if err != nil {
		return nil, err
	}
	for _, m := range reLoc.FindAllStringSubmatch(string(idx.body), -1) {
		sub, err := fetch(*ref, strings.TrimPrefix(m[1], *ref))
		if err != nil {
			return nil, err
		}
		for _, u := range reLoc.FindAllStringSubmatch(string(sub.body), -1) {
			add(u[1])
		}
	}
	// Links found on the home page and paging.
	home, err := fetch(*ref, "/")
	if err != nil {
		return nil, err
	}
	for _, m := range regexp.MustCompile(`href=['"](`+regexp.QuoteMeta(*ref)+`/[^'"#?]*)['"]`).FindAllStringSubmatch(string(home.body), -1) {
		add(m[1])
	}
	for _, p := range []string{"/page/2/", "/page/3/", "/page/7/", "/page/99/", "/category/firewall/fortigate/page/2/",
		"/tag/linux/page/3/", "/2020/", "/2020/05/", "/2020/05/12/", "/1999/", "/author/ti/page/2/",
		"/feed/", "/comments/feed/", "/category/os/linux/feed/", "/tag/linux/feed/", "/2024/02/openfortigui-0-9-10/feed/",
		"/feed/atom/", "/feed/rss2/", "/?s=fortigate", "/?s=openfortigui+ubuntu", "/?s=zzzqqq", "/?s=fortigate&paged=2", "/page/2/?s=linux",
		"/?p=726", "/?page_id=124", "/?cat=30", "/?m=202402", "/?attachment_id=604", "/?feed=rss2", "/?author=1",
		"/nonexistent/", "/2024/02/openfortigui-0-9-10", "/i-moved-again/", "/2014/04/i-moved-again/", "/openfortigui-0-9-10/",
		"/linux/forticlient-sslvpn-deb-packages/comment-page-1/", "/linux/forticlient-sslvpn-deb-packages/comment-page-6/",
		"/linux/openfortigui/comment-page-2/", "/linux/tinyotp/tinyotp1-2/", "/linux/openfortigui/openfortigui1-2/",
		"/robots.txt", "/xmlrpc.php"} {
		set[p] = true
	}
	var paths []string
	for p := range set {
		if strings.HasPrefix(p, "/wp-") || strings.Contains(p, "/wp-json") || strings.Contains(p, "xmlrpc.php?") {
			continue
		}
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths, nil
}

// knownDifferences are paths whose comparison is intentionally skipped
// (WordPress-only endpoints).
var knownStatus = map[string]bool{
	"/xmlrpc.php":     true, // removed in GoPress (410)
	"/feed/atom/":     true, // GoPress redirects Atom/RDF feeds to RSS2
	"/?feed=rss2":     true, // GoPress redirects to the pretty feed URL
	"/i-moved-again/": true, // GoPress also redirects old slugs without the date prefix
}

func compare(path string) ([]string, error) {
	a, err := fetch(*ref, path)
	if err != nil {
		return nil, err
	}
	b, err := fetch(*got, path)
	if err != nil {
		return nil, err
	}
	if knownStatus[path] {
		return nil, nil
	}
	var diffs []string
	if a.status != b.status {
		diffs = append(diffs, fmt.Sprintf("status ref=%d got=%d", a.status, b.status))
	}
	if a.location != "" || b.location != "" {
		la, lb := normURL(a.location), normURL(b.location)
		if la != lb {
			diffs = append(diffs, fmt.Sprintf("redirect ref=%s got=%s", la, lb))
		}
	}
	if len(diffs) > 0 || a.status >= 300 || knownStatus[path] {
		return diffs, nil
	}
	switch {
	case strings.Contains(a.ctype, "html"):
		diffs = append(diffs, compareHTML(a.body, b.body)...)
	case strings.Contains(a.ctype, "rss"), strings.Contains(a.ctype, "xml"):
		diffs = append(diffs, compareFeed(a.body, b.body)...)
	}
	return diffs, nil
}

var hostRE = regexp.MustCompile(`https?://(127\.0\.0\.1:808[01]|hadler\.me|www\.hadler\.me)`)

func normURL(u string) string {
	u = hostRE.ReplaceAllString(u, "")
	if u == "" {
		return ""
	}
	return u
}

// --- HTML comparison ---

func compareHTML(a, b []byte) []string {
	da, err1 := html.Parse(bytes.NewReader(a))
	db, err2 := html.Parse(bytes.NewReader(b))
	if err1 != nil || err2 != nil {
		return []string{"parse error"}
	}
	var diffs []string
	ta, tb := text(find(da, func(n *html.Node) bool { return n.Data == "title" })), text(find(db, func(n *html.Node) bool { return n.Data == "title" }))
	if ta != tb {
		diffs = append(diffs, fmt.Sprintf("title ref=%q got=%q", ta, tb))
	}
	ca, cb := attr(find(da, tagIs("body")), "class"), attr(find(db, tagIs("body")), "class")
	if ca != cb {
		diffs = append(diffs, fmt.Sprintf("body class\nref: %s\ngot: %s", ca, cb))
	}
	for _, region := range []string{"content", "secondary", "site-navigation"} {
		ra, rb := find(da, idIs(region)), find(db, idIs(region))
		na, nb := normalize(ra), normalize(rb)
		if na != nb {
			diffs = append(diffs, fmt.Sprintf("#%s differs\n%s", region, lineDiff(na, nb)))
		}
	}
	return diffs
}

func tagIs(tag string) func(*html.Node) bool {
	return func(n *html.Node) bool { return n.Type == html.ElementNode && n.Data == tag }
}

func idIs(id string) func(*html.Node) bool {
	return func(n *html.Node) bool { return n.Type == html.ElementNode && attr(n, "id") == id }
}

func find(n *html.Node, pred func(*html.Node) bool) *html.Node {
	if n == nil {
		return nil
	}
	if pred(n) {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if f := find(c, pred); f != nil {
			return f
		}
	}
	return nil
}

func attr(n *html.Node, key string) string {
	if n == nil {
		return ""
	}
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func text(n *html.Node) string {
	if n == nil {
		return ""
	}
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.Join(strings.Fields(b.String()), " ")
}

// Attributes that only affect loading performance or are WordPress
// internals are ignored.
var ignoredAttrs = map[string]bool{"loading": true, "decoding": true, "fetchpriority": true, "data-wp-strategy": true}

// skipped returns true for subtrees excluded from the comparison: plugin
// output that GoPress renders in later milestones and spam-protection
// fields that differ by design.
func skipped(n *html.Node) bool {
	if n.Type != html.ElementNode {
		return false
	}
	id, class := attr(n, "id"), attr(n, "class")
	switch {
	case strings.HasPrefix(id, "paypal_donations-"): // M4 plugin
		return true
	case strings.Contains(class, "paypal-donations"), strings.Contains(class, "fslb-gallery"): // M4 plugins
		return true
	case strings.Contains(class, "akismet-fields-container"), id == "akismet_comment_nonce":
		return true
	case n.Data == "form" && strings.Contains(attr(n, "action"), "paypal.com"): // M4 plugin
		return true
	case n.Data == "input" && strings.HasPrefix(attr(n, "name"), "gp_"):
		return true
	case n.Data == "p" && n.FirstChild != nil && n.FirstChild.Type == html.ElementNode && (attr(n.FirstChild, "id") == "akismet_comment_nonce"):
		return true
	case n.Data == "p" && strings.Contains(attr(n, "style"), "display: none") && strings.Contains(attr(n, "aria-hidden"), "true"):
		return true
	case n.Data == "script":
		return true
	}
	return false
}

var reContainer = regexp.MustCompile(`\s*wp-container-[\w-]+|\s*wp-block-gallery-\d+`)

func normAttr(key, val string) string {
	val = strings.TrimSpace(val)
	switch key {
	case "href", "src", "action", "cite":
		return normURL(val)
	case "srcset":
		parts := strings.Split(val, ",")
		for i, p := range parts {
			parts[i] = normURL(strings.TrimSpace(p))
		}
		sort.Strings(parts)
		return strings.Join(parts, ", ")
	case "sizes":
		return strings.TrimPrefix(val, "auto, ")
	case "class":
		return strings.Join(strings.Fields(reContainer.ReplaceAllString(val, "")), " ")
	case "rel":
		f := strings.Fields(val)
		sort.Strings(f)
		return strings.Join(f, " ")
	}
	return val
}

// intentional maps GoPress output that differs from WordPress on purpose
// to the WordPress equivalent before comparing.
var intentional = strings.NewReplacer(
	`<a href="https://github.com/theinvisible/gopress">`+"\n", `<a href="https://wordpress.org/">`+"\n",
	"GoPress\n", "WordPress.org\n",
)

// normalize serializes a subtree canonically: one element per line, sorted
// attributes, collapsed whitespace, decoded text.
func normalize(n *html.Node) string {
	if n == nil {
		return "<missing>"
	}
	var b strings.Builder
	var walk func(*html.Node, int)
	walk = func(n *html.Node, depth int) {
		if skipped(n) {
			return
		}
		ind := strings.Repeat("  ", depth)
		switch n.Type {
		case html.TextNode:
			if t := strings.Join(strings.Fields(n.Data), " "); t != "" {
				b.WriteString(ind + t + "\n")
			}
			return
		case html.CommentNode:
			return
		case html.ElementNode:
			var attrs []string
			for _, a := range n.Attr {
				if ignoredAttrs[a.Key] {
					continue
				}
				v := normAttr(a.Key, a.Val)
				if a.Key == "class" && v == "" {
					continue
				}
				attrs = append(attrs, a.Key+"="+fmt.Sprintf("%q", v))
			}
			sort.Strings(attrs)
			b.WriteString(ind + "<" + n.Data)
			if len(attrs) > 0 {
				b.WriteString(" " + strings.Join(attrs, " "))
			}
			b.WriteString(">\n")
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c, depth+1)
		}
	}
	walk(n, 0)
	return intentional.Replace(b.String())
}

// lineDiff shows the first differing lines with some context.
func lineDiff(a, b string) string {
	la, lb := strings.Split(a, "\n"), strings.Split(b, "\n")
	i := 0
	for i < len(la) && i < len(lb) && la[i] == lb[i] {
		i++
	}
	start := max(0, i-3)
	var out strings.Builder
	fmt.Fprintf(&out, "first difference at line %d\n--- ref\n", i+1)
	for j := start; j < min(len(la), i+8); j++ {
		fmt.Fprintf(&out, "%s %s\n", mark(j == i), la[j])
	}
	out.WriteString("--- got\n")
	for j := start; j < min(len(lb), i+8); j++ {
		fmt.Fprintf(&out, "%s %s\n", mark(j == i), lb[j])
	}
	return out.String()
}

func mark(b bool) string {
	if b {
		return ">"
	}
	return " "
}

// --- feed comparison ---

var (
	reItemTitle = regexp.MustCompile(`<item>\s*<title>([^<]*)</title>\s*<link>([^<]*)</link>`)
	reGUID      = regexp.MustCompile(`<guid[^>]*>([^<]*)</guid>`)
)

func compareFeed(a, b []byte) []string {
	norm := func(body []byte) string {
		var lines []string
		for _, m := range reItemTitle.FindAllStringSubmatch(string(body), -1) {
			lines = append(lines, strings.Join(strings.Fields(html.UnescapeString(m[1])), " ")+" | "+normURL(m[2]))
		}
		for _, m := range reGUID.FindAllStringSubmatch(string(body), -1) {
			lines = append(lines, "guid "+normURL(m[1]))
		}
		return strings.Join(lines, "\n")
	}
	na, nb := norm(a), norm(b)
	if na != nb {
		return []string{"feed items differ\n" + lineDiff(na, nb)}
	}
	return nil
}
