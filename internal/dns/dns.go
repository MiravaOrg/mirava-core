// Package dns implements DNS-provider-based mirror testing: resolve a
// mirror's hostname through different resolvers (UDP, DoH, DoT, or the
// system) and probe connectivity to the mirror via each provider's IPs.
// Diverging answers can reveal poisoning, filtering or broken geo-DNS.
package dns

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/MiravaOrg/mirava-core/internal/httpx"
	"golang.org/x/net/dns/dnsmessage"
)

// Provider resolves names through one specific DNS resolver.
type Provider interface {
	// Name identifies the provider in results ("system", "cloudflare", ...).
	Name() string
	// Resolve returns the A/AAAA addresses for host.
	Resolve(ctx context.Context, host string) ([]net.IP, error)
	// RTT measures a resolution round-trip (typically via Resolve timing).
	RTT(ctx context.Context, host string) (time.Duration, error)
}

// --- UDP provider -------------------------------------------------------

// UDP resolves via a classic UDP DNS server (e.g. "1.1.1.1:53").
type UDP struct {
	Addr string // host:port; port 53 appended if missing
}

// Name implements Provider.
func (u UDP) Name() string { return "udp:" + u.Addr }

func (u UDP) addr() string {
	if strings.Contains(u.Addr, ":") {
		return u.Addr
	}
	return net.JoinHostPort(u.Addr, "53")
}

// Resolve implements Provider.
func (u UDP) Resolve(ctx context.Context, host string) ([]net.IP, error) {
	ips, err := u.queryIPs(ctx, host)
	if err != nil {
		return nil, err
	}
	return ips, nil
}

func (u UDP) queryIPs(ctx context.Context, host string) ([]net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		return []net.IP{ip}, nil
	}
	// Query A then AAAA; return whichever answers.
	a, aErr := u.queryType(ctx, host, dnsmessage.TypeA)
	aaaa, aaaaErr := u.queryType(ctx, host, dnsmessage.TypeAAAA)
	if aErr != nil && aaaaErr != nil {
		return nil, fmt.Errorf("dns query failed for %q via %s: %w", host, u.addr(), aErr)
	}
	ips := append(a, aaaa...)
	if len(ips) == 0 {
		return nil, fmt.Errorf("no addresses for %q via %s", host, u.addr())
	}
	return ips, nil
}

func (u UDP) queryType(ctx context.Context, host string, t dnsmessage.Type) ([]net.IP, error) {
	msg := dnsmessage.Message{
		Header: dnsmessage.Header{RecursionDesired: true},
		Questions: []dnsmessage.Question{
			{Name: dnsmessage.MustNewName(host + "."), Type: t, Class: dnsmessage.ClassINET},
		},
	}
	wire, err := msg.Pack()
	if err != nil {
		return nil, err
	}

	d := net.Dialer{Timeout: 5 * time.Second}
	conn, err := d.DialContext(ctx, "udp", u.addr())
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	deadline := time.Now().Add(5 * time.Second)
	_ = conn.SetDeadline(deadline)

	if _, err := conn.Write(wire); err != nil {
		return nil, err
	}
	buf := make([]byte, 1500)
	n, err := conn.Read(buf)
	if err != nil {
		return nil, err
	}
	var resp dnsmessage.Message
	if err := resp.Unpack(buf[:n]); err != nil {
		return nil, err
	}
	if resp.Header.RCode != dnsmessage.RCodeSuccess {
		return nil, fmt.Errorf("dns rcode %v for %q", resp.Header.RCode, host)
	}
	var ips []net.IP
	for _, rr := range resp.Answers {
		switch rr.Header.Type {
		case dnsmessage.TypeA:
			if a, ok := rr.Body.(*dnsmessage.AResource); ok {
				ips = append(ips, net.IP(a.A[:]))
			}
		case dnsmessage.TypeAAAA:
			if a, ok := rr.Body.(*dnsmessage.AAAAResource); ok {
				ips = append(ips, net.IP(a.AAAA[:]))
			}
		}
	}
	return ips, nil
}

// RTT implements Provider.
func (u UDP) RTT(ctx context.Context, host string) (time.Duration, error) {
	start := time.Now()
	if _, err := u.Resolve(ctx, host); err != nil {
		return 0, err
	}
	return time.Since(start), nil
}

// --- DoT provider -------------------------------------------------------

// DoT resolves via DNS-over-TLS (port 853).
type DoT struct {
	Addr string // host:port, default port 853
}

// Name implements Provider.
func (d DoT) Name() string { return "dot:" + d.Addr }

func (d DoT) addr() string {
	if strings.Contains(d.Addr, ":") {
		return d.Addr
	}
	return net.JoinHostPort(d.Addr, "853")
}

