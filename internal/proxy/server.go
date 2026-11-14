// Package proxy is the allowlisting egress proxy run inside each sandbox.
package proxy

import (
	"context"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Crocogiciel-Studio/podbox/internal/policy"
)

// Resolver resolves host names; *net.Resolver satisfies it.
type Resolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

// DialFunc opens a TCP connection.
type DialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// Config configures a Server.
type Config struct {
	PolicyPath   string
	Upstream     *url.URL       // optional parent proxy
	UpstreamCA   *x509.CertPool // trust for an https:// upstream
	AllowAll     bool           // ignore the policy (test upstreams only)
	Auth         string         // "user:pass" required from clients when set
	Log          io.Writer
	Resolver     Resolver
	Dial         DialFunc
	PollInterval time.Duration
}

// Server is an HTTP proxy handling CONNECT and absolute-URI requests.
type Server struct {
	cfg       Config
	policy    atomic.Pointer[policy.Effective]
	logMu     sync.Mutex
	direct    *http.Transport
	upstreamT *http.Transport
}

type dialKey struct{}

// New builds a Server and loads the policy once; call Watch to hot-reload.
func New(cfg Config) *Server {
	if cfg.Resolver == nil {
		cfg.Resolver = net.DefaultResolver
	}
	if cfg.Dial == nil {
		cfg.Dial = (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	}
	if cfg.Log == nil {
		cfg.Log = io.Discard
	}
	if cfg.PollInterval == 0 {
		cfg.PollInterval = time.Second
	}
	s := &Server{cfg: cfg}
	s.direct = &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			addr, _ := ctx.Value(dialKey{}).(string)
			return cfg.Dial(ctx, network, addr)
		},
		ForceAttemptHTTP2:   false,
		MaxIdleConnsPerHost: 4,
		IdleConnTimeout:     60 * time.Second,
	}
	if cfg.Upstream != nil {
		s.upstreamT = &http.Transport{
			Proxy:       http.ProxyURL(cfg.Upstream),
			DialContext: cfg.Dial,
		}
		if cfg.UpstreamCA != nil {
			s.upstreamT.TLSClientConfig = tlsConfig(cfg.UpstreamCA)
		}
	}
	if !cfg.AllowAll {
		s.load()
	}
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Auth != "" && !s.authorized(r) {
		w.Header().Set("Proxy-Authenticate", `Basic realm="podbox"`)
		http.Error(w, "proxy authentication required", http.StatusProxyAuthRequired)
		return
	}
	if r.Method == http.MethodConnect {
		s.handleConnect(w, r)
		return
	}
	if !r.URL.IsAbs() || r.URL.Host == "" {
		http.Error(w, "podbox proxy: send absolute-URI or CONNECT requests", http.StatusBadRequest)
		return
	}
	s.handleForward(w, r)
}

func (s *Server) authorized(r *http.Request) bool {
	v, ok := strings.CutPrefix(r.Header.Get("Proxy-Authorization"), "Basic ")
	if !ok {
		return false
	}
	got, err := base64.StdEncoding.DecodeString(v)
	return err == nil && subtle.ConstantTimeCompare(got, []byte(s.cfg.Auth)) == 1
}

// decision is what the logs record for one request.
type decision struct {
	Time     string `json:"time"`
	Sandbox  string `json:"sandbox,omitempty"`
	Method   string `json:"method"`
	Host     string `json:"host"`
	Port     string `json:"port"`
	Decision string `json:"decision"`
	Reason   string `json:"reason,omitempty"`
	Rule     string `json:"rule,omitempty"`
	Layer    string `json:"layer,omitempty"`
	IP       string `json:"ip,omitempty"`
	Via      string `json:"via,omitempty"`
}

func (s *Server) logDecision(d decision) {
	d.Time = time.Now().UTC().Format(time.RFC3339Nano)
	if p := s.policy.Load(); p != nil {
		d.Sandbox = p.Sandbox
	}
	s.logMu.Lock()
	defer s.logMu.Unlock()
	_ = json.NewEncoder(s.cfg.Log).Encode(d)
}

func (s *Server) logEvent(msg string, err error) {
	ev := map[string]string{"time": time.Now().UTC().Format(time.RFC3339Nano), "event": msg}
	if err != nil {
		ev["error"] = err.Error()
	}
	s.logMu.Lock()
	defer s.logMu.Unlock()
	_ = json.NewEncoder(s.cfg.Log).Encode(ev)
}

// route is the outcome of policy and destination checks for one target.
type route struct {
	host, port string
	addr       string // resolved ip:port to dial directly
	byName     bool   // forward host:port to the upstream unresolved
	ip         string
}

// authorize applies the policy and the destination guard. On refusal it
// writes the response and returns false.
func (s *Server) authorize(ctx context.Context, w http.ResponseWriter, method, host, port string) (route, bool) {
	host = policy.NormalizeHost(host)
	rt := route{host: host, port: port}
	d := decision{Method: method, Host: host, Port: port, Decision: "deny"}
	if !s.cfg.AllowAll {
		pd := s.policy.Load().Decide(host)
		d.Rule, d.Layer = pd.Reason()
		if !pd.Allow {
			d.Reason = "policy"
			s.logDecision(d)
			writeNotAllowed(w, s.sandboxName(), host)
			return rt, false
		}
		var err error
		rt, err = s.resolve(ctx, rt, pd.AllowPrivate)
		if err != nil {
			var be *blockedError
			if errors.As(err, &be) {
				d.Reason, d.IP = "blocked_destination", be.ip.String()
				s.logDecision(d)
				writeBlockedDestination(w, host, be)
				return rt, false
			}
			d.Reason = "resolve_error"
			s.logDecision(d)
			writeError(w, http.StatusBadGateway, "podbox proxy: cannot resolve "+host+": "+err.Error())
			return rt, false
		}
	} else {
		rt.byName = s.cfg.Upstream != nil
		if !rt.byName {
			var err error
			if rt, err = s.resolve(ctx, rt, true); err != nil {
				d.Reason = "resolve_error"
				s.logDecision(d)
				writeError(w, http.StatusBadGateway, "podbox proxy: "+err.Error())
				return rt, false
			}
		}
	}
	d.Decision, d.IP = "allow", rt.ip
	if s.cfg.Upstream != nil {
		d.Via = "upstream"
	}
	s.logDecision(d)
	return rt, true
}

func (s *Server) sandboxName() string {
	if p := s.policy.Load(); p != nil && p.Sandbox != "" {
		return p.Sandbox
	}
	return "<sandbox>"
}

func splitTarget(hostport, defPort string) (string, string) {
	if h, p, err := net.SplitHostPort(hostport); err == nil {
		return h, p
	}
	return strings.TrimSuffix(strings.TrimPrefix(hostport, "["), "]"), defPort
}
