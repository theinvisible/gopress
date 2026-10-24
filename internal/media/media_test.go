package media

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "golang.org/x/image/webp"

	"github.com/theinvisible/gopress/internal/db"
)

var testSizes = []Size{
	{Name: "thumbnail", Width: 150, Height: 150, Crop: true},
	{Name: "medium", Width: 300, Height: 300},
	{Name: "large", Width: 1024, Height: 1024},
	{Name: "post-thumbnail", Width: 624, Height: 9999},
}

func newStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	s.now = func() time.Time { return time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC) }
	return s, dir
}

func (s *Store) put(t *testing.T, name string, data []byte) (*Result, error) {
	t.Helper()
	f, tmp, err := s.CreateTemp()
	if err != nil {
		t.Fatal(err)
	}
	f.Write(data)
	f.Close()
	return s.Save(context.Background(), Upload{Temp: tmp, Name: name, Sizes: testSizes, YearMonth: true})
}

// twoColor returns a w×h image: left half red, right half blue.
func twoColor(w, h int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.NRGBA{255, 0, 0, 255}
			if x >= w/2 {
				c = color.NRGBA{0, 0, 255, 255}
			}
			img.Set(x, y, c)
		}
	}
	return img
}

func segment(marker byte, payload []byte) []byte {
	b := []byte{0xFF, marker, 0, 0}
	binary.BigEndian.PutUint16(b[2:], uint16(len(payload)+2))
	return append(b, payload...)
}

// exifOrientation builds an EXIF APP1 payload with an orientation and a
// fake GPS entry.
func exifPayload(orientation uint16) []byte {
	t := []byte("MM\x00\x2a\x00\x00\x00\x08")
	t = append(t, 0, 2) // two entries
	e := make([]byte, 12)
	binary.BigEndian.PutUint16(e, 0x0112)
	binary.BigEndian.PutUint16(e[2:], 3)
	binary.BigEndian.PutUint32(e[4:], 1)
	binary.BigEndian.PutUint16(e[8:], orientation)
	t = append(t, e...)
	g := make([]byte, 12)
	binary.BigEndian.PutUint16(g, 0x8825) // GPS IFD pointer
	binary.BigEndian.PutUint16(g[2:], 4)
	binary.BigEndian.PutUint32(g[4:], 1)
	t = append(t, g...)
	t = append(t, 0, 0, 0, 0)
	t = append(t, []byte("GPSLatitude 48.2082 N GPSLongitude 16.3738 E")...)
	return append([]byte("Exif\x00\x00"), t...)
}

func photoJPEG(t *testing.T, w, h int, orientation uint16) []byte {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, twoColor(w, h), &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	enc := buf.Bytes()
	var out []byte
	out = append(out, enc[:2]...)
	out = append(out, segment(0xE1, exifPayload(orientation))...)
	out = append(out, segment(0xE1, []byte("http://ns.adobe.com/xap/1.0/\x00<x:xmpmeta>secret location</x:xmpmeta>"))...)
	out = append(out, segment(0xE2, []byte("ICC_PROFILE\x00\x01\x01fake-profile"))...)
	out = append(out, segment(0xED, []byte("Photoshop 3.0\x008BIM IPTC city=Vienna"))...)
	out = append(out, segment(0xFE, []byte("comment: taken at home"))...)
	out = append(out, enc[2:]...)
	return append(out, []byte("trailing preview image with Exif GPS")...)
}

