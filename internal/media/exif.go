package media

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/draw"
)

// jpegOrientation returns the EXIF orientation (1–8) of a JPEG, 1 if it
// has none. Only IFD0 tag 0x0112 is read.
func jpegOrientation(data []byte) int {
	pos := 2
	for pos+4 <= len(data) && data[pos] == 0xFF {
		marker := data[pos+1]
		if marker == 0xDA || marker == 0xD9 {
			break
		}
		n := int(binary.BigEndian.Uint16(data[pos+2:]))
		if n < 2 || pos+2+n > len(data) {
			break
		}
		payload := data[pos+4 : pos+2+n]
		pos += 2 + n
		if marker != 0xE1 || !bytes.HasPrefix(payload, []byte("Exif\x00\x00")) {
			continue
		}
		return tiffOrientation(payload[6:])
	}
	return 1
}

func tiffOrientation(t []byte) int {
	if len(t) < 8 {
		return 1
	}
	var bo binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return 1
	}
	ifd := int(bo.Uint32(t[4:]))
	if ifd < 8 || ifd+2 > len(t) {
		return 1
	}
	count := int(bo.Uint16(t[ifd:]))
	for i := 0; i < count; i++ {
		e := ifd + 2 + 12*i
		if e+12 > len(t) {
			return 1
		}
		if bo.Uint16(t[e:]) == 0x0112 && bo.Uint16(t[e+2:]) == 3 { // SHORT
			if o := int(bo.Uint16(t[e+8:])); o >= 1 && o <= 8 {
				return o
			}
		}
	}
	return 1
}

// orient applies an EXIF orientation to img (so the stored pixels show
// the photo upright once the EXIF block is gone).
func orient(img image.Image, o int) image.Image {
	if o <= 1 || o > 8 {
		return img
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	src := image.NewNRGBA(image.Rect(0, 0, w, h))
	draw.Draw(src, src.Bounds(), img, b.Min, draw.Src)
	dw, dh := w, h
	if o >= 5 {
		dw, dh = h, w
	}
	dst := image.NewNRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var dx, dy int
			switch o {
			case 2: // mirrored
				dx, dy = w-1-x, y
			case 3: // rotated 180°
				dx, dy = w-1-x, h-1-y
			case 4: // mirrored vertically
				dx, dy = x, h-1-y
			case 5: // transposed
				dx, dy = y, x
			case 6: // rotated 90° clockwise
				dx, dy = h-1-y, x
			case 7: // transversed
				dx, dy = h-1-y, w-1-x
			case 8: // rotated 90° counter-clockwise
				dx, dy = y, w-1-x
			}
			i, j := src.PixOffset(x, y), dst.PixOffset(dx, dy)
			copy(dst.Pix[j:j+4], src.Pix[i:i+4])
		}
	}
	return dst
}
