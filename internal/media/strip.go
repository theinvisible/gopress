package media

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
)

// Metadata removal. Photos carry EXIF (camera, serial numbers, GPS
// position), XMP and IPTC blocks; uploads publish them to everyone. The
// functions below rewrite the container without these blocks and without
// re-encoding the image data, so quality is untouched. Color profiles
// (ICC) and everything needed to decode the image are kept.

var errFormat = errors.New("media: malformed image file")

// stripJPEG removes APP1 (EXIF, XMP), APP3–APP13 (IPTC/Photoshop …),
// APP15 and comments, and drops data after the end of the image (some
// cameras append preview images there). APP0 (JFIF), APP2 ICC profiles
// and APP14 (Adobe color transform) stay. It also returns the ICC profile
// segments, to re-insert them when the image has to be re-encoded.
func stripJPEG(data []byte) (out []byte, icc [][]byte, err error) {
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return nil, nil, errFormat
	}
	var b bytes.Buffer
	b.Grow(len(data))
	b.Write(data[:2])
	pos := 2
	for pos < len(data) {
		if data[pos] != 0xFF {
			return nil, nil, errFormat
		}
		for pos < len(data) && data[pos] == 0xFF { // fill bytes
			pos++
		}
		if pos >= len(data) {
			return nil, nil, errFormat
		}
		marker := data[pos]
		pos++
		switch {
		case marker == 0xD9: // EOI: anything after it is dropped
			b.Write([]byte{0xFF, 0xD9})
			return b.Bytes(), icc, nil
		case marker >= 0xD0 && marker <= 0xD7, marker == 0x01: // standalone
			b.Write([]byte{0xFF, marker})
			continue
		}
		if pos+2 > len(data) {
			return nil, nil, errFormat
		}
		n := int(binary.BigEndian.Uint16(data[pos:]))
		if n < 2 || pos+n > len(data) {
			return nil, nil, errFormat
		}
		seg := data[pos-2 : pos+n]
		payload := data[pos+2 : pos+n]
		pos += n
		keep := true
		switch {
		case marker == 0xE2:
			keep = bytes.HasPrefix(payload, []byte("ICC_PROFILE\x00"))
			if keep {
				icc = append(icc, seg)
			}
		case marker == 0xE0:
			// JFIF header without its embedded thumbnail (which may show
			// the uncropped original); JFXX extensions are dropped.
			keep = false
			if bytes.HasPrefix(payload, []byte("JFIF\x00")) && len(payload) >= 14 {
				jfif := append([]byte{0xFF, 0xE0, 0, 16}, payload[:14]...)
				jfif[4+12], jfif[4+13] = 0, 0
				b.Write(jfif)
			}
		case marker == 0xEE: // Adobe color transform
			keep = bytes.HasPrefix(payload, []byte("Adobe"))
		case marker >= 0xE1 && marker <= 0xEF, marker == 0xFE: // other APPn, COM
			keep = false
		}
		if keep {
			b.Write(seg)
		}
		if marker == 0xDA { // start of scan: copy the entropy-coded data
			start := pos
			for pos < len(data)-1 {
				if data[pos] == 0xFF {
					next := data[pos+1]
					if next != 0x00 && !(next >= 0xD0 && next <= 0xD7) && next != 0xFF {
						break
					}
				}
				pos++
			}
			if pos >= len(data)-1 {
				return nil, nil, errFormat
			}
			b.Write(data[start:pos])
		}
	}
	return nil, nil, errFormat
}

// insertICC puts ICC profile segments right after the SOI (and a JFIF
// APP0, if present) of a freshly encoded JPEG.
func insertICC(jpg []byte, icc [][]byte) []byte {
	if len(icc) == 0 || len(jpg) < 2 {
		return jpg
	}
	at := 2
	if len(jpg) > 6 && jpg[2] == 0xFF && jpg[3] == 0xE0 {
		at = 4 + int(binary.BigEndian.Uint16(jpg[4:]))
	}
	var b bytes.Buffer
	b.Write(jpg[:at])
	for _, s := range icc {
		b.Write(s)
	}
	b.Write(jpg[at:])
	return b.Bytes()
}

var pngSignature = []byte("\x89PNG\r\n\x1a\n")

// pngKeep lists the PNG chunks that are kept: everything needed to show
// the image (including APNG animation and color information).
var pngKeep = map[string]bool{
	"IHDR": true, "PLTE": true, "IDAT": true, "IEND": true, "tRNS": true, "gAMA": true, "cHRM": true,
	"sRGB": true, "iCCP": true, "sBIT": true, "bKGD": true, "pHYs": true, "cICP": true,
	"acTL": true, "fcTL": true, "fdAT": true,
}

