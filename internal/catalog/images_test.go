package catalog

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

func pngOf(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.RGBA{R: 200, A: 255})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func jpegOf(t *testing.T, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, image.NewRGBA(image.Rect(0, 0, w, h)), nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// The client check names the same reasons the server refuses with
// (PROTOCOL 3.6), so the publish review can say why an icon stays local.
func TestCheckImageReasons(t *testing.T) {
	l := DefaultImageLimits
	p := pngOf(t, 16, 16)
	j := jpegOf(t, 16, 16)
	for _, tc := range []struct {
		name   string
		data   []byte
		reason string
	}{
		{"png", p, ""},
		{"jpeg", j, ""},
		{"png trailing", append(append([]byte{}, p...), "zip"...), "trailing_data"},
		{"jpeg trailing", append(append([]byte{}, j...), 0, 1, 2), "trailing_data"},
		{"png truncated", p[:len(p)-20], "decode"},
		{"too wide", pngOf(t, 1025, 4), "dimensions"},
		{"svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), "unsupported_type"},
		{"gif", []byte("GIF89a\x01\x00\x01\x00"), "unsupported_type"},
		{"too large", append(append([]byte{}, p...), make([]byte, l.MaxBytes)...), "too_large"},
		{"webp short riff", []byte("RIFF\xff\x00\x00\x00WEBPVP8 "), "decode"},
	} {
		typ, err := CheckImage(tc.data, l)
		if tc.reason == "" {
			if err != nil || typ == "" {
				t.Errorf("%s: refused: %v", tc.name, err)
			}
			continue
		}
		r, ok := err.(*ImageRefusal)
		if !ok || r.Reason != tc.reason {
			t.Errorf("%s: got %v, want reason %s", tc.name, err, tc.reason)
		}
	}
}

func TestImageHash(t *testing.T) {
	p := pngOf(t, 2, 2)
	h, ok := ImageHash(ImageRef(p))
	if !ok || h != ImageSHA256(p) {
		t.Fatalf("round trip: %q %v", h, ok)
	}
	for _, bad := range []string{"", "sha256:", "sha256:ABC", "md5:" + h, "sha256:" + h[:63] + "G"} {
		if _, ok := ImageHash(bad); ok {
			t.Errorf("%q accepted", bad)
		}
	}
}