// Resolve implements Provider.
func (d DoT) Resolve(ctx context.Context, host string) ([]net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		return []net.IP{ip}, nil
	}
	a, aErr := d.queryType(ctx, host, dnsmessage.TypeA)
	aaaa, aaaaErr := d.queryType(ctx, host, dnsmessage.TypeAAAA)
	if aErr != nil && aaaaErr != nil {
		return nil, aErr
	}
	ips := append(a, aaaa...)
	if len(ips) == 0 {
		return nil, fmt.Errorf("no addresses for %q via %s", host, d.addr())
	}
	return ips, nil
}

func (d DoT) queryType(ctx context.Context, host string, t dnsmessage.Type) ([]net.IP, error) {
	msg := dnsmessage.Message{
		Header: dnsmessage.Header{RecursionDesired: true},
		Questions: []dnsmessage.Question{
			{Name: dnsmessage.MustNewName(host + "."), Type: t, Class: dnsmessage.ClassINET},
		},
	}
	wire, err := msg.Pack()
	if err != nil {
		return nil, err
	}
	// DNS-over-TLS framing: 2-byte big-endian length prefix.
	frame := make([]byte, 2+len(wire))
	frame[0] = byte(len(wire) >> 8)
	frame[1] = byte(len(wire))
	copy(frame[2:], wire)

	dialer := net.Dialer{Timeout: 5 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", d.addr())
	if err != nil {
		return nil, err
	}
	tlsConn := net.Conn(conn)
	_ = tlsConn
	// Wrap with TLS.
	tlsDialer := &TLSDialer{Addr: d.addr()}
	tconn, err := tlsDialer.dial(ctx, conn)
	if err != nil {
		conn.Close()
		return nil, err
	}
	defer tconn.Close()
	_ = tconn.SetDeadline(time.Now().Add(5 * time.Second))

	if _, err := tconn.Write(frame); err != nil {
		return nil, err
	}
	lenBuf := make([]byte, 2)
	if _, err := readFull(tconn, lenBuf); err != nil {
		return nil, err
	}
	respLen := int(lenBuf[0])<<8 | int(lenBuf[1])
	respBuf := make([]byte, respLen)
	if _, err := readFull(tconn, respBuf); err != nil {
		return nil, err
	}
	var resp dnsmessage.Message
	if err := resp.Unpack(respBuf); err != nil {
		return nil, err
	}
	if resp.Header.RCode != dnsmessage.RCodeSuccess {
		return nil, fmt.Errorf("dns rcode %v for %q", resp.Header.RCode, host)
	}
	var ips []net.IP
	for _, rr := range resp.Answers {
		switch rr.Header.Type {
		case dnsmessage.TypeA:
			if a, ok := rr.Body.(*dnsmessage.AResource); ok {
				ips = append(ips, net.IP(a.A[:]))
			}
		case dnsmessage.TypeAAAA:
			if a, ok := rr.Body.(*dnsmessage.AAAAResource); ok {
				ips = append(ips, net.IP(a.AAAA[:]))
			}
		}
	}
	return ips, nil
}

// RTT implements Provider.
func (d DoT) RTT(ctx context.Context, host string) (time.Duration, error) {
	start := time.Now()
	if _, err := d.Resolve(ctx, host); err != nil {
		return 0, err
	}
	return time.Since(start), nil
}

// --- DoH provider -------------------------------------------------------

// DoH resolves via DNS-over-HTTPS (RFC 8484), e.g.
// https://cloudflare-dns.com/dns-query.
type DoH struct {
	// URL is the DoH endpoint accepting GET ?dns=<base64url wireformat>.
	URL string
	// HTTPClient for the DoH requests; default httpx.ClientOr.
	HTTPClient *http.Client
}

// Name implements Provider.
func (d DoH) Name() string { return "doh:" + d.URL }

// Resolve implements Provider.
func (d DoH) Resolve(ctx context.Context, host string) ([]net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		return []net.IP{ip}, nil
	}
	client := httpx.ClientOr(d.HTTPClient)

	a, aErr := d.queryType(ctx, client, host, dnsmessage.TypeA)
	aaaa, aaaaErr := d.queryType(ctx, client, host, dnsmessage.TypeAAAA)
	if aErr != nil && aaaaErr != nil {
		return nil, aErr
	}
	ips := append(a, aaaa...)
	if len(ips) == 0 {
		return nil, fmt.Errorf("no addresses for %q via %s", host, d.URL)
	}
	return ips, nil
}

