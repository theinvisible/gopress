package render

import (
	"fmt"
	"math"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/theinvisible/gopress/internal/db"
)

var (
	reImgOrIframe = regexp.MustCompile(`<(img|iframe)\s[^>]+>`)
	reWPImageID   = regexp.MustCompile(`(?i)wp-image-([0-9]+)`)
	reSrcAttr     = regexp.MustCompile(`src="([^"]+)"`)
	reWidthAttr   = regexp.MustCompile(` width="([0-9]+)"`)
	reHeightAttr  = regexp.MustCompile(` height="([0-9]+)"`)
	reImgClose    = regexp.MustCompile(`<img ([^>]+?)[/ ]*>`)
	reStyleWidth  = regexp.MustCompile(`style="width:\s*(\d+)px;"`)
)

// maxSrcsetWidth is WordPress' max_srcset_image_width.
const maxSrcsetWidth = 2048

// lazyThreshold is wp_omit_loading_attr_threshold(): the first images on a
// page are loaded eagerly.
const lazyThreshold = 3

// FilterContentTags is a port of wp_filter_content_tags(): images with a
// wp-image-N class get width/height and srcset/sizes from the media library,
// and all images get loading/decoding attributes.
func FilterContentTags(env *Env, content string) string {
	matches := reImgOrIframe.FindAllStringSubmatch(content, -1)
	if len(matches) == 0 {
		return content
	}
	ids := map[string]int64{}
	var idList []int64
	for _, m := range matches {
		if m[1] != "img" {
			continue
		}
		if c := reWPImageID.FindStringSubmatch(m[0]); c != nil {
			id, _ := strconv.ParseInt(c[1], 10, 64)
			ids[m[0]] = id
			idList = append(idList, id)
		}
	}
	media := map[int64]*db.Media{}
	if len(idList) > 0 && env.Q != nil {
		var err error
		if media, err = db.MediaByIDs(env.Ctx, env.Q, idList); err != nil {
			media = map[int64]*db.Media{}
		}
	}
	done := map[string]bool{}
	for _, m := range matches {
		tag := m[0]
		if done[tag] {
			continue
		}
		done[tag] = true
		filtered := tag
		if m[1] == "img" {
			if med := media[ids[tag]]; med != nil {
				if !strings.Contains(filtered, " width=") && !strings.Contains(filtered, " height=") {
					filtered = addWidthHeight(env, filtered, med)
				}
				if !strings.Contains(filtered, " srcset=") {
					filtered = addSrcsetAndSizes(env, filtered, med)
				}
			}
			filtered = env.addLoadingAttrs(filtered)
		} else if !strings.Contains(filtered, " loading=") {
			filtered = strings.Replace(filtered, "<iframe", `<iframe loading="lazy"`, 1)
		}
		if filtered != tag {
			content = strings.ReplaceAll(content, tag, filtered)
		}
	}
	return content
}

func imgAttr(re *regexp.Regexp, tag string) string {
	if m := re.FindStringSubmatch(tag); m != nil {
		return m[1]
	}
	return ""
}

// srcDimensions is wp_image_src_get_dimensions(): the size of the image
// file referenced by src.
func srcDimensions(src string, m *db.Media) (int, int, bool) {
	base := path.Base(strings.SplitN(src, "?", 2)[0])
	if base == path.Base(m.File) && m.Width > 0 {
		return m.Width, m.Height, true
	}
	for _, s := range m.Sizes {
		if s.File == base {
			return s.Width, s.Height, true
		}
	}
	return 0, 0, false
}

func addWidthHeight(env *Env, tag string, m *db.Media) string {
	src := imgAttr(reSrcAttr, tag)
	if src == "" {
		return tag
	}
	w, h, ok := srcDimensions(src, m)
	if !ok || w == 0 || h == 0 {
		return tag
	}
	if sw, _ := strconv.Atoi(imgAttr(reStyleWidth, tag)); sw > 0 {
		h = int(math.Round(float64(h) * float64(sw) / float64(w)))
		w = sw
	}
	return strings.Replace(tag, "<img", fmt.Sprintf(`<img width="%d" height="%d"`, w, h), 1)
}

func addSrcsetAndSizes(env *Env, tag string, m *db.Media) string {
	if len(m.Sizes) == 0 {
		return tag
	}
	src := strings.SplitN(imgAttr(reSrcAttr, tag), "?", 2)[0]
	if src == "" {
		return tag
	}
	w, _ := strconv.Atoi(imgAttr(reWidthAttr, tag))
	h, _ := strconv.Atoi(imgAttr(reHeightAttr, tag))
	if w == 0 || h == 0 {
		var ok bool
		if w, h, ok = srcDimensions(src, m); !ok {
			return tag
		}
	}
	srcset := calculateSrcset(env, w, h, src, m)
	if srcset == "" {
		return tag
	}
	attr := fmt.Sprintf(` srcset="%s"`, srcset)
	if !strings.Contains(tag, " sizes=") {
		attr += fmt.Sprintf(` sizes="(max-width: %dpx) 100vw, %dpx"`, w, w)
	}
	inner := reImgClose.FindStringSubmatch(tag)
	if inner == nil {
		return tag
	}
	return strings.Replace(tag, inner[0], "<img "+inner[1]+attr+" />", 1)
}

type srcsetSource struct {
	url   string
	width int
	isSrc bool
}

