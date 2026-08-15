package kombifypoint

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"

	"github.com/miekg/dns"
)

// Server serves local wildcard DNS and forwards all non-local requests.
type Server struct {
	cfg    Config
	client *dns.Client
}

// NewServer returns a configured Kombify Point DNS server.
func NewServer(cfg Config) (*Server, error) {
	normalized, err := cfg.Validated()
	if err != nil {
		return nil, err
	}
	return &Server{
		cfg: normalized,
		client: &dns.Client{
			Net:     "udp",
			Timeout: normalized.Timeout,
		},
	}, nil
}

// Serve starts UDP DNS, TCP DNS, and the health HTTP endpoint.
func (s *Server) Serve(ctx context.Context) error {
	mux := dns.NewServeMux()
	mux.HandleFunc(".", s.serveDNS)

	udp := &dns.Server{Addr: s.cfg.DNSAddr, Net: "udp", Handler: mux}
	tcp := &dns.Server{Addr: s.cfg.DNSAddr, Net: "tcp", Handler: mux}
	health := &http.Server{Addr: s.cfg.HTTPAddr, Handler: s.healthHandler()}

	errs := make(chan error, 3)
	var wg sync.WaitGroup
	start := func(name string, fn func() error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := fn(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errs <- fmt.Errorf("%s server failed: %w", name, err)
			}
		}()
	}

	start("udp dns", udp.ListenAndServe)
	start("tcp dns", tcp.ListenAndServe)
	start("http health", health.ListenAndServe)

	log.Printf("kombify-point listening dns=%s health=%s zones=%s target=%s", s.cfg.DNSAddr, s.cfg.HTTPAddr, strings.Join(s.cfg.Zones, ","), s.cfg.TargetIP)

	select {
	case <-ctx.Done():
		_ = udp.Shutdown()
		_ = tcp.Shutdown()
		_ = health.Shutdown(context.Background())
		wg.Wait()
		return ctx.Err()
	case err := <-errs:
		_ = udp.Shutdown()
		_ = tcp.Shutdown()
		_ = health.Shutdown(context.Background())
		wg.Wait()
		return err
	}
}

// Resolve returns a DNS response for tests and for the network handlers.
func (s *Server) Resolve(req *dns.Msg, remote netip.Addr) *dns.Msg {
	resp := new(dns.Msg)
	resp.SetReply(req)
	resp.Authoritative = true
	resp.RecursionAvailable = true

	if remote.IsValid() && !s.remoteAllowed(remote) {
		resp.Rcode = dns.RcodeRefused
		return resp
	}
	if len(req.Question) == 0 {
		resp.Rcode = dns.RcodeFormatError
		return resp
	}

	q := req.Question[0]
	if s.isLocalName(q.Name) {
		s.answerLocal(resp, q)
		return resp
	}

	return s.forward(req)
}

func (s *Server) serveDNS(w dns.ResponseWriter, req *dns.Msg) {
	remote, _ := addrFromNetAddr(w.RemoteAddr())
	resp := s.Resolve(req, remote)
	if err := w.WriteMsg(resp); err != nil {
		log.Printf("write dns response: %v", err)
	}
}

func (s *Server) answerLocal(resp *dns.Msg, q dns.Question) {
	hdr := dns.RR_Header{
		Name:   q.Name,
		Class:  q.Qclass,
		Ttl:    s.cfg.TTL,
		Rrtype: q.Qtype,
	}

	switch q.Qtype {
	case dns.TypeA:
		if s.cfg.TargetIP.Is4() {
			ip4 := s.cfg.TargetIP.As4()
			resp.Answer = append(resp.Answer, &dns.A{
				Hdr: hdr,
				A:   net.IPv4(ip4[0], ip4[1], ip4[2], ip4[3]),
			})
		}
	case dns.TypeAAAA:
		if s.cfg.TargetIP.Is6() {
			resp.Answer = append(resp.Answer, &dns.AAAA{
				Hdr:  hdr,
				AAAA: net.IP(s.cfg.TargetIP.AsSlice()),
			})
		}
	case dns.TypeSOA:
		zone := dns.Fqdn(s.matchedZone(q.Name))
		resp.Answer = append(resp.Answer, &dns.SOA{
			Hdr:     hdr,
			Ns:      "ns." + zone,
			Mbox:    "hostmaster." + zone,
			Serial:  1,
			Refresh: 3600,
			Retry:   600,
			Expire:  86400,
			Minttl:  s.cfg.TTL,
		})
	}
}

func (s *Server) forward(req *dns.Msg) *dns.Msg {
	for _, upstream := range s.cfg.Upstreams {
		network := "udp"
		if req.Len() > 1200 {
			network = "tcp"
		}
		client := *s.client
		client.Net = network
		resp, _, err := client.Exchange(req.Copy(), upstream)
		if err == nil && resp != nil {
			return resp
		}
	}

	resp := new(dns.Msg)
	resp.SetReply(req)
	resp.Rcode = dns.RcodeServerFailure
	return resp
}

func (s *Server) isLocalName(name string) bool {
	return s.matchedZone(name) != ""
}

func (s *Server) matchedZone(name string) string {
	name = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(name)), ".")
	for _, zone := range s.cfg.Zones {
		if name == zone || strings.HasSuffix(name, "."+zone) {
			return zone
		}
	}
	return ""
}

func (s *Server) remoteAllowed(addr netip.Addr) bool {
	for _, prefix := range s.cfg.AllowedCIDRs {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

func (s *Server) healthHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":     "ok",
			"zones":      s.cfg.Zones,
			"target_ip":  s.cfg.TargetIP.String(),
			"upstreams":  len(s.cfg.Upstreams),
			"dns_addr":   s.cfg.DNSAddr,
			"http_addr":  s.cfg.HTTPAddr,
			"local_only": true,
		})
	})
	return mux
}

func addrFromNetAddr(addr net.Addr) (netip.Addr, error) {
	if addr == nil {
		return netip.Addr{}, fmt.Errorf("nil remote address")
	}
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		host = addr.String()
	}
	parsed, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, err
	}
	return parsed.Unmap(), nil
}
