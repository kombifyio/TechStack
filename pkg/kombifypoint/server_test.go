package kombifypoint

import (
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/stretchr/testify/require"
)

func testServer(t *testing.T, zones []string) *Server {
	t.Helper()
	server, err := NewServer(Config{
		Zones:     zones,
		TargetIP:  netip.MustParseAddr("192.168.56.10"),
		DNSAddr:   "127.0.0.1:0",
		HTTPAddr:  "127.0.0.1:0",
		Upstreams: []string{"127.0.0.1:53535"},
		AllowedCIDRs: []netip.Prefix{
			netip.MustParsePrefix("192.168.0.0/16"),
			netip.MustParsePrefix("127.0.0.0/8"),
		},
		Timeout: 200 * time.Millisecond,
	})
	require.NoError(t, err)
	return server
}

func TestLocalHomeWildcardResolvesToLANIP(t *testing.T) {
	server := testServer(t, []string{"home"})
	req := new(dns.Msg)
	req.SetQuestion("base.home.", dns.TypeA)

	resp := server.Resolve(req, netip.MustParseAddr("192.168.56.20"))

	require.Equal(t, dns.RcodeSuccess, resp.Rcode)
	require.Len(t, resp.Answer, 1)
	record, ok := resp.Answer[0].(*dns.A)
	require.True(t, ok)
	require.Equal(t, net.IPv4(192, 168, 56, 10), record.A)
}

func TestNamedHomeWildcardResolvesToLANIP(t *testing.T) {
	server := testServer(t, []string{"family.home", "home"})
	req := new(dns.Msg)
	req.SetQuestion("vault.family.home.", dns.TypeA)

	resp := server.Resolve(req, netip.MustParseAddr("192.168.56.20"))

	require.Equal(t, dns.RcodeSuccess, resp.Rcode)
	require.Len(t, resp.Answer, 1)
	record, ok := resp.Answer[0].(*dns.A)
	require.True(t, ok)
	require.Equal(t, "vault.family.home.", record.Hdr.Name)
}

func TestLocalZonesAreNotForwarded(t *testing.T) {
	server := testServer(t, []string{"home"})
	req := new(dns.Msg)
	req.SetQuestion("base.home.", dns.TypeTXT)

	resp := server.Resolve(req, netip.MustParseAddr("192.168.56.20"))

	require.Equal(t, dns.RcodeSuccess, resp.Rcode)
	require.Empty(t, resp.Answer)
}

func TestExternalDomainsAreForwarded(t *testing.T) {
	upstreamAddr, shutdown := startFakeUpstream(t)
	defer shutdown()

	server, err := NewServer(Config{
		Zones:        []string{"home"},
		TargetIP:     netip.MustParseAddr("192.168.56.10"),
		Upstreams:    []string{upstreamAddr},
		AllowedCIDRs: []netip.Prefix{netip.MustParsePrefix("192.168.0.0/16")},
		Timeout:      time.Second,
	})
	require.NoError(t, err)

	req := new(dns.Msg)
	req.SetQuestion("example.com.", dns.TypeA)

	resp := server.Resolve(req, netip.MustParseAddr("192.168.56.20"))

	require.Equal(t, dns.RcodeSuccess, resp.Rcode)
	require.Len(t, resp.Answer, 1)
	record, ok := resp.Answer[0].(*dns.A)
	require.True(t, ok)
	require.Equal(t, "93.184.216.34", record.A.String())
}

func TestRejectsPublicTargetIP(t *testing.T) {
	_, err := NewServer(Config{
		Zones:     []string{"home"},
		TargetIP:  netip.MustParseAddr("8.8.8.8"),
		Upstreams: []string{"1.1.1.1:53"},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "private LAN")
}

func TestRejectsArpaZone(t *testing.T) {
	_, err := NewServer(Config{
		Zones:     []string{"home.arpa"},
		TargetIP:  netip.MustParseAddr("192.168.56.10"),
		Upstreams: []string{"1.1.1.1:53"},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), ".arpa")
}

func startFakeUpstream(t *testing.T) (string, func()) {
	t.Helper()
	mux := dns.NewServeMux()
	mux.HandleFunc(".", func(w dns.ResponseWriter, req *dns.Msg) {
		resp := new(dns.Msg)
		resp.SetReply(req)
		resp.Answer = append(resp.Answer, &dns.A{
			Hdr: dns.RR_Header{
				Name:   req.Question[0].Name,
				Rrtype: dns.TypeA,
				Class:  dns.ClassINET,
				Ttl:    30,
			},
			A: net.IPv4(93, 184, 216, 34),
		})
		_ = w.WriteMsg(resp)
	})

	packetConn, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	server := &dns.Server{PacketConn: packetConn, Handler: mux}
	go func() {
		_ = server.ActivateAndServe()
	}()

	return packetConn.LocalAddr().String(), func() {
		_ = server.Shutdown()
	}
}
