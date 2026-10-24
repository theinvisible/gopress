// Package media stores uploaded files: it checks the type against the
// content, removes photo metadata (EXIF/GPS, XMP, IPTC), applies the EXIF
// orientation, scales very large images down and creates the image sizes
// (thumbnail, medium, large …) like WordPress. Everything is pure Go.
package media

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	"image/png"
	"io/fs"
	"net/http"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
	"golang.org/x/text/unicode/norm"

	"github.com/theinvisible/gopress/internal/db"
)

// Size is an image size (add_image_size).
type Size struct {
	Name          string
	Width, Height int // 0 = unlimited
	Crop          bool
}

// Limits.
const (
	MaxPixels       = 50_000_000 // decoded images larger than this are refused
	BigImageMax     = 2560       // big_image_size_threshold: larger images get a -scaled copy
	jpegQuality     = 82         // WordPress' default
	jpegQualityFull = 90         // re-encoded originals (orientation applied)
	maxNameLen      = 120
	tmpDir          = ".tmp" // never served (dot-prefixed)
)

// fileType describes an accepted upload type.
type fileType struct {
	mime  string
	image bool
	// sniff lists acceptable results of http.DetectContentType; nil
	// accepts any content that is not text, HTML, XML or script.
	sniff []string
}

var types = map[string]fileType{
	".jpg": {"image/jpeg", true, []string{"image/jpeg"}}, ".jpeg": {"image/jpeg", true, []string{"image/jpeg"}},
	".png": {"image/png", true, []string{"image/png"}}, ".gif": {"image/gif", true, []string{"image/gif"}},
	".webp": {"image/webp", true, []string{"image/webp"}},
	".pdf":  {"application/pdf", false, []string{"application/pdf"}},
	".zip":  {"application/zip", false, []string{"application/zip"}},
	".gz":   {"application/gzip", false, []string{"application/x-gzip"}}, ".tgz": {"application/gzip", false, []string{"application/x-gzip"}},
	".bz2": {"application/x-bzip2", false, nil}, ".xz": {"application/x-xz", false, nil}, ".7z": {"application/x-7z-compressed", false, nil},
	".tar": {"application/x-tar", false, nil}, ".deb": {"application/vnd.debian.binary-package", false, nil},
	".rpm": {"application/x-rpm", false, nil},
	".mp4": {"video/mp4", false, []string{"video/mp4"}}, ".webm": {"video/webm", false, []string{"video/webm"}},
	".mp3": {"audio/mpeg", false, []string{"audio/mpeg"}}, ".ogg": {"audio/ogg", false, []string{"application/ogg", "audio/ogg"}},
	".m4a": {"audio/mp4", false, []string{"video/mp4", "audio/mp4"}}, ".wav": {"audio/wav", false, []string{"audio/wave"}},
}

// Accept is the accept attribute for file inputs.
func Accept() string {
	exts := make([]string, 0, len(types))
	for e := range types {
		exts = append(exts, e)
	}
	sort.Strings(exts)
	return strings.Join(exts, ",")
}

// Errors shown to the user.
var (
	ErrType     = errors.New("This file type is not allowed.")
	ErrMismatch = errors.New("The file content does not match its extension.")
	ErrTooLarge = errors.New("The image is too large (more than 50 megapixels).")
	ErrCorrupt  = errors.New("The image could not be read.")
)

// Store saves uploads below the uploads directory. All file access goes
// through an os.Root, so no path can leave that directory.
type Store struct {
	root *os.Root
	sem  chan struct{}
	now  func() time.Time
}

// Open opens (creating) the uploads directory.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	return &Store{root: root, sem: make(chan struct{}, 2), now: time.Now}, nil
}

// Close releases the directory.
func (s *Store) Close() error { return s.root.Close() }

// CreateTemp creates a temporary file for an upload in the (never
// served) temp directory. The caller writes the upload into it and
// passes its name to Save, or calls RemoveTemp.
func (s *Store) CreateTemp() (*os.File, string, error) {
	if err := s.root.MkdirAll(tmpDir, 0o700); err != nil {
		return nil, "", err
	}
	b := make([]byte, 16)
	rand.Read(b)
	name := path.Join(tmpDir, hex.EncodeToString(b))
	f, err := s.root.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	return f, name, err
}

