package proxy

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Crocogiciel-Studio/podbox/internal/policy"
)

type fakeResolver map[string]string

func (f fakeResolver) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	ip, ok := f[host]
	if !ok {
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	return []net.IPAddr{{IP: net.ParseIP(ip)}}, nil
}

var resolver = fakeResolver{
	"allowed.test": "203.0.113.10",
	"denied.test":  "203.0.113.11",
	"loop.test":    "127.0.0.1",
	"meta.test":    "169.254.169.254",
	"private.test": "10.1.2.3",
	"privok.test":  "10.1.2.4",
}

type env struct {
	t        *testing.T
	policy   string
	proxy    *httptest.Server
	srv      *Server
	logs     *syncBuffer
	tlsAddr  string
	httpAddr string
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func rules(hosts ...string) []policy.Rule {
	var rs []policy.Rule
	for _, h := range hosts {
		r := policy.Rule{Host: h, Allow: true, Layer: policy.LayerSession}
		if strings.HasPrefix(h, "+") {
			r.Host, r.AllowPrivate = h[1:], true
		}
		rs = append(rs, r)
	}
	return rs
}

func writePolicy(t *testing.T, path string, rs []policy.Rule) {
	t.Helper()
	if err := policy.WriteEffective(path, policy.Effective{Version: policy.EffectiveVersion, Sandbox: "t", Rules: rs}); err != nil {
		t.Fatal(err)
	}
}

// mapDial sends test IPs to local servers and everything else to the real address.
func mapDial(m map[string]string) DialFunc {
	d := &net.Dialer{Timeout: 2 * time.Second}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		if to, ok := m[addr]; ok {
			addr = to
		}
		return d.DialContext(ctx, network, addr)
	}
}

func setup(t *testing.T, mutate func(*Config)) *env {
	t.Helper()
	tlsOrigin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "tls-ok") }))
	httpOrigin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "http-ok %s", r.Header.Get("Proxy-Authorization"))
	}))
	t.Cleanup(tlsOrigin.Close)
	t.Cleanup(httpOrigin.Close)
	e := &env{t: t, logs: &syncBuffer{}, tlsAddr: tlsOrigin.Listener.Addr().String(), httpAddr: httpOrigin.Listener.Addr().String()}
	e.policy = filepath.Join(t.TempDir(), "policy.effective.json")
	writePolicy(t, e.policy, rules("allowed.test", "loop.test", "meta.test", "private.test", "+privok.test", "localhost", "127.0.0.1"))
	cfg := Config{
		PolicyPath: e.policy, Log: e.logs, Resolver: resolver, PollInterval: 50 * time.Millisecond,
		Dial: mapDial(map[string]string{
			"203.0.113.10:443": e.tlsAddr, "203.0.113.10:80": e.httpAddr, "10.1.2.4:80": e.httpAddr,
		}),
	}
	if mutate != nil {
		mutate(&cfg)
	}
	e.srv = New(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go e.srv.Watch(ctx)
	e.proxy = httptest.NewServer(e.srv)
	t.Cleanup(e.proxy.Close)
	return e
}

func (e *env) client() *http.Client {
	u, _ := url.Parse(e.proxy.URL)
	return &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		Proxy:           http.ProxyURL(u),
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}}
}

func (e *env) get(rawURL string) (int, string, error) {
	resp, err := e.client().Get(rawURL)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), nil
}

