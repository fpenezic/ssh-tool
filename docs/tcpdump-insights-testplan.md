# tcpdump Insights - test scenarios

How to provoke each of the five detectors on a Linux test host, so you
can watch them land in the Insights tab live.

## Setup

1. Build + run the app locally (`task linux:build` then `./bin/ssh-tool`,
   or the dev loop). Connect to a Linux test host you can run commands on.
2. Open the tcpdump modal for that session.
3. Pick the interface the traffic will ride (often `any` is easiest for
   testing - it catches loopback + every NIC).
4. **Tick "Insights" (on by default).** For the TCP scenarios (half-open,
   RST storm) **also tick "Verbose"** - brief `-q` output omits the TCP
   flag field (`[S]`, `[S.]`, `[R]`) the analyzer keys on. UDP / ICMP /
   ARP scenarios work without Verbose.
5. Press Start, then run the provoking command(s) below in a separate
   shell on the host. Findings appear in the Insights tab (badge turns
   red). Click "Check route" on a finding to run the active confirmation.

A loose BPF filter keeps the capture readable while testing, e.g.
`icmp or arp or udp port 5353 or tcp port 9999`.

---

## 1. ICMP unreachable  (easiest - no Verbose needed)

The host tries to reach something with no route / a closed UDP port and
gets an ICMP error back.

```sh
# Closed UDP port -> "port unreachable" comes straight back from the peer.
# Point it at a host that's up but isn't listening on the port.
nmap -sU -p 9999 <some-reachable-host>     # or:
echo hi | nc -u -w1 <some-reachable-host> 9999

# Or force a "network unreachable" by routing to a black hole:
ping -c1 -W1 192.0.2.1     # TEST-NET-1, usually unrouteable -> may yield
                           # "Destination Net Unreachable" from a router
```

Expected: **icmp_unreachable** (warn). The detector fires on any ICMP
line containing "unreachable".

---

## 2. ICMP redirect / TTL exceeded

```sh
# TTL exceeded: a traceroute deliberately sends low-TTL packets and the
# routers along the path answer "time exceeded".
traceroute -n 8.8.8.8        # or: ping -c1 -t1 8.8.8.8
```

Expected: **ttl_exceeded** (warn) once a router replies. Redirect
(**icmp_redirect**) is harder to force on demand - it needs a host using
a suboptimal gateway; skip unless your topology already does this.

---

## 3. ARP for an off-subnet address

Make the host ARP for an IP outside its own subnet. Normally the kernel
sends off-link traffic to the gateway, so you have to bypass the routing
table to provoke a direct ARP (simulating a wrong netmask).

```sh
# Add a bogus /32 route that forces direct delivery (root needed).
# Replace eth0 with the capture interface; 203.0.113.7 is TEST-NET-3.
sudo ip route add 203.0.113.7/32 dev eth0
ping -c1 -W1 203.0.113.7          # host now ARPs for an off-subnet IP
sudo ip route del 203.0.113.7/32  # clean up
```

Expected: **arp_off_subnet** (warn). Requires the analyzer to know the
host's subnets - the app probes them automatically on Start
(`ip -o addr show`). If the host has no usable CIDR this check stays
silent by design.

---

## 4. Half-open TCP  (needs Verbose)

A SYN that never gets a SYN-ACK. Aim at a filtered port - one a firewall
drops silently, so nothing comes back at all (a *refused* port sends a
RST, which is a completed-ish handshake, not half-open).

```sh
# A DROP'd port: add a local rule that black-holes inbound SYNs.
sudo iptables -A INPUT -p tcp --dport 9999 -j DROP
# Now knock on it from elsewhere, or loopback won't traverse INPUT;
# use the host's own external IP so the packet hits the filter:
timeout 5 nc -w4 <host-external-ip> 9999
sudo iptables -D INPUT -p tcp --dport 9999 -j DROP   # clean up

# Simpler if you have an unrouteable/filtered destination handy:
timeout 5 nc -w4 192.0.2.1 9999    # SYN goes out, nothing returns
```