// RemoveTemp deletes a temporary file.
func (s *Store) RemoveTemp(name string) { s.root.Remove(name) }

// Result is a stored upload.
type Result struct {
	File     string // path below uploads, e.g. 2026/10/photo.jpg
	Original string // unscaled original of a -scaled image ("" if none)
	Mime     string
	Width    int
	Height   int
	Filesize int64
	Sizes    map[string]db.MediaSize
	Title    string // file name without extension, for the attachment title
}

// Upload describes a file to store.
type Upload struct {
	Temp      string // name returned by CreateTemp
	Name      string // file name from the client
	Sizes     []Size
	YearMonth bool // store in YYYY/MM/
}

// Save checks and stores an upload; the temp file is removed.
func (s *Store) Save(ctx context.Context, up Upload) (*Result, error) {
	defer s.root.Remove(up.Temp)
	base, ext := SanitizeName(up.Name)
	ft, ok := types[ext]
	if !ok {
		return nil, ErrType
	}
	data, err := s.root.ReadFile(up.Temp)
	if err != nil {
		return nil, err
	}
	if !sniffOK(data, ft) {
		return nil, ErrMismatch
	}
	dir := ""
	if up.YearMonth {
		dir = s.now().Format("2006/01")
	}
	res := &Result{Mime: ft.mime, Title: base}
	var out *processed
	if ft.image {
		select {
		case s.sem <- struct{}{}:
			defer func() { <-s.sem }()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if out, err = processImage(data, ext, up.Sizes); err != nil {
			return nil, err
		}
		res.Width, res.Height = out.width, out.height
	} else {
		out = &processed{main: data}
	}
	// Pick a name for which none of the files exists yet.
	var names map[string]string
	for n := 0; ; n++ {
		b := base
		if n > 0 {
			b = base + "-" + strconv.Itoa(n)
		}
		names = out.names(b, ext)
		free := true
		for _, f := range names {
			if _, err := s.root.Stat(path.Join(dir, f)); !errors.Is(err, fs.ErrNotExist) {
				free = false
				break
			}
		}
		if free {
			break
		}
		if n > 1000 {
			return nil, errors.New("media: no free file name")
		}
	}
	if dir != "" {
		if err := s.root.MkdirAll(dir, 0o750); err != nil {
			return nil, err
		}
	}
	var written []string
	write := func(name string, b []byte) error {
		p := path.Join(dir, name)
		f, err := s.root.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
		if err != nil {
			return err
		}
		written = append(written, p)
		if _, err := f.Write(b); err != nil {
			f.Close()
			return err
		}
		return f.Close()
	}
	fail := func(err error) (*Result, error) {
		for _, p := range written {
			s.root.Remove(p)
		}
		return nil, err
	}
	if err := write(names["main"], out.main); err != nil {
		return fail(err)
	}
	res.File, res.Filesize = path.Join(dir, names["main"]), int64(len(out.main))
	if out.original != nil {
		if err := write(names["original"], out.original); err != nil {
			return fail(err)
		}
		res.Original = names["original"]
	}
	res.Sizes = map[string]db.MediaSize{}
	for _, sz := range out.sizes {
		if err := write(names[sz.name], sz.data); err != nil {
			return fail(err)
		}
		res.Sizes[sz.name] = db.MediaSize{File: names[sz.name], Width: sz.w, Height: sz.h, Mime: sz.mime}
	}
	if out.scaled {
		res.Mime = out.mainMime
	}
	return res, nil
}

// sniffOK compares the content with the expected type.
func sniffOK(data []byte, ft fileType) bool {
	got := http.DetectContentType(data)
	if i := strings.IndexByte(got, ';'); i >= 0 {
		got = got[:i]
	}
	if ft.sniff == nil {
		return !strings.HasPrefix(got, "text/") && !strings.Contains(got, "html") && !strings.Contains(got, "xml") &&
			!strings.Contains(got, "javascript") && !strings.Contains(got, "json")
	}
	for _, s := range ft.sniff {
		if got == s {
			return true
		}
	}
	return false
}

// SanitizeName turns a client file name into a safe base name and a
// lower-case extension (sanitize_file_name, restricted to ASCII).
func SanitizeName(name string) (base, ext string) {
	name = path.Base(strings.ReplaceAll(name, "\\", "/"))
	ext = strings.ToLower(path.Ext(name))
	if ext == ".jpe" {
		ext = ".jpg"
	}
	base = strings.TrimSuffix(name, path.Ext(name))
	var b strings.Builder
	dash := false
	for _, r := range norm.NFD.String(base) {
		switch {
		case unicode.Is(unicode.Mn, r):
			continue // accents
		case r < 0x80 && (unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'):
			b.WriteRune(r)
			dash = false
		case r == 'ß':
			b.WriteString("ss")
			dash = false
		default:
			if !dash {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	base = strings.Trim(b.String(), "-_")
	if len(base) > maxNameLen {
		base = strings.Trim(base[:maxNameLen], "-_")
	}
	if base == "" {
		base = "file"
	}
	return base, ext
}

// processed is the outcome of processing an image.
type processed struct {
	main     []byte
	mainMime string
	original []byte // when main is a -scaled copy
	scaled   bool
	width    int
	height   int
	ext      string // extension of main ("" = the upload's)
	sizes    []sizeOut
}

type sizeOut struct {
	name, mime, ext string
	w, h            int
	data            []byte
}

// names maps "main", "original" and the size names to file names.
func (p *processed) names(base, ext string) map[string]string {
	m := map[string]string{"main": base + ext}
	if p.scaled {
		m["original"] = base + ext
		m["main"] = base + "-scaled" + p.mainExt(ext)
	}
	for _, s := range p.sizes {
		m[s.name] = base + "-" + strconv.Itoa(s.w) + "x" + strconv.Itoa(s.h) + s.ext
	}
	return m
}

func (p *processed) mainExt(ext string) string {
	if p.ext != "" {
		return p.ext
	}
	return ext
}

func processImage(data []byte, ext string, sizes []Size) (*processed, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, ErrCorrupt
	}
	if want := map[string]string{".jpg": "jpeg", ".jpeg": "jpeg", ".png": "png", ".gif": "gif", ".webp": "webp"}[ext]; want != format {
		return nil, ErrMismatch
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > MaxPixels || cfg.Width > 30000 || cfg.Height > 30000 {
		return nil, ErrTooLarge
	}
	p := &processed{width: cfg.Width, height: cfg.Height}
	var icc [][]byte
	orientation := 1
	switch format {
	case "jpeg":
		orientation = jpegOrientation(data)
		p.main, icc, err = stripJPEG(data)
	case "png":
		p.main, err = stripPNG(data)
	case "webp":
		p.main, err = stripWebP(data)
	case "gif":
		// Animated GIFs would lose their animation when resized: they are
		// stored as they are (without metadata) and get no sizes.
		p.main, err = stripGIF(data)
		if err != nil {
			return nil, ErrCorrupt
		}
		return p, nil
	}
	if err != nil {
		return nil, ErrCorrupt
	}
	img, _, err := image.Decode(bytes.NewReader(p.main))
	if err != nil {
		return nil, ErrCorrupt
	}
	if orientation != 1 {
		img = orient(img, orientation)
		b := img.Bounds()
		p.width, p.height = b.Dx(), b.Dy()
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: jpegQualityFull}); err != nil {
			return nil, err
		}
		p.main = insertICC(buf.Bytes(), icc)
	}
	alpha := hasAlpha(img)
	encode := func(m image.Image) ([]byte, string, string, error) {
		var buf bytes.Buffer
		if format == "png" || (format == "webp" && alpha) {
			enc := png.Encoder{CompressionLevel: png.DefaultCompression}
			err := enc.Encode(&buf, m)
			return buf.Bytes(), "image/png", ".png", err
		}
		err := jpeg.Encode(&buf, m, &jpeg.Options{Quality: jpegQuality})
		b := buf.Bytes()
		if format == "jpeg" {
			b = insertICC(b, icc)
		}
		return b, "image/jpeg", ".jpg", err
	}
	src := newSources(img)
	// Very large images: the -scaled copy becomes the main file (not for
	// WebP, which cannot be encoded in pure Go).
	if format != "webp" && (p.width > BigImageMax || p.height > BigImageMax) {
		w, h := constrain(p.width, p.height, BigImageMax, BigImageMax)
		scaled := src.resize(image.Rect(0, 0, p.width, p.height), w, h)
		data, mime, sext, err := encode(scaled)
		if err != nil {
			return nil, err
		}
		p.original, p.main, p.mainMime, p.scaled = p.main, data, mime, true
		if format != "jpeg" && sext != ext { // .jpeg stays .jpeg
			p.ext = sext
		}
		p.width, p.height = w, h
		src = newSources(scaled)
	}
	for _, sz := range sizes {
		rect, w, h, ok := resizeDims(p.width, p.height, sz)
		if !ok {
			continue
		}
		m := src.resize(rect, w, h)
		data, mime, sext, err := encode(m)
		if err != nil {
			return nil, err
		}
		if format == "jpeg" {
			sext = ext
		}
		p.sizes = append(p.sizes, sizeOut{name: sz.Name, mime: mime, ext: sext, w: w, h: h, data: data})
	}
	return p, nil
}

func hasAlpha(img image.Image) bool {
	if o, ok := img.(interface{ Opaque() bool }); ok {
		return !o.Opaque()
	}
	return true
}

// constrain fits w×h into maxW×maxH (0 = unlimited) keeping the ratio
// (wp_constrain_dimensions).
func constrain(w, h, maxW, maxH int) (int, int) {
	if maxW == 0 && maxH == 0 {
		return w, h
	}
	wr, hr := 1.0, 1.0
	if maxW > 0 && w > maxW {
		wr = float64(maxW) / float64(w)
	}
	if maxH > 0 && h > maxH {
		hr = float64(maxH) / float64(h)
	}
	r := min(wr, hr)
	nw, nh := int(float64(w)*r+0.5), int(float64(h)*r+0.5)
	// WordPress rounds and corrects off-by-one results to the limit.
	if maxW > 0 && nw > maxW {
		nw = maxW
	}
	if maxH > 0 && nh > maxH {
		nh = maxH
	}
	return max(nw, 1), max(nh, 1)
}

// resizeDims returns the source rectangle and the target size for a size
// (image_resize_dimensions), or ok=false when no resize is needed.
func resizeDims(w, h int, sz Size) (src image.Rectangle, nw, nh int, ok bool) {
	if sz.Width <= 0 && sz.Height <= 0 {
		return image.Rectangle{}, 0, 0, false
	}
	if sz.Crop && sz.Width > 0 && sz.Height > 0 {
		nw, nh = min(sz.Width, w), min(sz.Height, h)
		ratio := max(float64(nw)/float64(w), float64(nh)/float64(h))
		cw, ch := int(float64(nw)/ratio+0.5), int(float64(nh)/ratio+0.5)
		x, y := (w-cw)/2, (h-ch)/2
		src = image.Rect(x, y, x+cw, y+ch)
	} else {
		nw, nh = constrain(w, h, sz.Width, sz.Height)
		src = image.Rect(0, 0, w, h)
	}
	if nw >= w && nh >= h {
		return image.Rectangle{}, 0, 0, false
	}
	return src, nw, nh, true
}

// sources keeps downscaled copies of an image: each size is resized from
// the smallest copy that is still at least twice as large, which is much
// faster than always starting from a large photo and looks the same.
type sources struct {
	full  image.Image
	cache []image.Image // non-cropped downscales, largest first
}

func newSources(img image.Image) *sources { return &sources{full: img} }

func (s *sources) resize(rect image.Rectangle, w, h int) image.Image {
	fb := s.full.Bounds()
	from, scale := s.full, 1.0
	for _, c := range s.cache {
		cs := float64(c.Bounds().Dx()) / float64(fb.Dx())
		if float64(rect.Dx())*cs >= float64(2*w) && float64(rect.Dy())*cs >= float64(2*h) {
			from, scale = c, cs
		}
	}
	sr := image.Rect(int(float64(rect.Min.X)*scale), int(float64(rect.Min.Y)*scale),
		int(float64(rect.Max.X)*scale+0.5), int(float64(rect.Max.Y)*scale+0.5)).Add(from.Bounds().Min)
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	draw.CatmullRom.Scale(dst, dst.Bounds(), from, sr, draw.Src, nil)
	if rect == image.Rect(0, 0, fb.Dx(), fb.Dy()) {
		s.cache = append(s.cache, dst)
		sort.Slice(s.cache, func(i, j int) bool { return s.cache[i].Bounds().Dx() > s.cache[j].Bounds().Dx() })
	}
	return dst
}

// Delete removes the files of a media item (main file, unscaled
// original and sizes).
func (s *Store) Delete(m *db.Media, original string) error {
	dir := path.Dir(m.File)
	files := []string{m.File}
	if original != "" {
		files = append(files, path.Join(dir, original))
	}
	for _, sz := range m.Sizes {
		files = append(files, path.Join(dir, sz.File))
	}
	var first error
	for _, f := range files {
		if err := s.root.Remove(f); err != nil && !errors.Is(err, fs.ErrNotExist) && first == nil {
			first = err
		}
	}
	return first
}

// Regenerate creates the sizes of an image again (after the size
// settings changed) from its original and removes sizes that are no
// longer generated. It returns the new sizes.
func (s *Store) Regenerate(ctx context.Context, m *db.Media, original string, sizes []Size) (map[string]db.MediaSize, error) {
	if !strings.HasPrefix(m.Mime, "image/") || m.Mime == "image/gif" {
		return m.Sizes, nil
	}
	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	dir := path.Dir(m.File)
	data, err := s.root.ReadFile(m.File)
	if err != nil {
		return nil, err
	}
	img, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, ErrCorrupt
	}
	var icc [][]byte
	if format == "jpeg" {
		_, icc, _ = stripJPEG(data)
	}
	ext := path.Ext(m.File)
	base := strings.TrimSuffix(path.Base(m.File), ext)
	base = strings.TrimSuffix(base, "-scaled")
	b := img.Bounds()
	src := newSources(img)
	out := map[string]db.MediaSize{}
	keep := map[string]bool{}
	for _, sz := range sizes {
		rect, w, h, ok := resizeDims(b.Dx(), b.Dy(), sz)
		if !ok {
			continue
		}
		var buf bytes.Buffer
		mime, sext := "image/jpeg", ext
		resized := src.resize(rect, w, h)
		if format == "png" || (format == "webp" && hasAlpha(img)) {
			mime, sext = "image/png", ".png"
			err = png.Encode(&buf, resized)
		} else {
			if format != "jpeg" {
				sext = ".jpg"
			}
			err = jpeg.Encode(&buf, resized, &jpeg.Options{Quality: jpegQuality})
		}
		if err != nil {
			return nil, err
		}
		data := buf.Bytes()
		if format == "jpeg" {
			data = insertICC(data, icc)
		}
		name := fmt.Sprintf("%s-%dx%d%s", base, w, h, sext)
		if err := s.root.WriteFile(path.Join(dir, name), data, 0o640); err != nil {
			return nil, err
		}
		out[sz.Name] = db.MediaSize{File: name, Width: w, Height: h, Mime: mime}
		keep[name] = true
	}
	for _, old := range m.Sizes {
		if !keep[old.File] && old.File != path.Base(m.File) && old.File != original {
			s.root.Remove(path.Join(dir, old.File))
		}
	}
	return out, nil
}

// Strip removes metadata from an existing file in place (for uploads from
// before GoPress). It reports whether the file changed.
func (s *Store) Strip(file string, apply bool) (bool, error) {
	data, err := s.root.ReadFile(file)
	if err != nil {
		return false, err
	}
	var out []byte
	switch strings.ToLower(path.Ext(file)) {
	case ".jpg", ".jpeg":
		out, _, err = stripJPEG(data)
	case ".png":
		out, err = stripPNG(data)
	case ".webp":
		out, err = stripWebP(data)
	case ".gif":
		out, err = stripGIF(data)
	default:
		return false, nil
	}
	if err != nil || bytes.Equal(out, data) {
		return false, err
	}
	if apply {
		if err := s.root.WriteFile(file, out, 0o640); err != nil {
			return false, err
		}
	}
	return true, nil
}

// ReadFile reads a file below the uploads directory (for tests and
// tools).
func (s *Store) ReadFile(name string) ([]byte, error) { return s.root.ReadFile(name) }
