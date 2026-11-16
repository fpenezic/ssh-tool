package catalog

// Icon images (PROTOCOL 3.6): a content-addressed store on the catalog
// server. A record's icon_image is "sha256:<hex>" of the image bytes, so
// the app knows the address of an image it holds without asking.
//
// CheckImage is the server's own check, so an image the server would
// refuse is caught before a publish and named in the review instead of
// failing the request: same signature sniff (PNG, JPEG, WebP only), the
// size and dimension limits, a full decode (an animated WebP does not
// decode), and no bytes after the end of the image.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"strings"

	"golang.org/x/image/webp"
)

// ImageLimits is meta.images.
type ImageLimits struct {
	MaxBytes  int      `json:"max_bytes"`
	MaxWidth  int      `json:"max_width"`
	MaxHeight int      `json:"max_height"`
	Types     []string `json:"types"`
}

// DefaultImageLimits are the protocol's (3.6), used when meta leaves a
// value out.
var DefaultImageLimits = ImageLimits{MaxBytes: 256 << 10, MaxWidth: 1024, MaxHeight: 1024,
	Types: []string{"image/png", "image/jpeg", "image/webp"}}

// ImagesSupported: the server has the image store (revision 2 or later,
// meta.images present). Against an older server uploaded icons stay in
// the publisher's local layer.
func (m *Meta) ImagesSupported() bool {
	return m != nil && m.Protocol.Revision >= 2 && m.Images != nil
}

// ImageLimitsOf returns meta's limits with the protocol's defaults for
// values it leaves out.
func (m *Meta) ImageLimitsOf() ImageLimits {
	l := DefaultImageLimits
	if m == nil || m.Images == nil {
		return l
	}
	if m.Images.MaxBytes > 0 {
		l.MaxBytes = m.Images.MaxBytes
	}
	if m.Images.MaxWidth > 0 {
		l.MaxWidth = m.Images.MaxWidth
	}
	if m.Images.MaxHeight > 0 {
		l.MaxHeight = m.Images.MaxHeight
	}
	if len(m.Images.Types) > 0 {
		l.Types = m.Images.Types
	}
	return l
}

// ImageRef is the record value for bytes: "sha256:<hex>".
func ImageRef(b []byte) string { return "sha256:" + ImageSHA256(b) }

