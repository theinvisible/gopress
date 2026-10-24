package site

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"

	"github.com/andybalholm/brotli"
)

// response is a fully rendered response, so that it can be cached and
// served compressed.
type response struct {
	status int
	header http.Header
	body   []byte
	err    error
}

func htmlResponse(status int, body []byte) *response {
	h := http.Header{}
	h.Set("Content-Type", "text/html; charset=UTF-8")
	return &response{status: status, header: h, body: body}
}

func redirect(url string) *response {
	h := http.Header{}
	h.Set("Location", url)
	h.Set("Content-Type", "text/html; charset=UTF-8")
	return &response{status: http.StatusMovedPermanently, header: h}
}

func errorResponse(err error) *response {
	h := http.Header{}
	h.Set("Content-Type", "text/plain; charset=utf-8")
	return &response{status: http.StatusInternalServerError, header: h, body: []byte("Internal Server Error\n"), err: err}
}

func (r *response) cacheable() bool {
	switch r.status {
	case http.StatusOK, http.StatusMovedPermanently, http.StatusNotFound, http.StatusGone:
		return r.header.Get("Cache-Control") != "no-store"
	}
	return false
}

// encoded is a response body in its plain, gzip and brotli forms.
type encoded struct {
	resp      *response
	etag      string
	gz, br    []byte
	compress  bool
	totalSize int64
}

func encode(resp *response) *encoded {
	e := &encoded{resp: resp}
	sum := sha256.Sum256(resp.body)
	e.etag = `"` + hex.EncodeToString(sum[:12]) + `"`
	ct := resp.header.Get("Content-Type")
	e.compress = len(resp.body) > 512 && (strings.HasPrefix(ct, "text/") || strings.Contains(ct, "xml") || strings.Contains(ct, "json"))
	if e.compress {
		var gz bytes.Buffer
		zw, _ := gzip.NewWriterLevel(&gz, gzip.BestCompression)
		zw.Write(resp.body)
		zw.Close()
		e.gz = gz.Bytes()
		var br bytes.Buffer
		bw := brotli.NewWriterLevel(&br, 9)
		bw.Write(resp.body)
		bw.Close()
		e.br = br.Bytes()
	}
	e.totalSize = int64(len(resp.body) + len(e.gz) + len(e.br) + 512)
	return e
}

// serve writes the response honoring If-None-Match and Accept-Encoding.
func (e *encoded) serve(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	for k, v := range e.resp.header {
		h[k] = v
	}
	if e.resp.status == http.StatusOK {
		h.Set("ETag", e.etag)
		if match := r.Header.Get("If-None-Match"); match != "" && strings.Contains(match, e.etag) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	body := e.resp.body
	if e.compress {
		h.Add("Vary", "Accept-Encoding")
		switch ae := r.Header.Get("Accept-Encoding"); {
		case acceptsEncoding(ae, "br"):
			h.Set("Content-Encoding", "br")
			body = e.br
		case acceptsEncoding(ae, "gzip"):
			h.Set("Content-Encoding", "gzip")
			body = e.gz
		}
	}
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(e.resp.status)
	if r.Method != http.MethodHead {
		w.Write(body)
	}
}

func acceptsEncoding(header, enc string) bool {
	for _, part := range strings.Split(header, ",") {
		name, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if !strings.EqualFold(strings.TrimSpace(name), enc) {
			continue
		}
		for _, p := range strings.Split(params, ";") {
			k, v, ok := strings.Cut(strings.TrimSpace(p), "=")
			if ok && strings.EqualFold(k, "q") {
				if q, err := strconv.ParseFloat(v, 64); err == nil && q == 0 {
					return false
				}
			}
		}
		return true
	}
	return false
}

// write serves an uncached response (gzip only, no brotli, to keep the
// uncached path cheap).
func (r *response) write(w http.ResponseWriter, req *http.Request) {
	h := w.Header()
	for k, v := range r.header {
		h[k] = v
	}
	body := r.body
	ct := r.header.Get("Content-Type")
	if len(body) > 512 && acceptsEncoding(req.Header.Get("Accept-Encoding"), "gzip") &&
		(strings.HasPrefix(ct, "text/") || strings.Contains(ct, "xml")) {
		var gz bytes.Buffer
		zw, _ := gzip.NewWriterLevel(&gz, gzip.DefaultCompression)
		zw.Write(body)
		zw.Close()
		body = gz.Bytes()
		h.Set("Content-Encoding", "gzip")
		h.Add("Vary", "Accept-Encoding")
	}
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(r.status)
	if req.Method != http.MethodHead {
		w.Write(body)
	}
}