func TestJPEGMetadataAndOrientation(t *testing.T) {
	s, dir := newStore(t)
	for _, o := range []uint16{1, 6} {
		res, err := s.put(t, "Urlaubsfoto Wien.JPG", photoJPEG(t, 400, 200, o))
		if err != nil {
			t.Fatal(err)
		}
		data, _ := os.ReadFile(filepath.Join(dir, res.File))
		for _, leak := range []string{"Exif", "GPS", "xmpmeta", "8BIM", "taken at home", "trailing"} {
			if bytes.Contains(data, []byte(leak)) {
				t.Errorf("orientation %d: %q still in %s", o, leak, res.File)
			}
		}
		if !bytes.Contains(data, []byte("ICC_PROFILE")) {
			t.Errorf("orientation %d: color profile lost", o)
		}
		img, err := jpeg.Decode(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		b := img.Bounds()
		if o == 1 && (b.Dx() != 400 || res.Width != 400 || res.Height != 200) {
			t.Errorf("size %v, %dx%d", b, res.Width, res.Height)
		}
		if o == 6 {
			// Rotated 90° clockwise: the red left half is now on top.
			if b.Dx() != 200 || b.Dy() != 400 || res.Width != 200 || res.Height != 400 {
				t.Fatalf("rotated size %v", b)
			}
			if r, _, bl, _ := img.At(100, 20).RGBA(); r < bl {
				t.Error("orientation not applied (top is not red)")
			}
		}
		for name, sz := range res.Sizes {
			sd, err := os.ReadFile(filepath.Join(dir, path.Dir(res.File), sz.File))
			if err != nil || bytes.Contains(sd, []byte("Exif")) {
				t.Errorf("size %s: %v", name, err)
			}
		}
	}
	// Name: sanitized, year/month folder, unique.
	res, _ := s.put(t, "Urlaubsfoto Wien.JPG", photoJPEG(t, 40, 20, 1))
	if res.File != "2026/10/Urlaubsfoto-Wien-2.jpg" || res.Title != "Urlaubsfoto-Wien" {
		t.Errorf("file = %s, title %s", res.File, res.Title)
	}
}

func TestSizes(t *testing.T) {
	s, dir := newStore(t)
	var buf bytes.Buffer
	png.Encode(&buf, twoColor(800, 600))
	res, err := s.put(t, "diagram.png", addPNGChunk(buf.Bytes(), "tEXt", []byte("Comment\x00GPS secret")))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][2]int{"thumbnail": {150, 150}, "medium": {300, 225}, "post-thumbnail": {624, 468}}
	if len(res.Sizes) != len(want) {
		t.Errorf("sizes = %v", res.Sizes)
	}
	for name, wh := range want {
		sz := res.Sizes[name]
		if sz.Width != wh[0] || sz.Height != wh[1] || sz.Mime != "image/png" || sz.File != "diagram-"+itoa(wh[0])+"x"+itoa(wh[1])+".png" {
			t.Errorf("%s = %+v", name, sz)
		}
		cfg, err := png.DecodeConfig(bytes.NewReader(mustRead(t, filepath.Join(dir, "2026/10", sz.File))))
		if err != nil || cfg.Width != wh[0] || cfg.Height != wh[1] {
			t.Errorf("%s file: %+v %v", name, cfg, err)
		}
	}
	if data := mustRead(t, filepath.Join(dir, res.File)); bytes.Contains(data, []byte("GPS secret")) {
		t.Error("PNG text chunk kept")
	}
	// Big images: a -scaled copy becomes the main file.
	big, err := s.put(t, "panorama.jpeg", photoJPEG(t, 3000, 1000, 1))
	if err != nil {
		t.Fatal(err)
	}
	if big.File != "2026/10/panorama-scaled.jpeg" || big.Original != "panorama.jpeg" || big.Width != 2560 || big.Height != 853 {
		t.Errorf("big = %+v", big)
	}
	if big.Sizes["large"].File != "panorama-1024x341.jpeg" {
		t.Errorf("large = %+v", big.Sizes["large"])
	}
	// Delete removes everything.
	m := mediaOf(big)
	if err := s.Delete(m, big.Original); err != nil {
		t.Fatal(err)
	}
	left, _ := filepath.Glob(filepath.Join(dir, "2026/10/panorama*"))
	if len(left) != 0 {
		t.Errorf("left after delete: %v", left)
	}
}