func (d DoH) queryType(ctx context.Context, client *http.Client, host string, t dnsmessage.Type) ([]net.IP, error) {
	msg := dnsmessage.Message{
		Header: dnsmessage.Header{RecursionDesired: true},
		Questions: []dnsmessage.Question{
			{Name: dnsmessage.MustNewName(host + "."), Type: t, Class: dnsmessage.ClassINET},
		},
	}
	wire, err := msg.Pack()
	if err != nil {
		return nil, err
	}
	u := d.URL + "?dns=" + base64URL(wire)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/dns-message")
	req.Header.Set("User-Agent", httpx.UserAgent())
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("doh status %d from %s", resp.StatusCode, d.URL)
	}
	body := make([]byte, 0, 4096)
	buf := make([]byte, 4096)
	for {
		n, rerr := resp.Body.Read(buf)
		body = append(body, buf[:n]...)
		if rerr != nil {
			break
		}
		if len(body) > 1<<20 {
			return nil, fmt.Errorf("doh response too large")
		}
	}
	var m dnsmessage.Message
	if err := m.Unpack(body); err != nil {
		return nil, err
	}
	if m.Header.RCode != dnsmessage.RCodeSuccess {
		return nil, fmt.Errorf("dns rcode %v for %q", m.Header.RCode, host)
	}
	var ips []net.IP
	for _, rr := range m.Answers {
		switch rr.Header.Type {
		case dnsmessage.TypeA:
			if a, ok := rr.Body.(*dnsmessage.AResource); ok {
				ips = append(ips, net.IP(a.A[:]))
			}
		case dnsmessage.TypeAAAA:
			if a, ok := rr.Body.(*dnsmessage.AAAAResource); ok {
				ips = append(ips, net.IP(a.AAAA[:]))
			}
		}
	}
	return ips, nil
}

// RTT implements Provider.
func (d DoH) RTT(ctx context.Context, host string) (time.Duration, error) {
	start := time.Now()
	if _, err := d.Resolve(ctx, host); err != nil {
		return 0, err
	}
	return time.Since(start), nil
}

// --- System provider ----------------------------------------------------

// System resolves via the OS resolver (net.Resolver default).
type System struct{}

// Name implements Provider.
func (System) Name() string { return "system" }

// Resolve implements Provider.
func (System) Resolve(ctx context.Context, host string) ([]net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		return []net.IP{ip}, nil
	}
	addrs, err := net.DefaultResolver.LookupHost(ctx, host)
	if err != nil {
		return nil, err
	}
	ips := make([]net.IP, 0, len(addrs))
	for _, a := range addrs {
		if ip := net.ParseIP(a); ip != nil {
			ips = append(ips, ip)
		}
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("no addresses for %q via system resolver", host)
	}
	return ips, nil
}

// RTT implements Provider.
func (System) RTT(ctx context.Context, host string) (time.Duration, error) {
	start := time.Now()
	if _, err := (System{}).Resolve(ctx, host); err != nil {
		return 0, err
	}
	return time.Since(start), nil
}

// --- Presets ------------------------------------------------------------

// Well-known public resolvers. Each entry lists its plain-UDP and
// encrypted endpoints; pick per Provider as needed.
var (
	// CloudflareUDP is 1.1.1.1 over UDP.
	CloudflareUDP = UDP{Addr: "1.1.1.1"}
	// CloudflareDoH is the 1.1.1.1 DNS-over-HTTPS endpoint.
	CloudflareDoH = DoH{URL: "https://cloudflare-dns.com/dns-query"}
	// CloudflareDoT is 1.1.1.1 over TLS port 853.
	CloudflareDoT = DoT{Addr: "1.1.1.1"}
	// GoogleUDP is 8.8.8.8 over UDP.
	GoogleUDP = UDP{Addr: "8.8.8.8"}
	// GoogleDoH is the dns.google DNS-over-HTTPS endpoint.
	GoogleDoH = DoH{URL: "https://dns.google/dns-query"}
	// Quad9UDP is 9.9.9.9 over UDP.
	Quad9UDP = UDP{Addr: "9.9.9.9"}
	// Quad9DoH is the quad9 DoH endpoint.
	Quad9DoH = DoH{URL: "https://dns.quad9.net/dns-query"}
	// OpenDNSUDP is 208.67.222.222 over UDP.
	OpenDNSUDP = UDP{Addr: "208.67.222.222"}
	// AdGuardUDP is 94.140.14.14 over UDP.
	AdGuardUDP = UDP{Addr: "94.140.14.14"}
	// AdGuardDoH is the AdGuard DoH endpoint.
	AdGuardDoH = DoH{URL: "https://dns.adguard-dns.com/dns-query"}

	// DefaultProviders is the preset set used when none are specified.
	DefaultProviders = []Provider{
		System{},
		CloudflareUDP,
		GoogleUDP,
		Quad9UDP,
	}
)

// --- Probe result types --------------------------------------------------

// ProviderResult reports one provider's view of a mirror host.
type ProviderResult struct {
	Provider   string   `json:"provider"`
	IPs        []string `json:"ips"`
	ResolveRTT Duration `json:"resolve_rtt"`
	ConnectRTT Duration `json:"connect_rtt,omitempty"`
	Reachable  bool     `json:"reachable"`
	Error      string   `json:"error,omitempty"`
	IPTag      string   `json:"ip_tag,omitempty"` // diverge / ok
}

