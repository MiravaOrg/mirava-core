package dns

import (
	"context"
	"encoding/base64"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// fakeUDPServer answers A queries for "mirror.test" with 1.2.3.4.
func fakeUDPServer(t *testing.T, ip net.IP) *net.UDPConn {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		buf := make([]byte, 1500)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			var msg dnsmessage.Message
			if err := msg.Unpack(buf[:n]); err != nil {
				continue
			}
			resp := dnsmessage.Message{
				Header: dnsmessage.Header{
					ID:               msg.ID,
					Response:         true,
					RecursionDesired: true,
					RCode:            dnsmessage.RCodeSuccess,
				},
				Questions: msg.Questions,
			}
			if len(msg.Questions) == 1 && msg.Questions[0].Type == dnsmessage.TypeA {
				resp.Answers = []dnsmessage.Resource{{
					Header: dnsmessage.ResourceHeader{
						Name:   msg.Questions[0].Name,
						Type:   dnsmessage.TypeA,
						Class:  dnsmessage.ClassINET,
						TTL:    60,
						Length: 4,
					},
					Body: &dnsmessage.AResource{A: [4]byte{ip[0], ip[1], ip[2], ip[3]}},
				}}
			}
			packed, err := resp.Pack()
			if err != nil {
				continue
			}
			_, _ = pc.WriteTo(packed, addr)
		}
	}()
	t.Cleanup(func() { pc.Close() })
	return pc.(*net.UDPConn)
}

func TestUDPResolve(t *testing.T) {
	// The fake server answers 9.9.9.9 for everything.
	conn := fakeUDPServer(t, net.IP{9, 9, 9, 9})
	udp := UDP{Addr: conn.LocalAddr().String()}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ips, err := udp.Resolve(ctx, "mirror.test")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(ips) == 0 || ips[0].String() != "9.9.9.9" {
		t.Fatalf("ips=%v", ips)
	}

	rtt, err := udp.RTT(ctx, "mirror.test")
	if err != nil || rtt <= 0 {
		t.Fatalf("rtt=%v err=%v", rtt, err)
	}
}

func TestDoHResolve(t *testing.T) {
	conn := fakeUDPServer(t, net.IP{8, 8, 4, 4})
	// Stand up a DoH facade over the fake UDP server.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("dns")
		wire, err := base64.RawURLEncoding.DecodeString(q)
		if err != nil {
			http.Error(w, "bad dns param", http.StatusBadRequest)
			return
		}
		// Forward to the fake UDP server over its loopback socket.
		pc, err := net.Dial("udp", conn.LocalAddr().String())
		if err != nil {
			http.Error(w, "dial", http.StatusInternalServerError)
			return
		}
		defer pc.Close()
		_ = pc.SetDeadline(time.Now().Add(2 * time.Second))
		if _, err := pc.Write(wire); err != nil {
			return
		}
		buf := make([]byte, 1500)
		n, err := pc.Read(buf)
		if err != nil {
			return
		}
		w.Header().Set("Content-Type", "application/dns-message")
		_, _ = w.Write(buf[:n])
	}))
	defer srv.Close()

	doh := DoH{URL: srv.URL}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ips, err := doh.Resolve(ctx, "mirror.test")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(ips) == 0 || ips[0].String() != "8.8.4.4" {
		t.Fatalf("ips=%v", ips)
	}
}

func TestBase64URL(t *testing.T) {
	// Compare against stdlib RawURLEncoding for assorted inputs.
	for _, in := range [][]byte{[]byte("hello"), []byte("foobarbaz"), {1, 2, 3, 4, 5}} {
		want := base64.RawURLEncoding.EncodeToString(in)
		if got := base64URL(in); got != want {
			t.Errorf("base64URL(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestSameSet(t *testing.T) {
	a := map[string]bool{"1.1.1.1": true, "2.2.2.2": true}
	b := map[string]bool{"2.2.2.2": true, "1.1.1.1": true}
	c := map[string]bool{"1.1.1.1": true}
	if !sameSet(a, b) {
		t.Error("a,b should be same set")
	}
	if sameSet(a, c) {
		t.Error("a,c should differ")
	}
}

func TestSummarizeDivergence(t *testing.T) {
	ps := []ProviderResult{
		{Provider: "system", IPs: []string{"1.1.1.1"}},
		{Provider: "cloudflare", IPs: []string{"1.1.1.1"}},
	}
	if got := summarizeDivergence(ps); got != "" {
		t.Errorf("expected no divergence, got %q", got)
	}
	ps = append(ps, ProviderResult{Provider: "google", IPs: []string{"9.9.9.9"}})
	if got := summarizeDivergence(ps); !strings.Contains(got, "different IPs") {
		t.Errorf("expected divergence, got %q", got)
	}
}

func TestProbeHappyPath(t *testing.T) {
	// A TCP listener to "reach" via the fake provider's IP.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()

	port := strings.TrimSuffix(ln.Addr().String(), ":")
	_ = port
	portNum := ln.Addr().String()[strings.LastIndex(ln.Addr().String(), ":")+1:]

	// Use the real system resolver with a host that maps to loopback.
	fake := fakeProvider{ips: []net.IP{net.ParseIP("127.0.0.1")}}
	res, err := Probe(context.Background(), "mirror.test", []Provider{fake}, portNum)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Providers) != 1 || !res.Providers[0].Reachable {
		t.Fatalf("res=%+v", res)
	}
}

type fakeProvider struct{ ips []net.IP }

func (f fakeProvider) Name() string { return "fake" }
func (f fakeProvider) Resolve(_ context.Context, _ string) ([]net.IP, error) {
	return f.ips, nil
}
func (f fakeProvider) RTT(_ context.Context, _ string) (time.Duration, error) {
	return time.Millisecond, nil
}

func TestProbeDivergence(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	portNum := ln.Addr().String()[strings.LastIndex(ln.Addr().String(), ":")+1:]

	// p1 and p2 agree on 127.0.0.1; p3 resolves an extra dead IP. The
	// extra IP is not the first entry, so dialing uses 127.0.0.1 and
	// succeeds; divergence is detected because the IP sets differ.
	p1 := fakeProvider{ips: []net.IP{net.ParseIP("127.0.0.1")}}
	p2 := fakeProvider{ips: []net.IP{net.ParseIP("127.0.0.1")}}
	p3 := fakeProvider{ips: []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("10.255.255.1")}}
	res, err := Probe(context.Background(), "mirror.test", []Provider{p1, p2, p3}, portNum)
	if err != nil {
		t.Fatal(err)
	}
	if res.Divergence == "" {
		t.Fatalf("expected divergence, got %+v", res)
	}
}
