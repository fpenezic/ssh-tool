package ssh

import (
	"encoding/hex"
	"testing"
)

// Minimal synthetic TLS ClientHello with a single SNI extension
// pointing at "example.com". Wrapped in a fake IP+TCP header (60
// bytes of zeros) so the scanner has to find the TLS record by
// signature, not by offset.
func TestDecodeTLSClientHelloSNI(t *testing.T) {
	// TLS Record: type=0x16 ver=0x0303 len=<computed>
	// Handshake: type=0x01 len=<24bit, computed> ver=0x0303
	// 32B random + 1B session_id_len(0)
	// 2B cipher_suites_len(2) + 2B cipher_suites(0x002f)
	// 1B compression_methods_len(1) + 1B method(0x00)
	// 2B extensions_len(<computed>)
	// Ext: type=0x0000 (SNI) len=<computed>
	//   2B list_len + 1B name_type(0) + 2B name_len + bytes(example.com)
	sni := "example.com"

	// Build SNI extension data
	nameLen := len(sni)
	extData := []byte{
		0x00, byte(nameLen + 3), // list length
		0x00,                   // name type = host_name
		byte(nameLen >> 8), byte(nameLen),
	}
	extData = append(extData, []byte(sni)...)

	ext := []byte{0x00, 0x00, byte(len(extData) >> 8), byte(len(extData))}
	ext = append(ext, extData...)

	// Body: ver + random(32) + sid(0) + cs(0x002f) + cm(0x00) + ext
	body := []byte{0x03, 0x03}
	body = append(body, make([]byte, 32)...) // random
	body = append(body, 0x00)               // session_id_len
	body = append(body, 0x00, 0x02, 0x00, 0x2f) // cipher_suites
	body = append(body, 0x01, 0x00)             // compression_methods
	body = append(body, byte(len(ext)>>8), byte(len(ext)))
	body = append(body, ext...)

	// Handshake header: type + uint24 length
	hsLen := len(body)
	hs := []byte{0x01, byte(hsLen >> 16), byte(hsLen >> 8), byte(hsLen)}
	hs = append(hs, body...)

	// Record header
	recLen := len(hs)
	rec := []byte{0x16, 0x03, 0x03, byte(recLen >> 8), byte(recLen)}
	rec = append(rec, hs...)

	// Prepend 60 bytes of fake IP+TCP header.
	pkt := append(make([]byte, 60), rec...)

	got := DecodeTLSClientHello(pkt)
	if got != sni {
		t.Errorf("DecodeTLSClientHello = %q, want %q", got, sni)
	}
}

func TestExtractHexPayload(t *testing.T) {
	// Mimic tcpdump -X output.
	lines := []string{
		"\t0x0000:  4500 0050 ee2c 4000 4006 0000 c0a8 0101  E..P.,@.@.......",
		"\t0x0010:  c0a8 0102 0035 1234 abcd ef01 23",
	}
	b := ExtractHexPayload(lines)
	if len(b) == 0 {
		t.Fatalf("no bytes extracted")
	}
	want := "45000050ee2c40004006"
	got := hex.EncodeToString(b[:10])
	if got != want {
		t.Errorf("first 10 bytes = %s, want %s", got, want)
	}
}