Expected: **half_open** (error) ~3s after the SYN (the Sweep grace
window). If you instead hit a *closed* port you'll get a RST and no
half-open - that's correct.

---

## 5. RST storm  (needs Verbose)

Five or more RSTs on one flow. Easiest: hammer a closed port repeatedly
so each connection attempt is reset.

```sh
# Closed (not filtered) port -> kernel answers each SYN with a RST.
for i in $(seq 1 8); do
  timeout 1 nc -w1 127.0.0.1 9999   # nothing listening on 9999 -> RST
done
```

Expected: **rst_storm** (warn) once the flow crosses 5 RSTs. Because the
flow key is direction-independent, all eight attempts on the same
4-tuple count together.

---

## 6. UDP reply from the wrong source IP  (the headline case)

The 0.0.0.0-bind / wrong-return-interface problem. This needs a real
multi-homed setup to occur naturally, but you can reproduce the *packet
shape* the detector keys on: a UDP request to IP A, answered from a
different source IP B on the same flow.

Natural reproduction (multi-homed host):

```
- Host has two interfaces, e.g. 10.0.0.5 (eth0) and 192.168.1.5 (eth1).
- A UDP service binds 0.0.0.0:5000.
- A client on the eth1 side sends to 192.168.1.5:5000, but the host's
  default route for the client's subnet egresses eth0, so the reply
  leaves with src 10.0.0.5 instead of 192.168.1.5.
- Capture on `any`: you see  client -> 192.168.1.5  then  10.0.0.5 ->
  client. Different reply source = the finding.
```

Synthetic reproduction (single host, two loopback aliases) - proves the
detector without needing two NICs:

```sh
# Two source addresses on loopback.
sudo ip addr add 127.0.1.1/32 dev lo
sudo ip addr add 127.0.2.2/32 dev lo

# Terminal A: a "server" that receives on .1.1 but replies from .2.2,
# mimicking a 0.0.0.0 service whose return path picks the wrong source.
#   socat sends its reply from 127.0.2.2 even though the request hit .1.1
socat -v UDP4-RECVFROM:5000,bind=127.0.1.1,fork \
      UDP4-SENDTO:127.0.0.1:0,bind=127.0.2.2 2>/dev/null &

# Terminal B (capture on `any`, BPF `udp port 5000 or udp port 0`),
# then fire a request at the address the client "thinks" it's using:
echo probe | nc -u -w1 127.0.1.1 5000

# cleanup
sudo ip addr del 127.0.1.1/32 dev lo
sudo ip addr del 127.0.2.2/32 dev lo
```

Expected: **udp_src_mismatch** (error) - "Client 127.0.0.1 sent to
127.0.1.1 but the reply came back from 127.0.2.2". Click "Check route":
it runs `ip route get 127.0.0.1 from 127.0.1.1` and the plain
`ip route get 127.0.0.1`, showing the dev/src the kernel actually picks.

> The synthetic socat trick is fiddly; if it doesn't line up on your
> host, the natural multi-homed case is the real target and the unit
> tests already cover the exact packet logic.

---

## Quick reference

| Detector          | Severity | Verbose? | Quickest provoke                         |
|-------------------|----------|----------|------------------------------------------|
| icmp_unreachable  | warn     | no       | `nc -u -w1 <host> 9999` (closed UDP)     |
| ttl_exceeded      | warn     | no       | `traceroute -n 8.8.8.8`                  |
| arp_off_subnet    | warn     | no       | `ip route add <test-net>/32 dev <if>` + ping |
| half_open         | error    | **yes**  | SYN to a DROP'd port, wait ~3s           |
| rst_storm         | warn     | **yes**  | loop `nc -w1 127.0.0.1 9999` x8          |
| udp_src_mismatch  | error    | no       | multi-homed 0.0.0.0 service, or socat trick |