// ImageSHA256 is the lowercase hex SHA-256 of b.
func ImageSHA256(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// ImageHash returns the hex part of a record's icon_image value.
func ImageHash(v string) (string, bool) {
	const prefix = "sha256:"
	h, ok := strings.CutPrefix(v, prefix)
	if !ok || len(h) != 64 {
		return "", false
	}
	for i := 0; i < len(h); i++ {
		c := h[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return "", false
		}
	}
	return h, true
}

// ImageRefusal is why an image cannot go to the catalog. Reason is the
// server's: "too_large", "unsupported_type", "dimensions", "decode",
// "trailing_data" (and "hash_mismatch" from the server only).
type ImageRefusal struct {
	Reason  string
	Message string
}

func (e *ImageRefusal) Error() string { return e.Message }

func refuse(reason, format string, args ...any) *ImageRefusal {
	return &ImageRefusal{Reason: reason, Message: fmt.Sprintf(format, args...)}
}

// SniffImage returns the image type from the bytes' signature, "" for
// anything but PNG, JPEG and WebP.
func SniffImage(b []byte) string {
	switch {
	case len(b) >= 8 && bytes.Equal(b[:8], []byte("\x89PNG\r\n\x1a\n")):
		return "image/png"
	case len(b) >= 3 && b[0] == 0xff && b[1] == 0xd8 && b[2] == 0xff:
		return "image/jpeg"
	case len(b) >= 12 && string(b[0:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return "image/webp"
	}
	return ""
}

// CheckImage checks bytes the way the server does and returns their type.
func CheckImage(b []byte, l ImageLimits) (string, error) {
	if len(b) > l.MaxBytes {
		return "", refuse("too_large", "the image is %d KiB; the catalog takes at most %d KiB", (len(b)+1023)/1024, l.MaxBytes/1024)
	}
	typ := SniffImage(b)
	allowed := false
	for _, t := range l.Types {
		allowed = allowed || t == typ
	}
	if typ == "" || !allowed {
		return "", refuse("unsupported_type", "the catalog takes PNG, JPEG and WebP images only")
	}
	var decodeConfig func(io.Reader) (image.Config, error)
	var decode func(io.Reader) (image.Image, error)
	switch typ {
	case "image/png":
		decodeConfig, decode = png.DecodeConfig, png.Decode
	case "image/jpeg":
		decodeConfig, decode = jpeg.DecodeConfig, jpeg.Decode
	case "image/webp":
		decodeConfig, decode = webp.DecodeConfig, webp.Decode
		n := int(binary.LittleEndian.Uint32(b[4:8]))
		if n+8 != len(b) && !(n%2 == 1 && n+9 == len(b)) {
			if n+8 < len(b) {
				return "", refuse("trailing_data", "the file has data after the end of the image")
			}
			return "", refuse("decode", "the image is truncated")
		}
	}
	cfg, err := decodeConfig(bytes.NewReader(b))
	if err != nil {
		return "", refuse("decode", "the image header cannot be read")
	}
	if cfg.Width < 1 || cfg.Height < 1 || cfg.Width > l.MaxWidth || cfg.Height > l.MaxHeight {
		return "", refuse("dimensions", "the image is %dx%d; the catalog takes at most %dx%d", cfg.Width, cfg.Height, l.MaxWidth, l.MaxHeight)
	}
	r := bytes.NewReader(b)
	img, err := decode(r)
	if err != nil {
		return "", refuse("decode", "the image does not decode completely (an animated image, or a damaged file)")
	}
	if bb := img.Bounds(); bb.Dx() != cfg.Width || bb.Dy() != cfg.Height {
		return "", refuse("decode", "the image data does not match its header")
	}
	switch typ {
	case "image/png":
		if r.Len() > 0 {
			return "", refuse("trailing_data", "the file has data after the end of the image")
		}
	case "image/jpeg":
		end := jpegEnd(b)
		if end < 0 {
			return "", refuse("decode", "the image does not decode completely")
		}
		if end != len(b) {
			return "", refuse("trailing_data", "the file has data after the end of the image")
		}
	}
	return typ, nil
}

// jpegEnd walks the JPEG segments and returns the offset just past the
// end-of-image marker, or -1.
func jpegEnd(b []byte) int {
	i := 2 // after SOI
	for {
		if i >= len(b) || b[i] != 0xff {
			return -1
		}
		for i < len(b) && b[i] == 0xff {
			i++
		}
		if i >= len(b) {
			return -1
		}
		m := b[i]
		i++
		switch {
		case m == 0xd9:
			return i
		case m == 0x01 || m >= 0xd0 && m <= 0xd7:
			continue
		}
		if i+2 > len(b) {
			return -1
		}
		n := int(b[i])<<8 | int(b[i+1])
		if n < 2 || i+n > len(b) {
			return -1
		}
		i += n
		if m == 0xda {
			for i < len(b)-1 {
				if b[i] == 0xff {
					x := b[i+1]
					if x == 0x00 || x >= 0xd0 && x <= 0xd7 {
						i += 2
						continue
					}
					break
				}
				i++
			}
			if i >= len(b)-1 {
				return -1
			}
		}
	}
}

// ServerImageRefusal turns a refused PUT into an ImageRefusal (nil when
// err is not one): 413, 415, or 422 with details.reason.
func ServerImageRefusal(err error) *ImageRefusal {
	var ae *APIError
	if !errors.As(err, &ae) {
		return nil
	}
	switch {
	case ae.Status == http.StatusRequestEntityTooLarge:
		return &ImageRefusal{Reason: "too_large", Message: "the catalog refused the image: too large"}
	case ae.Status == http.StatusUnsupportedMediaType:
		return &ImageRefusal{Reason: "unsupported_type", Message: "the catalog refused the image: not PNG, JPEG or WebP"}
	case ae.Status == http.StatusUnprocessableEntity:
		var d struct {
			Reason string `json:"reason"`
		}
		_ = json.Unmarshal(ae.Details, &d)
		if d.Reason == "" {
			d.Reason = "decode"
		}
		msg := ae.Message
		if msg == "" {
			msg = d.Reason
		}
		return &ImageRefusal{Reason: d.Reason, Message: "the catalog refused the image: " + msg}
	}
	return nil
}

// HeadImage reports whether the store holds the image.
func (c *Client) HeadImage(ctx context.Context, hexHash string) (bool, error) {
	status, _, _, err := c.raw(ctx, http.MethodHead, "/api/v1/images/"+hexHash, nil, nil)
	if err != nil {
		return false, err
	}
	switch status {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	}
	return false, &APIError{Status: status}
}

// PutImage uploads image bytes to their address. A 429 is a
// RetryAfterError; a refusal an APIError (see ServerImageRefusal).
func (c *Client) PutImage(ctx context.Context, hexHash string, data []byte) error {
	status, _, body, err := c.raw(ctx, http.MethodPut, "/api/v1/images/"+hexHash, data, map[string]string{"Content-Type": "application/octet-stream"})
	if err != nil {
		return err
	}
	if status == http.StatusOK || status == http.StatusCreated {
		return nil
	}
	return parseAPIError(status, body)
}

// GetImage fetches image bytes and the type the server determined.
func (c *Client) GetImage(ctx context.Context, hexHash string) (string, []byte, error) {
	status, h, body, err := c.raw(ctx, http.MethodGet, "/api/v1/images/"+hexHash, nil, map[string]string{"Accept": "image/*"})
	if err != nil {
		return "", nil, err
	}
	if status != http.StatusOK {
		return "", nil, parseAPIError(status, body)
	}
	return h.Get("Content-Type"), body, nil
}

// raw sends one authenticated request with a raw body and returns the
// status, headers and body as they came (429 / 503 as RetryAfterError).
// A 401 refreshes the access token once and retries.
func (c *Client) raw(ctx context.Context, method, path string, body []byte, headers map[string]string) (int, http.Header, []byte, error) {
	staleGen := -1
	for attempt := 0; attempt < 2; attempt++ {
		var rd io.Reader
		if body != nil {
			rd = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, rd)
		if err != nil {
			return 0, nil, nil, err
		}
		req.Header.Set("Accept", "application/json")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		c.setUA(req)
		tok, gen, err := c.token(ctx, staleGen)
		if err != nil {
			return 0, nil, nil, err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := c.HTTP.Do(req)
		if err != nil {
			return 0, nil, nil, fmt.Errorf("catalog: %w", err)
		}
		out, rerr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if rerr != nil {
			return 0, nil, nil, fmt.Errorf("catalog: read response: %w", rerr)
		}
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			staleGen = gen
			continue
		}
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable {
			return 0, nil, nil, &RetryAfterError{Status: resp.StatusCode, Wait: retryAfter(resp.Header.Get("Retry-After"))}
		}
		return resp.StatusCode, resp.Header, out, nil
	}
	return 0, nil, nil, ErrSignedOut
}