// Duration marshals as a Go duration string.
type Duration time.Duration

func (d Duration) String() string { return time.Duration(d).String() }

func (d Duration) MarshalJSON() ([]byte, error) {
	return []byte(`"` + time.Duration(d).String() + `"`), nil
}

// ProbeResult aggregates all providers for one mirror host.
type ProbeResult struct {
	Host      string           `json:"host"`
	Port      string           `json:"port"`
	Providers []ProviderResult `json:"providers"`
	// Divergence describes providers whose IP sets differ from the
	// majority — a poisoning / geo-DNS / filtering signal.
	Divergence string `json:"divergence,omitempty"`
}

// Probe resolves host via each provider and then dials mirrorPort through
// each provider's resolved IPs (Host header semantics are left to callers;
// this measures TCP reachability only).
func Probe(ctx context.Context, host string, providers []Provider, port string) (*ProbeResult, error) {
	if port == "" {
		port = "443"
	}
	if len(providers) == 0 {
		providers = DefaultProviders
	}
	res := &ProbeResult{Host: host, Port: port}

	type answer struct {
		idx int
		ips []net.IP
		rtt time.Duration
		err error
	}
	answers := make([]answer, len(providers))
	var wg = &syncWaitGroup{}
	for i, p := range providers {
		wg.Add(1)
		go func(i int, p Provider) {
			defer wg.Done()
			start := time.Now()
			ips, err := p.Resolve(ctx, host)
			answers[i] = answer{idx: i, ips: ips, rtt: time.Since(start), err: err}
		}(i, p)
	}
	wg.Wait()

	for i, a := range answers {
		pr := ProviderResult{Provider: providers[i].Name(), ResolveRTT: Duration(a.rtt)}
		if a.err != nil {
			pr.Error = a.err.Error()
			res.Providers = append(res.Providers, pr)
			continue
		}
		for _, ip := range a.ips {
			pr.IPs = append(pr.IPs, ip.String())
		}
		sort.Strings(pr.IPs)

		// TCP connectivity to the first IP.
		if len(a.ips) > 0 {
			start := time.Now()
			d := net.Dialer{Timeout: 5 * time.Second}
			conn, derr := d.DialContext(ctx, "tcp", net.JoinHostPort(a.ips[0].String(), port))
			if derr == nil {
				_ = conn.Close()
				pr.Reachable = true
				pr.ConnectRTT = Duration(time.Since(start))
			} else {
				pr.Error = derr.Error()
			}
		}
		res.Providers = append(res.Providers, pr)
	}

	res.Divergence = summarizeDivergence(res.Providers)
	return res, nil
}

// summarizeDivergence compares IP sets and flags disagreement.
func summarizeDivergence(ps []ProviderResult) string {
	var sets []struct {
		name string
		set  map[string]bool
	}
	for _, p := range ps {
		if p.Error != "" || len(p.IPs) == 0 {
			continue
		}
		set := make(map[string]bool, len(p.IPs))
		for _, ip := range p.IPs {
			set[ip] = true
		}
		sets = append(sets, struct {
			name string
			set  map[string]bool
		}{p.Provider, set})
	}
	if len(sets) < 2 {
		return ""
	}
	base := sets[0]
	for _, s := range sets[1:] {
		if !sameSet(base.set, s.set) {
			return fmt.Sprintf("%s and %s resolve %s to different IPs", base.name, s.name, "the host")
		}
	}
	return ""
}

func sameSet(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

// --- small helpers -------------------------------------------------------

func base64URL(b []byte) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	var sb strings.Builder
	for i := 0; i < len(b); i += 3 {
		var chunk [3]byte
		copy(chunk[:], b[i:])
		n := int(chunk[0])<<16 | int(chunk[1])<<8 | int(chunk[2])
		sb.WriteByte(alphabet[(n>>18)&0x3f])
		if i+1 < len(b) {
			sb.WriteByte(alphabet[(n>>12)&0x3f])
		} else {
			break
		}
		if i+2 < len(b) {
			sb.WriteByte(alphabet[(n>>6)&0x3f])
			sb.WriteByte(alphabet[n&0x3f])
		} else {
			sb.WriteByte(alphabet[(n>>6)&0x3f])
		}
	}
	return sb.String()
}

func readFull(c net.Conn, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := c.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

type TLSDialer struct{ Addr string }

func (t *TLSDialer) dial(ctx context.Context, raw net.Conn) (net.Conn, error) {
	host, _, err := net.SplitHostPort(t.Addr)
	if err != nil {
		host = t.Addr
	}
	// Use crypto/tls via the imported helper to avoid a new top import.
	return tlsHandshake(ctx, raw, host)
}

func parsePort(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

var _ = parsePort