// stripPNG removes text chunks (tEXt, zTXt, iTXt: often XMP), eXIf, tIME
// and unknown ancillary chunks.
func stripPNG(data []byte) ([]byte, error) {
	if !bytes.HasPrefix(data, pngSignature) {
		return nil, errFormat
	}
	var b bytes.Buffer
	b.Write(pngSignature)
	pos := len(pngSignature)
	for pos+12 <= len(data) {
		n := int(binary.BigEndian.Uint32(data[pos:]))
		if n < 0 || pos+12+n > len(data) {
			return nil, errFormat
		}
		typ := string(data[pos+4 : pos+8])
		chunk := data[pos : pos+12+n]
		if crc32.ChecksumIEEE(chunk[4:8+n]) != binary.BigEndian.Uint32(chunk[8+n:]) {
			return nil, errFormat
		}
		pos += 12 + n
		if pngKeep[typ] {
			b.Write(chunk)
		}
		if typ == "IEND" {
			return b.Bytes(), nil
		}
	}
	return nil, errFormat
}

// stripWebP removes the EXIF and XMP chunks of a WebP file and clears
// their flags in the VP8X header.
func stripWebP(data []byte) ([]byte, error) {
	if len(data) < 12 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WEBP" {
		return nil, errFormat
	}
	var body bytes.Buffer
	pos := 12
	for pos+8 <= len(data) {
		typ := string(data[pos : pos+4])
		n := int(binary.LittleEndian.Uint32(data[pos+4:]))
		end := pos + 8 + n + n%2 // chunks are padded to even sizes
		if n < 0 || pos+8+n > len(data) {
			return nil, errFormat
		}
		if end > len(data) {
			end = len(data)
		}
		chunk := append([]byte(nil), data[pos:end]...)
		pos = end
		switch typ {
		case "VP8 ", "VP8L", "ALPH", "ANIM", "ANMF", "ICCP":
		case "VP8X":
			if len(chunk) > 8 {
				chunk[8] &^= 0x08 | 0x04 // EXIF and XMP flags
			}
		default: // EXIF, XMP and unknown chunks
			continue
		}
		body.Write(chunk)
	}
	out := make([]byte, 12, 12+body.Len())
	copy(out, data[:12])
	binary.LittleEndian.PutUint32(out[4:], uint32(4+body.Len()))
	return append(out, body.Bytes()...), nil
}

// stripGIF removes comment blocks and application extensions other than
// the animation loop extension (XMP lives in an application extension).
func stripGIF(data []byte) ([]byte, error) {
	if len(data) < 13 || (string(data[:6]) != "GIF89a" && string(data[:6]) != "GIF87a") {
		return nil, errFormat
	}
	var b bytes.Buffer
	pos := 13
	if flags := data[10]; flags&0x80 != 0 { // global color table
		pos += 3 << (flags&0x07 + 1)
	}
	if pos > len(data) {
		return nil, errFormat
	}
	b.Write(data[:pos])
	// subBlocks returns the end of a sequence of data sub-blocks.
	subBlocks := func(p int) (int, error) {
		for p < len(data) {
			n := int(data[p])
			p++
			if n == 0 {
				return p, nil
			}
			p += n
		}
		return 0, errFormat
	}
	for pos < len(data) {
		switch data[pos] {
		case 0x3B: // trailer
			b.WriteByte(0x3B)
			return b.Bytes(), nil
		case 0x21: // extension
			if pos+2 > len(data) {
				return nil, errFormat
			}
			label := data[pos+1]
			end, err := subBlocks(pos + 2)
			if err != nil {
				return nil, err
			}
			keep := label != 0xFE // comment
			if label == 0xFF {
				app := ""
				if pos+3+11 <= len(data) && data[pos+2] == 11 {
					app = string(data[pos+3 : pos+3+11])
				}
				keep = app == "NETSCAPE2.0" || app == "ANIMEXTS1.0"
			}
			if keep {
				b.Write(data[pos:end])
			}
			pos = end
		case 0x2C: // image descriptor
			if pos+10 > len(data) {
				return nil, errFormat
			}
			start := pos
			flags := data[pos+9]
			pos += 10
			if flags&0x80 != 0 { // local color table
				pos += 3 << (flags&0x07 + 1)
			}
			pos++ // LZW minimum code size
			end, err := subBlocks(pos)
			if err != nil {
				return nil, err
			}
			b.Write(data[start:end])
			pos = end
		default:
			return nil, errFormat
		}
	}
	return nil, errFormat
}
