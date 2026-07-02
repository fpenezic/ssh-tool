package ssh

import "testing"

func TestDecodeDHCPDiscover(t *testing.T) {
	header := "13:45:12.000000 IP (tos 0x0, ttl 64, id 0, offset 0, flags [none], proto UDP (17), length 328) 0.0.0.0.68 > 255.255.255.255.67: BOOTP/DHCP, Request from aa:bb:cc:dd:ee:ff, length 300"
	payload := []string{
		"\t  Client-Ethernet-Address aa:bb:cc:dd:ee:ff",
		"\t  Vendor-rfc1048 Extensions",
		"\t    Magic Cookie 0x63825363",
		"\t    DHCP-Message Option 53, length 1, Value: Discover",
		"\t    Requested-IP Option 50, length 4, Value: 10.0.0.5",
	}
	// We rely on the parser pulling src/dst+proto. For the test, fake
	// the header parse since decodeDHCP only needs proto/ports.
	p := &ParsedPacket{Proto: "udp", SrcPort: 68, DstPort: 67, Raw: header}
	d := Decode(p, payload)
	if d == nil || d.Type != "dhcp" {
		t.Fatalf("decode returned %+v", d)
	}
	if d.Fields["msg_type"] != "Discover" {
		t.Errorf("msg_type = %q, want Discover", d.Fields["msg_type"])
	}
	if d.Fields["requested_ip"] != "10.0.0.5" {
		t.Errorf("requested_ip = %q", d.Fields["requested_ip"])
	}
	if d.Summary == "" {
		t.Errorf("empty summary")
	}
}

func TestDecodeARP(t *testing.T) {
	d := decodeARPInfo("Request who-has 10.0.0.5 tell 10.0.0.1, length 28")
	if d.Fields["op"] != "request" {
		t.Errorf("op = %q", d.Fields["op"])
	}
	if d.Fields["target"] != "10.0.0.5" {
		t.Errorf("target = %q", d.Fields["target"])
	}
	if d.Fields["sender"] != "10.0.0.1" {
		t.Errorf("sender = %q", d.Fields["sender"])
	}

	d2 := decodeARPInfo("Reply 10.0.0.5 is-at aa:bb:cc:dd:ee:ff, length 28")
	if d2.Fields["op"] != "reply" {
		t.Errorf("op2 = %q", d2.Fields["op"])
	}
	if d2.Fields["target_mac"] != "aa:bb:cc:dd:ee:ff" {
		t.Errorf("mac = %q", d2.Fields["target_mac"])
	}
}

func TestDecodeBOOTPBogusRejected(t *testing.T) {
	// PacketCable / DOCSIS MTA provisioning rides UDP src port 67 to a
	// non-68 client port (2011), so tcpdump mislabels it "BOOTP/DHCP"
	// and prints a garbage header: op "unknown (0x89)", htype 136,
	// hlen 136, hops 136, and bogus IP fields (250.14.9.3, 3.89.253.191).
	// An earlier version of this decoder treated that as a real BOOTP
	// relay and surfaced the scrambled IPs - which is exactly the bug
	// where the Decode tab showed a dozen "Reply" stages and IPs like
	// "0.130.204.240". A non-Ethernet htype/hlen and an "unknown" op are
	// the tells; we must NOT decode it. It falls through to a plain UDP
	// row instead.
	header := "2026-05-25 08:42:35.383016 IP (tos 0x0, ttl 64, id 17997, offset 0, flags [DF], proto UDP (17), length 383) 10.250.14.9.67 > 10.239.248.62.2011: BOOTP/DHCP, unknown (0x89), length 355, htype 136, hlen 136, hops 136, xid 0x100ff59"
	payload := []string{
		"      Client-IP 250.14.9.3",
		"      Your-IP 3.89.253.191",
		"      Server-IP 37.2.75.225",
		"      Gateway-IP 19.106.0.0",
	}
	p := &ParsedPacket{Proto: "udp", SrcPort: 67, DstPort: 2011, Raw: header}
	if d := Decode(p, payload); d != nil {
		t.Fatalf("bogus PacketCable packet should not decode as DHCP, got %+v", d)
	}
}

func TestDecodeBOOTPRealRelay(t *testing.T) {
	// A genuine BOOTP relay (htype 1 Ethernet, hlen 6, real Reply op)
	// must still decode even on a non-standard client port.
	header := "2026-05-25 08:42:35.383016 IP (tos 0x0, ttl 64, id 17997, offset 0, flags [DF], proto UDP (17), length 383) 10.0.0.9.67 > 10.0.0.2.68: BOOTP/DHCP, Reply, length 300, htype 1, hlen 6, hops 0, xid 0x100ff59"
	payload := []string{
		"      Your-IP 10.0.0.55",
		"      Server-IP 10.0.0.1",
	}
	p := &ParsedPacket{Proto: "udp", SrcPort: 67, DstPort: 68, Raw: header}
	d := Decode(p, payload)
	if d == nil || d.Type != "dhcp" {
		t.Fatalf("real BOOTP relay should decode, got %+v", d)
	}
	if d.Fields["assigned_ip"] != "10.0.0.55" {
		t.Errorf("assigned_ip = %q", d.Fields["assigned_ip"])
	}
}