// calculateSrcset is a port of wp_calculate_image_srcset(). Sources are
// ordered with the src image first, then by width (WordPress uses the
// metadata order, which is not kept).
func calculateSrcset(env *Env, w, h int, src string, m *db.Media) string {
	if w < 1 || len(m.File) < 4 {
		return ""
	}
	dir := path.Dir(m.File)
	if dir == "." {
		dir = ""
	} else {
		dir += "/"
	}
	candidates := make([]db.MediaSize, 0, len(m.Sizes)+1)
	for _, s := range m.Sizes {
		candidates = append(candidates, s)
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Width < candidates[j].Width })
	if thumb, ok := m.Sizes["thumbnail"]; !ok || thumb.Mime != "image/gif" {
		candidates = append(candidates, db.MediaSize{File: path.Base(m.File), Width: m.Width, Height: m.Height})
	} else if strings.Contains(src, m.File) {
		return ""
	}
	byWidth := map[int]srcsetSource{}
	srcMatched := false
	var order []int
	for _, c := range candidates {
		isSrc := false
		if !srcMatched && strings.Contains(src, dir+c.File) {
			srcMatched, isSrc = true, true
		}
		if c.Width > maxSrcsetWidth && !isSrc {
			continue
		}
		if !imageMatchesRatio(w, h, c.Width, c.Height) {
			continue
		}
		if _, seen := byWidth[c.Width]; !seen {
			order = append(order, c.Width)
		}
		byWidth[c.Width] = srcsetSource{url: env.uploadURL(dir + c.File), width: c.Width, isSrc: isSrc}
	}
	if !srcMatched || len(byWidth) < 2 {
		return ""
	}
	sort.SliceStable(order, func(i, j int) bool { return byWidth[order[i]].isSrc && !byWidth[order[j]].isSrc })
	parts := make([]string, 0, len(order))
	for _, width := range order {
		s := byWidth[width]
		parts = append(parts, strings.ReplaceAll(s.url, " ", "%20")+" "+strconv.Itoa(s.width)+"w")
	}
	return strings.Join(parts, ", ")
}

func (env *Env) uploadURL(file string) string {
	if env.Links != nil {
		return env.Links.UploadURL(file)
	}
	return "/wp-content/uploads/" + file
}

// imageMatchesRatio is wp_image_matches_ratio().
func imageMatchesRatio(sw, sh, tw, th int) bool {
	var cw, ch, ew, eh int
	if sw > tw {
		cw, ch = constrainDimensions(sw, sh, tw, 0)
		ew, eh = tw, th
	} else {
		cw, ch = constrainDimensions(tw, th, sw, 0)
		ew, eh = sw, sh
	}
	return abs(cw-ew) <= 1 && abs(ch-eh) <= 1
}

// constrainDimensions is wp_constrain_dimensions().
func constrainDimensions(w, h, maxW, maxH int) (int, int) {
	if maxW == 0 && maxH == 0 {
		return w, h
	}
	wr, hr := 1.0, 1.0
	didW, didH := false, false
	if maxW > 0 && w > 0 && w > maxW {
		wr, didW = float64(maxW)/float64(w), true
	}
	if maxH > 0 && h > 0 && h > maxH {
		hr, didH = float64(maxH)/float64(h), true
	}
	smaller, larger := math.Min(wr, hr), math.Max(wr, hr)
	ratio := larger
	if int(phpRound(float64(w)*larger)) > maxW || int(phpRound(float64(h)*larger)) > maxH {
		ratio = smaller
	}
	nw := max(1, int(phpRound(float64(w)*ratio)))
	nh := max(1, int(phpRound(float64(h)*ratio)))
	if didW && nw == maxW-1 {
		nw = maxW
	}
	if didH && nh == maxH-1 {
		nh = maxH
	}
	return nw, nh
}

// phpRound rounds half away from zero like PHP's round().
func phpRound(f float64) float64 { return math.Round(f) }

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// addLoadingAttrs approximates wp_img_tag_add_loading_optimization_attrs():
// the first images on a page load eagerly (the first large one with
// fetchpriority="high"), later ones lazily; all get decoding="async".
func (env *Env) addLoadingAttrs(tag string) string {
	if strings.Contains(tag, " loading=") || strings.Contains(tag, " fetchpriority=") {
		return tag
	}
	env.contentImages++
	var add []string
	if env.contentImages > lazyThreshold && !env.IsFeed {
		add = append(add, `loading="lazy"`)
	} else if !env.fetchPriority && !env.IsFeed {
		w, _ := strconv.Atoi(imgAttr(reWidthAttr, tag))
		h, _ := strconv.Atoi(imgAttr(reHeightAttr, tag))
		if w*h >= 50000 {
			add = append(add, `fetchpriority="high"`)
			env.fetchPriority = true
		}
	}
	if !strings.Contains(tag, " decoding=") {
		add = append(add, `decoding="async"`)
	}
	if len(add) > 0 {
		tag = strings.Replace(tag, "<img", "<img "+strings.Join(add, " "), 1)
	}
	// wp_img_tag_add_auto_sizes: lazy images let the browser pick the size.
	if strings.Contains(tag, `loading="lazy"`) && strings.Contains(tag, ` sizes="`) && !strings.Contains(tag, ` sizes="auto`) {
		tag = strings.Replace(tag, ` sizes="`, ` sizes="auto, `, 1)
	}
	return tag
}