func TestWebPAndGIF(t *testing.T) {
	s, dir := newStore(t)
	raw := mustRead(t, "testdata/blue-purple-pink.lossy.webp")
	// Wrap the VP8 image in an extended container with EXIF and XMP.
	vp8x := []byte("VP8X\x0a\x00\x00\x00\x0c\x00\x00\x00")
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	dims := make([]byte, 6)
	putUint24(dims, cfg.Width-1)
	putUint24(dims[3:], cfg.Height-1)
	vp8x = append(vp8x, dims...)
	body := append(append([]byte{}, vp8x...), raw[12:]...)
	body = append(body, riffChunk("EXIF", exifPayload(1)[6:])...)
	body = append(body, riffChunk("XMP ", []byte("<x:xmpmeta>secret</x:xmpmeta>"))...)
	file := append([]byte("RIFF\x00\x00\x00\x00WEBP"), body...)
	binary.LittleEndian.PutUint32(file[4:], uint32(len(file)-8))
	res, err := s.put(t, "image.webp", file)
	if err != nil {
		t.Fatal(err)
	}
	data := mustRead(t, filepath.Join(dir, res.File))
	if bytes.Contains(data, []byte("GPS")) || bytes.Contains(data, []byte("xmpmeta")) || data[20]&0x0C != 0 {
		t.Error("WebP metadata kept")
	}
	if _, _, err := image.Decode(bytes.NewReader(data)); err != nil {
		t.Errorf("stripped WebP does not decode: %v", err)
	}
	for _, sz := range res.Sizes {
		if sz.Mime != "image/jpeg" || !strings.HasSuffix(sz.File, ".jpg") {
			t.Errorf("WebP size %+v", sz)
		}
	}
	// GIF: comments go, animation stays, no sizes.
	var g bytes.Buffer
	pal := []color.Color{color.Black, color.White}
	frames := &gif.GIF{Image: []*image.Paletted{image.NewPaletted(image.Rect(0, 0, 400, 400), pal), image.NewPaletted(image.Rect(0, 0, 400, 400), pal)},
		Delay: []int{10, 10}}
	gif.EncodeAll(&g, frames)
	gd := g.Bytes()
	withComment := append(append(append([]byte{}, gd[:len(gd)-1]...), 0x21, 0xFE, 5, 'h', 'e', 'l', 'l', 'o', 0), 0x3B)
	res, err = s.put(t, "anim.gif", withComment)
	if err != nil {
		t.Fatal(err)
	}
	data = mustRead(t, filepath.Join(dir, res.File))
	if bytes.Contains(data, []byte("hello")) || len(res.Sizes) != 0 {
		t.Errorf("GIF: comment kept or sizes %v", res.Sizes)
	}
	if a, err := gif.DecodeAll(bytes.NewReader(data)); err != nil || len(a.Image) != 2 {
		t.Errorf("GIF animation broken: %v", err)
	}
}

func TestRejected(t *testing.T) {
	s, dir := newStore(t)
	var p bytes.Buffer
	png.Encode(&p, twoColor(10, 10))
	// A PNG header that claims an enormous image (decompression bomb).
	bomb := append([]byte{}, p.Bytes()...)
	binary.BigEndian.PutUint32(bomb[16:], 100000)
	binary.BigEndian.PutUint32(bomb[20:], 100000)
	binary.BigEndian.PutUint32(bomb[29:], crc32.ChecksumIEEE(bomb[12:29]))
	for _, c := range []struct {
		name string
		data []byte
		want error
	}{
		{"x.html", []byte("<html><script>alert(1)</script>"), ErrType},
		{"x.svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`), ErrType},
		{"x.php", []byte("<?php system($_GET['c']);"), ErrType},
		{"x.jpg", p.Bytes(), ErrMismatch},
		{"x.pdf", []byte("<html><body>not a pdf</body></html>"), ErrMismatch},
		{"x.tar", []byte("<!DOCTYPE html><html><script>alert(1)</script>"), ErrMismatch},
		{"x.png", bomb, ErrTooLarge},
		{"x.png", []byte("\x89PNG\r\n\x1a\nbroken"), ErrCorrupt},
	} {
		if _, err := s.put(t, c.name, c.data); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", c.name, err, c.want)
		}
	}
	// Nothing was stored, and temp files are gone.
	left, _ := filepath.Glob(filepath.Join(dir, "2026/10/*"))
	tmp, _ := filepath.Glob(filepath.Join(dir, tmpDir, "*"))
	if len(left)+len(tmp) != 0 {
		t.Errorf("left: %v %v", left, tmp)
	}
	// Path tricks in the client name stay in the upload folder.
	res, err := s.put(t, `..\..\etc/../../pass wd.PDF`, []byte("%PDF-1.4\n%fake"))
	if err != nil || res.File != "2026/10/pass-wd.pdf" || res.Mime != "application/pdf" {
		t.Errorf("pdf: %+v %v", res, err)
	}
}

func TestSanitizeName(t *testing.T) {
	for in, want := range map[string]string{
		"Grüße aus Köln.JPG": "Grusse-aus-Koln.jpg", "file.php.jpg": "file-php.jpg", "  .hidden.png": "hidden.png",
		"<script>.png": "script.png", "名前.png": "file.png", "a/b\\c.jpe": "c.jpg",
	} {
		b, e := SanitizeName(in)
		if b+e != want {
			t.Errorf("SanitizeName(%q) = %q, want %q", in, b+e, want)
		}
	}
}