func TestParseVerbosePreambleWithIPsOnSecondLine(t *testing.T) {
	// Verbose tcpdump splits a packet across two lines: preamble
	// with proto/length, then "src.port > dst.port: ..." indented.
	// The Run loop joins them before parsing - simulate that here.
	joined := `2026-05-25 08:55:12.408005 IP (tos 0x0, ttl 64, id 54828, offset 0, flags [DF], proto UDP (17), length 589) 10.250.14.9.67 > 10.239.248.62.2011: BOOTP/DHCP, unknown (0x89), length 561, htype 136, hlen 136, hops 136, xid 0x1020355`
	p, ok := ParseTcpdumpLine(joined)
	if !ok {
		t.Fatalf("parse failed")
	}
	if p.Proto != "udp" {
		t.Errorf("proto = %q, want udp", p.Proto)
	}
	if p.SrcIP != "10.250.14.9" || p.SrcPort != 67 {
		t.Errorf("src = %s:%d", p.SrcIP, p.SrcPort)
	}
	if p.DstIP != "10.239.248.62" || p.DstPort != 2011 {
		t.Errorf("dst = %s:%d", p.DstIP, p.DstPort)
	}
	if p.FlowKey == "" {
		t.Errorf("empty flow key")
	}
}

func TestDecodeDNSQuery(t *testing.T) {
	header := "13:45:12.000 IP 10.0.0.1.40000 > 1.1.1.1.53: 1234+ A? example.com. (28)"
	p := &ParsedPacket{Proto: "udp", SrcPort: 40000, DstPort: 53, Raw: header}
	d := Decode(p, nil)
	if d == nil || d.Type != "dns" {
		t.Fatalf("decode = %+v", d)
	}
	if d.Fields["qtype"] != "A" || d.Fields["qname"] != "example.com" {
		t.Errorf("dns query parsed wrong: %+v", d.Fields)
	}
}

// toHexDumpLines renders a byte buffer the way tcpdump -X does, so
// ExtractHexPayload (which parses the hex column) can reconstruct it.
// 16 bytes per line, grouped in 2-byte words, with the "0xNNNN:" offset
// prefix and a trailing ASCII gloss the parser ignores.
func toHexDumpLines(b []byte) []string {
	var lines []string
	for off := 0; off < len(b); off += 16 {
		end := off + 16
		if end > len(b) {
			end = len(b)
		}
		row := b[off:end]
		hexCols := ""
		for i, c := range row {
			hexCols += sprintfByte(c)
			if i%2 == 1 {
				hexCols += " "
			}
		}
		lines = append(lines, "\t0x"+hex4(off)+":  "+hexCols+" gloss")
	}
	return lines
}

func sprintfByte(c byte) string {
	const hexdig = "0123456789abcdef"
	return string([]byte{hexdig[c>>4], hexdig[c&0x0f]})
}

func hex4(n int) string {
	const hexdig = "0123456789abcdef"
	return string([]byte{
		hexdig[(n>>12)&0xf], hexdig[(n>>8)&0xf],
		hexdig[(n>>4)&0xf], hexdig[n&0xf],
	})
}