// connect sends a raw CONNECT and returns the proxy's answer.
func (e *env) connect(target string) (*http.Response, string) {
	e.t.Helper()
	c, err := net.Dial("tcp", e.proxy.Listener.Addr().String())
	if err != nil {
		e.t.Fatal(err)
	}
	defer c.Close()
	fmt.Fprintf(c, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
	resp, err := http.ReadResponse(bufio.NewReader(c), &http.Request{Method: http.MethodConnect})
	if err != nil {
		e.t.Fatal(err)
	}
	if resp.StatusCode == http.StatusOK {
		return resp, ""
	}
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func TestTunnelAndForward(t *testing.T) {
	e := setup(t, nil)
	if code, body, err := e.get("https://allowed.test/"); err != nil || code != 200 || body != "tls-ok" {
		t.Fatalf("CONNECT: %d %q %v", code, body, err)
	}
	if code, body, err := e.get("http://allowed.test/"); err != nil || code != 200 || !strings.HasPrefix(body, "http-ok") {
		t.Fatalf("forward: %d %q %v", code, body, err)
	}
	if !strings.Contains(e.logs.String(), `"host":"allowed.test","port":"443","decision":"allow"`) {
		t.Fatalf("decision log: %s", e.logs.String())
	}
}

func TestNotAllowed(t *testing.T) {
	e := setup(t, nil)
	resp, body := e.connect("denied.test:443")
	want := "Blocked by podbox network policy: host \"denied.test\" is not allowed.\nAsk the user to run: podbox policy allow t denied.test\n"
	if resp.StatusCode != 403 || resp.Header.Get(BlockedHeader) != "1" || body != want {
		t.Fatalf("%d %q %q", resp.StatusCode, resp.Header, body)
	}
	if code, body, _ := e.get("http://denied.test/x"); code != 403 || body != want {
		t.Fatalf("http: %d %q", code, body)
	}
	if !strings.Contains(e.logs.String(), `"decision":"deny","reason":"policy","rule":"default"`) {
		t.Fatal(e.logs.String())
	}
}

func TestPortsDefaultToWeb(t *testing.T) {
	e := setup(t, nil)
	resp, body := e.connect("allowed.test:22")
	want := "Blocked by podbox network policy: host \"allowed.test\" is not allowed on port 22.\nAsk the user to run: podbox policy allow t allowed.test --port 22\n"
	if resp.StatusCode != 403 || body != want {
		t.Fatalf("%d %q", resp.StatusCode, body)
	}
	if !strings.Contains(e.logs.String(), `"host":"allowed.test","port":"22","decision":"deny","reason":"policy"`) {
		t.Fatal(e.logs.String())
	}
	rs := rules("allowed.test")
	rs[0].Ports = []int{22}
	writePolicy(t, e.policy, rs)
	eventually(t, func() bool { r, _ := e.connect("allowed.test:443"); return r.StatusCode == 403 })
	if r, _ := e.connect("allowed.test:22"); r.StatusCode == 403 {
		t.Fatal("ports: [22] not honoured")
	}
}

func TestDestinationGuard(t *testing.T) {
	e := setup(t, nil)
	for _, target := range []string{"loop.test:443", "meta.test:80", "private.test:443", "localhost:443", "127.0.0.1:80"} {
		resp, body := e.connect(target)
		if resp.StatusCode != 403 || !strings.Contains(body, "blocked destination") {
			t.Errorf("%s: %d %q", target, resp.StatusCode, body)
		}
	}
	if code, body, err := e.get("http://privok.test/"); err != nil || code != 200 {
		t.Fatalf("allow_private: %d %q %v", code, body, err)
	}
}

func TestCheckIP(t *testing.T) {
	cases := map[string]bool{ // blocked without allow_private
		"127.0.0.2": true, "::1": true, "169.254.169.254": true, "fd00:ec2::254": true, "fe80::1": true,
		"0.0.0.0": true, "224.0.0.1": true, "10.0.0.1": true, "192.168.1.1": true, "172.16.0.1": true,
		"fc00::1": true, "100.64.0.1": true, "::ffff:127.0.0.1": true, "203.0.113.5": false, "8.8.8.8": false,
	}
	for ip, blocked := range cases {
		if got := checkIP(net.ParseIP(ip), false) != nil; got != blocked {
			t.Errorf("%s: blocked=%v", ip, got)
		}
	}
	for _, ip := range []string{"127.0.0.1", "169.254.169.254", "fd00:ec2::254"} {
		if checkIP(net.ParseIP(ip), true) == nil {
			t.Errorf("%s allowed with allow_private", ip)
		}
	}
	if checkIP(net.ParseIP("10.0.0.1"), true) != nil {
		t.Error("allow_private ignored")
	}
}

func eventually(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("condition not met within 2s")
}

func TestHotReload(t *testing.T) {
	e := setup(t, nil)
	if resp, _ := e.connect("denied.test:443"); resp.StatusCode != 403 {
		t.Fatal("precondition")
	}
	writePolicy(t, e.policy, rules("denied.test"))
	eventually(t, func() bool {
		resp, _ := e.connect("allowed.test:443")
		return resp.StatusCode == 403
	})
	// corrupt file: previous policy kept
	if err := os.WriteFile(e.policy, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return strings.Contains(e.logs.String(), "keeping previous") })
	if resp, _ := e.connect("allowed.test:443"); resp.StatusCode != 403 {
		t.Fatal("corrupt reload widened the policy")
	}
	if p := e.srv.policy.Load(); p == nil || p.Rules[0].Host != "denied.test" {
		t.Fatalf("previous policy lost: %+v", p)
	}
}

func TestCorruptAtStartDeniesAll(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "policy.effective.json")
	_ = os.WriteFile(p, []byte("garbage"), 0o644)
	e := setup(t, func(c *Config) { c.PolicyPath = p })
	if resp, _ := e.connect("allowed.test:443"); resp.StatusCode != 403 {
		t.Fatal("corrupt policy at start must deny all")
	}
}

