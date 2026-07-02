package ssh

import "testing"

func TestParseTcpdumpLine(t *testing.T) {
	cases := []struct {
		in        string
		wantOK    bool
		wantProto string
		wantSrc   string
		wantDst   string
		wantSPort int
		wantDPort int
		wantFlow  bool
	}{
		{
			in:        "13:45:12.345678 IP 10.0.0.1.443 > 10.0.0.2.51234: tcp 200",
			wantOK:    true,
			wantProto: "tcp",
			wantSrc:   "10.0.0.1",
			wantDst:   "10.0.0.2",
			wantSPort: 443,
			wantDPort: 51234,
			wantFlow:  true,
		},
		{
			in:        "2026-05-24 13:45:12.000 IP 192.168.1.5.53 > 192.168.1.10.40000: UDP, length 76",
			wantOK:    true,
			wantProto: "udp",
			wantSrc:   "192.168.1.5",
			wantDst:   "192.168.1.10",
			wantSPort: 53,
			wantDPort: 40000,
			wantFlow:  true,
		},
		{
			in:        "13:45:12.000 ARP, Request who-has 10.0.0.5 tell 10.0.0.1, length 28",
			wantOK:    true,
			wantProto: "arp",
			wantFlow:  true,
		},
		{
			in:        "13:45:12.000 IP6 fe80::1.443 > fe80::2.1234: tcp 100",
			wantOK:    true,
			wantProto: "tcp",
			wantSrc:   "fe80::1",
			wantDst:   "fe80::2",
			wantSPort: 443,
			wantDPort: 1234,
			wantFlow:  true,
		},
		{
			in:     "garbage that doesn't match",
			wantOK: false,
		},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			p, ok := ParseTcpdumpLine(c.in)
			if ok != c.wantOK {
				t.Fatalf("ok = %v, want %v", ok, c.wantOK)
			}
			if !ok {
				return
			}
			if p.Proto != c.wantProto {
				t.Errorf("proto = %q, want %q", p.Proto, c.wantProto)
			}
			if c.wantSrc != "" && p.SrcIP != c.wantSrc {
				t.Errorf("src = %q, want %q", p.SrcIP, c.wantSrc)
			}
			if c.wantDst != "" && p.DstIP != c.wantDst {
				t.Errorf("dst = %q, want %q", p.DstIP, c.wantDst)
			}
			if c.wantSPort != 0 && p.SrcPort != c.wantSPort {
				t.Errorf("sport = %d, want %d", p.SrcPort, c.wantSPort)
			}
			if c.wantDPort != 0 && p.DstPort != c.wantDPort {
				t.Errorf("dport = %d, want %d", p.DstPort, c.wantDPort)
			}
			if c.wantFlow && p.FlowKey == "" {
				t.Errorf("flow key empty")
			}
		})
	}
}

func TestFlowKeyDirectionIndependent(t *testing.T) {
	a, _ := ParseTcpdumpLine("13:00:00.000 IP 10.0.0.1.443 > 10.0.0.2.51234: tcp 200")
	b, _ := ParseTcpdumpLine("13:00:00.001 IP 10.0.0.2.51234 > 10.0.0.1.443: tcp 60")
	if a.FlowKey != b.FlowKey {
		t.Errorf("flow keys differ for same conversation: %q vs %q", a.FlowKey, b.FlowKey)
	}
}