func addPNGChunk(data []byte, typ string, payload []byte) []byte {
	c := make([]byte, 8+len(payload)+4)
	binary.BigEndian.PutUint32(c, uint32(len(payload)))
	copy(c[4:], typ)
	copy(c[8:], payload)
	binary.BigEndian.PutUint32(c[8+len(payload):], crc32.ChecksumIEEE(c[4:8+len(payload)]))
	at := 8 + 25 // after IHDR
	return append(append(append([]byte{}, data[:at]...), c...), data[at:]...)
}

func riffChunk(typ string, payload []byte) []byte {
	c := make([]byte, 8, 8+len(payload)+1)
	copy(c, typ)
	binary.LittleEndian.PutUint32(c[4:], uint32(len(payload)))
	c = append(c, payload...)
	if len(payload)%2 == 1 {
		c = append(c, 0)
	}
	return c
}

func putUint24(b []byte, v int) { b[0], b[1], b[2] = byte(v), byte(v>>8), byte(v>>16) }

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func itoa(n int) string { return strconv.Itoa(n) }

func mediaOf(r *Result) *db.Media {
	return &db.Media{File: r.File, Mime: r.Mime, Width: r.Width, Height: r.Height, Sizes: r.Sizes, Original: r.Original}
}

// Review fix: a small 16-bit PNG that decodes to a huge image is refused
// before decoding (the pixel count alone was below the limit).
func TestDecodedSizeLimit(t *testing.T) {
	s, _ := newStore(t)
	img := image.NewNRGBA64(image.Rect(0, 0, 7000, 7000)) // 392 MB decoded, a small file
	var buf bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	if buf.Len() > 5<<20 {
		t.Fatalf("test image too large: %d", buf.Len())
	}
	if _, err := s.put(t, "bomb.png", buf.Bytes()); !errors.Is(err, ErrTooLarge) {
		t.Errorf("16-bit image: %v", err)
	}
}

// Review fix: regenerating sizes never overwrites another upload's file.
func TestRegenerateKeepsForeignFiles(t *testing.T) {
	s, dir := newStore(t)
	var p bytes.Buffer
	png.Encode(&p, twoColor(800, 600))
	logo, err := s.put(t, "logo.png", p.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	// Another upload is called like a size the next regeneration creates.
	var q bytes.Buffer
	png.Encode(&q, twoColor(64, 64))
	other, err := s.put(t, "logo-200x150.png", q.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	before := mustRead(t, filepath.Join(dir, other.File))
	sizes, err := s.Regenerate(context.Background(), mediaOf(logo), logo.Original, []Size{{Name: "medium", Width: 200, Height: 200}})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, mustRead(t, filepath.Join(dir, other.File))) {
		t.Error("another upload was overwritten")
	}
	if sizes["medium"].File == "logo-200x150.png" {
		t.Error("size uses the other upload's name")
	}
}

// Review fix: other files are moved into place, not read into memory.
func TestNonImageMoved(t *testing.T) {
	s, dir := newStore(t)
	data := append([]byte("PK\x03\x04"), bytes.Repeat([]byte{7}, 3<<20)...)
	res, err := s.put(t, "archive.zip", data)
	if err != nil {
		t.Fatal(err)
	}
	if res.Filesize != int64(len(data)) || !bytes.Equal(mustRead(t, filepath.Join(dir, res.File)), data) {
		t.Errorf("zip stored wrongly: %d", res.Filesize)
	}
}

// Review fix: JFIF thumbnails, JFXX and foreign APP14 data are removed.
func TestJFIFThumbnailRemoved(t *testing.T) {
	var buf bytes.Buffer
	jpeg.Encode(&buf, twoColor(40, 20), nil)
	enc := buf.Bytes()
	jfif := append([]byte("JFIF\x00\x01\x01\x00\x00\x01\x00\x01\x02\x01"), []byte("THUMBDATA-THUMB")...) // 2×1 thumbnail
	data := append(append(append([]byte{}, enc[:2]...), segment(0xE0, jfif)...), segment(0xE0, []byte("JFXX\x00\x10secret-thumbnail"))...)
	data = append(data, segment(0xEE, []byte("NotAdobe hidden"))...)
	data = append(data, enc[2:]...)
	out, _, err := stripJPEG(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"THUMBDATA", "secret-thumbnail", "hidden"} {
		if bytes.Contains(out, []byte(leak)) {
			t.Errorf("%q kept", leak)
		}
	}
	if _, err := jpeg.Decode(bytes.NewReader(out)); err != nil {
		t.Errorf("does not decode: %v", err)
	}
}