func TestUpstreamChaining(t *testing.T) {
	upLogs := &syncBuffer{}
	var upAddr string
	{
		// Parent proxy: allow-all with basic auth, like a corporate proxy.
		e := setup(t, nil)
		up := New(Config{AllowAll: true, Auth: "u:p", Log: upLogs, Resolver: resolver,
			Dial: mapDial(map[string]string{"203.0.113.10:443": e.tlsAddr, "203.0.113.10:80": e.httpAddr})})
		ups := httptest.NewServer(up)
		t.Cleanup(ups.Close)
		upAddr = ups.Listener.Addr().String()
	}
	upURL, _ := url.Parse("http://u:p@" + upAddr)
	e := setup(t, func(c *Config) {
		c.Upstream = upURL
		c.Resolver = fakeResolver{"allowed.test": "203.0.113.10", "denied.test": "203.0.113.11", "loop.test": "127.0.0.1"}
		c.Dial = mapDial(nil) // only the upstream is reachable
	})
	if code, body, err := e.get("https://allowed.test/"); err != nil || code != 200 || body != "tls-ok" {
		t.Fatalf("chained CONNECT: %d %q %v", code, body, err)
	}
	if code, body, err := e.get("http://allowed.test/"); err != nil || code != 200 || !strings.HasPrefix(body, "http-ok") {
		t.Fatalf("chained forward: %d %q %v", code, body, err)
	}
	if !strings.Contains(upLogs.String(), `"host":"allowed.test","port":"443","decision":"allow"`) {
		t.Fatalf("upstream not used: %s", upLogs.String())
	}
	resp, body := e.connect("denied.test:443")
	if resp.StatusCode != 403 || body != NotAllowedBody("t", "denied.test", 443) {
		t.Fatalf("refusal changed behind upstream: %d %q", resp.StatusCode, body)
	}
	if resp, _ := e.connect("loop.test:443"); resp.StatusCode != 403 {
		t.Fatal("guard skipped behind upstream")
	}
	// Name unknown locally: forwarded by name, upstream resolves it.
	writePolicy(t, e.policy, rules("allowed.test", "remote-only.test"))
	eventually(t, func() bool { return strings.Contains(e.logs.String(), "policy loaded") })
	time.Sleep(100 * time.Millisecond)
	resp, _ = e.connect("remote-only.test:443")
	if resp.StatusCode != 502 || !strings.Contains(upLogs.String(), `"host":"remote-only.test"`) {
		t.Fatalf("by-name forward: %d %s", resp.StatusCode, upLogs.String())
	}
}

func TestUpstreamAuthRequired(t *testing.T) {
	up := httptest.NewServer(New(Config{AllowAll: true, Auth: "u:p", Resolver: resolver}))
	t.Cleanup(up.Close)
	upURL, _ := url.Parse("http://u:wrong@" + up.Listener.Addr().String())
	e := setup(t, func(c *Config) { c.Upstream = upURL; c.Dial = mapDial(nil) })
	resp, body := e.connect("allowed.test:443")
	if resp.StatusCode != 502 || !strings.Contains(body, "407") {
		t.Fatalf("%d %q", resp.StatusCode, body)
	}
}