func TestDecodeCWMPInform(t *testing.T) {
	// A minimal TR-069 Inform request body over HTTP, as it appears in
	// the TCP payload. Prefix 60 bytes of fake IP+TCP header so the
	// payload-offset finder has something to skip (or fall back from).
	body := "POST /acs HTTP/1.1\r\n" +
		"Host: acs.example\r\n" +
		"Content-Type: text/xml; charset=utf-8\r\n" +
		"SOAPAction: \"cwmp:Inform\"\r\n\r\n" +
		`<soap:Envelope xmlns:cwmp="urn:dslforum-org:cwmp-1-0">` +
		`<soap:Body><cwmp:Inform><DeviceId>` +
		`<Manufacturer>Acme</Manufacturer>` +
		`<OUI>00AABB</OUI>` +
		`<ProductClass>Gateway</ProductClass>` +
		`<SerialNumber>SN12345</SerialNumber>` +
		`</DeviceId>` +
		`<Event><EventStruct><EventCode>2 PERIODIC</EventCode></EventStruct></Event>` +
		`<ParameterList>` +
		`<ParameterValueStruct><Name>Device.DeviceInfo.SoftwareVersion</Name>` +
		`<Value xsi:type="xsd:string">1.2.3</Value></ParameterValueStruct>` +
		`</ParameterList></cwmp:Inform></soap:Body></soap:Envelope>`
	pkt := append(make([]byte, 60), []byte(body)...)
	lines := toHexDumpLines(pkt)

	p := &ParsedPacket{Proto: "tcp", SrcPort: 45734, DstPort: 7547}
	d := Decode(p, lines)
	if d == nil || d.Type != "cwmp" {
		t.Fatalf("decode returned %+v", d)
	}
	if d.Fields["method"] != "Inform" {
		t.Errorf("method = %q, want Inform", d.Fields["method"])
	}
	if d.Fields["serial"] != "SN12345" {
		t.Errorf("serial = %q, want SN12345", d.Fields["serial"])
	}
	if d.Fields["product_class"] != "Gateway" {
		t.Errorf("product_class = %q", d.Fields["product_class"])
	}
	if d.Fields["events"] != "2 PERIODIC" {
		t.Errorf("events = %q", d.Fields["events"])
	}
	if d.Fields["param.Device.DeviceInfo.SoftwareVersion"] != "1.2.3" {
		t.Errorf("param value = %q", d.Fields["param.Device.DeviceInfo.SoftwareVersion"])
	}
	if d.Summary == "" {
		t.Errorf("empty summary")
	}
}

func TestDecodeCWMPGetParameterValuesResponse(t *testing.T) {
	// The shape from the real capture: a GetParameterValuesResponse
	// with several ParameterValueStruct entries.
	body := "HTTP/1.1 200 OK\r\nContent-Type: text/xml\r\n\r\n" +
		`<soap:Envelope><soap:Body><cwmp:GetParameterValuesResponse><ParameterList>` +
		`<ParameterValueStruct><Name>Device.DeviceInfo.TemperatureStatus.TemperatureSensor.1.Status</Name>` +
		`<Value xsi:type="xsd:string">1</Value></ParameterValueStruct>` +
		`<ParameterValueStruct><Name>Device.DeviceInfo.TemperatureStatus.TemperatureSensor.1.Value</Name>` +
		`<Value xsi:type="xsd:int">0</Value></ParameterValueStruct>` +
		`</ParameterList></cwmp:GetParameterValuesResponse></soap:Body></soap:Envelope>`
	pkt := append(make([]byte, 60), []byte(body)...)
	lines := toHexDumpLines(pkt)

	p := &ParsedPacket{Proto: "tcp", SrcPort: 7547, DstPort: 45734}
	d := Decode(p, lines)
	if d == nil || d.Type != "cwmp" {
		t.Fatalf("decode returned %+v", d)
	}
	if d.Fields["method"] != "GetParameterValuesResponse" {
		t.Errorf("method = %q", d.Fields["method"])
	}
	if d.Fields["param.Device.DeviceInfo.TemperatureStatus.TemperatureSensor.1.Status"] != "1" {
		t.Errorf("status param = %q", d.Fields["param.Device.DeviceInfo.TemperatureStatus.TemperatureSensor.1.Status"])
	}
}

func TestDecodeCWMPContinuationNoMethod(t *testing.T) {
	body := `<ParameterValueStruct><Name>InternetGatewayDevice.LANDevice.1.Hosts.Host.8.IPAddress</Name>` +
		`<Value xsi:type="xsd:string">192.168.100.3</Value></ParameterValueStruct>`
	pkt := append(make([]byte, 60), []byte(body)...)
	lines := toHexDumpLines(pkt)
	p := &ParsedPacket{Proto: "tcp", SrcPort: 7547, DstPort: 51786}
	d := Decode(p, lines)
	if d == nil || d.Type != "cwmp" {
		t.Fatalf("decode returned %+v", d)
	}
	if d.Fields["method"] != "(continuation)" {
		t.Errorf("method = %q, want (continuation)", d.Fields["method"])
	}
	if d.Fields["param.InternetGatewayDevice.LANDevice.1.Hosts.Host.8.IPAddress"] != "192.168.100.3" {
		t.Errorf("param missing: %+v", d.Fields)
	}
}

func TestDecodeCWMPDeclinesNonCWMP(t *testing.T) {
	// A body with no CWMP markers: decodeCWMP must decline (return nil)
	// so the dispatch falls back to the HTTP / raw view instead of
	// mislabelling plain traffic on 7547 as CWMP.
	body := "GET /status HTTP/1.1\r\nHost: cpe\r\n\r\n"
	pkt := append(make([]byte, 60), []byte(body)...)
	lines := toHexDumpLines(pkt)
	p := &ParsedPacket{Proto: "tcp", SrcPort: 51786, DstPort: 7547}
	if d := decodeCWMP(p, lines); d != nil {
		t.Fatalf("expected nil for non-CWMP body, got %+v", d)
	}
}
