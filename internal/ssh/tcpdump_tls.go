package ssh

import (
	"encoding/hex"
	"regexp"
	"strings"
)

// ExtractHexPayload pulls the raw packet bytes out of tcpdump's
// `-X` output. Lines look like:
//
//	0x0000:  4500 0050 ee2c 4000 4006 ...   E..P.,@.@.
//	0x0010:  c0a8 0101 c0a8 0102 ...
//
// We grab the hex tokens between the offset and the ASCII gloss.
// Returns the concatenated bytes or nil if the lines don't match.
func ExtractHexPayload(lines []string) []byte {
	var hexStr strings.Builder
	for _, l := range lines {
		m := reHexLine.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		// m[1] is the hex region. Strip spaces, the ASCII tail is
		// already excluded by the regex.
		hexStr.WriteString(strings.ReplaceAll(m[1], " ", ""))
	}
	if hexStr.Len() == 0 {
		return nil
	}
	b, err := hex.DecodeString(hexStr.String())
	if err != nil {
		return nil
	}
	return b
}

// Hex dump line: "  0x0000:  4500 0050 1234 ...  E..P..."
// Capture only the hex region between the offset and the ASCII gloss
// (gloss starts after at least two spaces following the hex words).
var reHexLine = regexp.MustCompile(`0x[0-9a-fA-F]+:\s+((?:[0-9a-fA-F]{2,4}\s+)+)`)

// DecodeTLSClientHello scans a packet's bytes for a TLS handshake
// ClientHello and returns the SNI hostname when present. Returns
// empty string on any parse failure (truncated packet, not a
// ClientHello, malformed extension list, etc).
//
// Layout we walk (RFC 8446, simplified):
//
//	TLS record:
//	  uint8  content_type   (0x16 = Handshake)
//	  uint16 version
//	  uint16 length
//	  Handshake:
//	    uint8  handshake_type (0x01 = ClientHello)
//	    uint24 length
//	    uint16 version
//	    32B    random
//	    uint8  session_id_len  + session_id
//	    uint16 cipher_suites_len + cipher_suites
//	    uint8  compression_methods_len + compression_methods
//	    uint16 extensions_len
//	    Extensions:
//	      uint16 type
//	      uint16 length
//	      data
//	      (type 0x0000 = server_name)
//	    server_name extension data:
//	      uint16 list_length
//	      uint8  name_type (0 = host_name)
//	      uint16 name_length
//	      bytes  hostname
//
// The packet bytes we get from tcpdump include the IP + TCP headers
// up front, so the first thing we do is scan for the handshake
// signature rather than assume a fixed offset.
func DecodeTLSClientHello(packet []byte) string {
	if len(packet) < 50 {
		return ""
	}
	// Scan for the TLS Handshake signature (0x16 0x03 0x0[1-4]) and
	// the ClientHello tag (0x01) at the expected handshake offset.
	for i := 0; i <= len(packet)-10; i++ {
		if packet[i] != 0x16 {
			continue
		}
		if packet[i+1] != 0x03 {
			continue
		}
		// TLS versions: 0x0301 (TLS 1.0) .. 0x0304 (TLS 1.3).
		if packet[i+2] < 0x01 || packet[i+2] > 0x04 {
			continue
		}
		// record_length at i+3..i+5; handshake_type at i+5.
		if packet[i+5] != 0x01 {
			continue
		}
		// Walk the ClientHello body from i+5.
		host := walkClientHello(packet[i+5:])
		if host != "" {
			return host
		}
	}
	return ""
}

func walkClientHello(b []byte) string {
	// b[0] = handshake type (0x01), already checked.
	if len(b) < 6 || b[0] != 0x01 {
		return ""
	}
	// uint24 length at b[1..3]
	hsLen := int(b[1])<<16 | int(b[2])<<8 | int(b[3])
	body := b[4:]
	if hsLen > len(body) {
		// Truncated packet, but we may still find SNI before the cut.
		hsLen = len(body)
	}
	body = body[:hsLen]

	// Skip version(2) + random(32)
	if len(body) < 2+32 {
		return ""
	}
	body = body[2+32:]

	// session_id
	if len(body) < 1 {
		return ""
	}
	sl := int(body[0])
	if 1+sl > len(body) {
		return ""
	}
	body = body[1+sl:]

	// cipher_suites length (u16)
	if len(body) < 2 {
		return ""
	}
	cs := int(body[0])<<8 | int(body[1])
	if 2+cs > len(body) {
		return ""
	}
	body = body[2+cs:]

	// compression_methods length (u8)
	if len(body) < 1 {
		return ""
	}
	cm := int(body[0])
	if 1+cm > len(body) {
		return ""
	}
	body = body[1+cm:]

	// extensions_length (u16)
	if len(body) < 2 {
		return ""
	}
	extLen := int(body[0])<<8 | int(body[1])
	body = body[2:]
	if extLen > len(body) {
		extLen = len(body)
	}
	ext := body[:extLen]

	// Walk extensions.
	for len(ext) >= 4 {
		etype := int(ext[0])<<8 | int(ext[1])
		elen := int(ext[2])<<8 | int(ext[3])
		if 4+elen > len(ext) {
			return ""
		}
		edata := ext[4 : 4+elen]
		ext = ext[4+elen:]

		if etype == 0x0000 {
			// server_name extension
			// uint16 list_length
			if len(edata) < 5 {
				return ""
			}
			// name_type at offset 2 (must be 0 = host_name)
			if edata[2] != 0 {
				return ""
			}
			nameLen := int(edata[3])<<8 | int(edata[4])
			if 5+nameLen > len(edata) {
				return ""
			}
			return string(edata[5 : 5+nameLen])
		}
	}
	return ""
}
