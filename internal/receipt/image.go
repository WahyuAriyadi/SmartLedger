package receipt

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/draw"
	"image/jpeg"
	_ "image/png" // decoder PNG
	"net/http"

	"smartledger/internal/apperr"
)

const (
	MaxUploadBytes = 5 << 20
	maxPixels      = 25_000_000 // cegah decompression bomb: PNG kecil bisa mengembang jadi gigabyte
	minSide        = 100
)

// SanitizeImage memvalidasi dan membersihkan unggahan:
//   - tipe ditentukan dari ISI file (magic bytes), bukan dari nama/Content-Type kiriman klien;
//   - dimensi diperiksa sebelum decode penuh;
//   - gambar di-encode ulang ke JPEG, sehingga metadata (GPS lokasi, tipe kamera) dan
//     payload tersembunyi/polyglot terbuang. Orientasi EXIF diterapkan lebih dulu agar foto tidak miring.
func SanitizeImage(raw []byte) ([]byte, error) {
	bad := func(msg string) error { return apperr.Validation("invalid_image", msg) }
	if len(raw) == 0 {
		return nil, bad("file gambar kosong")
	}
	if t := http.DetectContentType(raw); t != "image/jpeg" && t != "image/png" {
		return nil, bad("format gambar harus JPEG atau PNG")
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return nil, bad("gambar rusak atau tidak bisa dibaca")
	}
	if cfg.Width < minSide || cfg.Height < minSide {
		return nil, bad("gambar terlalu kecil")
	}
	if cfg.Width*cfg.Height > maxPixels {
		return nil, bad("resolusi gambar terlalu besar")
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, bad("gambar rusak atau tidak bisa dibaca")
	}
	img = applyOrientation(img, exifOrientation(raw))

	// Ratakan ke RGBA berlatar putih (PNG transparan tidak jadi hitam).
	b := img.Bounds()
	flat := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(flat, flat.Bounds(), image.White, image.Point{}, draw.Src)
	draw.Draw(flat, flat.Bounds(), img, b.Min, draw.Over)

	var out bytes.Buffer
	if err := jpeg.Encode(&out, flat, &jpeg.Options{Quality: 85}); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// exifOrientation membaca tag Orientation (0x0112) dari segmen APP1 JPEG; 1 bila tidak ada.
func exifOrientation(raw []byte) int {
	if len(raw) < 4 || raw[0] != 0xFF || raw[1] != 0xD8 {
		return 1
	}
	i := 2
	for i+4 <= len(raw) && raw[i] == 0xFF {
		marker := raw[i+1]
		if marker == 0xDA || marker == 0xD9 { // mulai data gambar
			break
		}
		segLen := int(binary.BigEndian.Uint16(raw[i+2 : i+4]))
		if segLen < 2 || i+2+segLen > len(raw) {
			break
		}
		if marker == 0xE1 {
			if o := parseExif(raw[i+4 : i+2+segLen]); o != 0 {
				return o
			}
		}
		i += 2 + segLen
	}
	return 1
}

func parseExif(seg []byte) int {
	if len(seg) < 14 || string(seg[:6]) != "Exif\x00\x00" {
		return 0
	}
	t := seg[6:]
	var bo binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return 0
	}
	ifd := int(bo.Uint32(t[4:8]))
	if ifd < 8 || ifd+2 > len(t) {
		return 0
	}
	n := int(bo.Uint16(t[ifd : ifd+2]))
	for k := 0; k < n; k++ {
		e := ifd + 2 + k*12
		if e+12 > len(t) {
			return 0
		}
		if bo.Uint16(t[e:e+2]) == 0x0112 {
			if v := int(bo.Uint16(t[e+8 : e+10])); v >= 1 && v <= 8 {
				return v
			}
		}
	}
	return 0
}

// applyOrientation memutar gambar sesuai EXIF (3 = 180°, 6 = 90° CW, 8 = 90° CCW).
// Varian cermin (2,4,5,7) sangat jarang pada foto kamera dan diabaikan.
func applyOrientation(src image.Image, o int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	switch o {
	case 3:
		dst := image.NewRGBA(image.Rect(0, 0, w, h))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				dst.Set(w-1-x, h-1-y, src.At(b.Min.X+x, b.Min.Y+y))
			}
		}
		return dst
	case 6:
		dst := image.NewRGBA(image.Rect(0, 0, h, w))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				dst.Set(h-1-y, x, src.At(b.Min.X+x, b.Min.Y+y))
			}
		}
		return dst
	case 8:
		dst := image.NewRGBA(image.Rect(0, 0, h, w))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				dst.Set(y, w-1-x, src.At(b.Min.X+x, b.Min.Y+y))
			}
		}
		return dst
	}
	return src
}
