package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Crocogiciel-Studio/podbox/internal/agents"
	"github.com/Crocogiciel-Studio/podbox/internal/policy"
	"github.com/Crocogiciel-Studio/podbox/internal/termsafe"
)

// OpenerLog is where podbox-open, the sandbox's xdg-open, records the URLs
// it is asked to open. /tmp is a tmpfs of the agent container.
const OpenerLog = "/tmp/podbox-open/urls"

// DefaultLoginTimeout bounds podbox auth login.
const DefaultLoginTimeout = 10 * time.Minute

var (
	// ErrNoLogin reports an agent with no login command and no override.
	ErrNoLogin = errors.New("no login command")
	// ErrPortBusy reports a callback port another program holds on the host.
	ErrPortBusy = errors.New("callback port in use")
)

// LoginOptions tune Login; zero values are the defaults.
type LoginOptions struct {
	Command []string      // overrides the agent's login command
	Timeout time.Duration // DefaultLoginTimeout when zero
	Poll    time.Duration // how often the opener log is read; 1s when zero
	// Listen opens a host listener; net.Listen when nil.
	Listen func(network, addr string) (net.Listener, error)
}

// LoginEventKind names what happened during a login.
type LoginEventKind string

// Login events.
const (
	EventURL       LoginEventKind = "url"        // the sandbox opened a URL (shown by podbox-open)
	EventListening LoginEventKind = "listening"  // the relay listens on Port
	EventCallback  LoginEventKind = "callback"   // a callback reached the sandbox, which answered Status
	EventNeedsHost LoginEventKind = "needs_host" // Host must be allowed before the callback is relayed
	EventWarning   LoginEventKind = "warning"
)

// LoginEvent is one step of a login. It never holds a callback's query.
type LoginEvent struct {
	Kind    LoginEventKind
	Port    int
	Host    string // cleaned
	Status  int
	Message string // cleaned
}

// LoginResult is what a login did.
type LoginResult struct {
	Command   []string
	URLs      int
	Callbacks int
	Code      int // the login command's exit code
}

// Login runs the agent's login command in a running sandbox on the caller's
// terminal and relays its browser callback. Programs in the sandbox cannot
// open a browser: podbox-open shows each URL to the human and records it.
// When a recorded URL names a loopback redirect_uri, Login listens on that
// port on the host loopback for as long as the command runs, and replays
// each browser request to the same address inside the sandbox through exec,
// so no port is published and the sandbox gains no route. The redirect_uri
// is never changed: the login's own checks (state, PKCE) apply unchanged.
func (m *Manager) Login(ctx context.Context, name string, opt LoginOptions, tio IO, ev func(LoginEvent)) (LoginResult, error) {
	res := LoginResult{Code: -1}
	meta, err := m.Store.Load(name)
	if err != nil {
		return res, err
	}
	cmd := opt.Command
	if len(cmd) == 0 {
		ag, err := agents.Get(meta.Agent)
		if err != nil {
			return res, err
		}
		cmd = ag.Login
	}
	if len(cmd) == 0 {
		return res, fmt.Errorf("%w for agent %s: give one after --, as in: podbox auth login %s -- <command>", ErrNoLogin, meta.Agent, name)
	}
	res.Command = cmd
	if err := m.requireRunning(ctx, agentContainer(name), name); err != nil {
		return res, err
	}
	if _, err := m.runIn(ctx, meta, []string{"rm", "-f", OpenerLog}, nil); err != nil {
		return res, fmt.Errorf("reset %s: %w", OpenerLog, err)
	}
	timeout, poll := opt.Timeout, opt.Poll
	if timeout <= 0 {
		timeout = DefaultLoginTimeout
	}
	if poll <= 0 {
		poll = time.Second
	}
	lctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var evMu sync.Mutex
	emit := func(e LoginEvent) {
		if ev != nil {
			evMu.Lock()
			defer evMu.Unlock()
			ev(e)
		}
	}
	r := &relay{m: m, meta: meta, ctx: lctx, listen: opt.Listen, emit: emit, ports: map[int]*relayPort{}, failed: make(chan struct{})}
	if r.listen == nil {
		r.listen = net.Listen
	}
	defer r.close()

	type exit struct {
		code int
		err  error
	}
	done := make(chan exit, 1)
	go func() {
		code, err := m.Exec(lctx, name, cmd, tio)
		done <- exit{code, err}
	}()

	tick := time.NewTicker(poll)
	defer tick.Stop()
	seen, watching := 0, true
	for {
		select {
		case e := <-done:
			res.URLs, res.Callbacks = r.counts()
			if errors.Is(lctx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
				return res, fmt.Errorf("login timed out after %s; the login command may still run in the sandbox until podbox stop %s", timeout, name)
			}
			res.Code = e.code
			return res, e.err
		case <-r.failed:
			cancel()
			<-done
			res.URLs, res.Callbacks = r.counts()
			return res, r.err
		case <-tick.C:
			if !watching {
				continue
			}
			urls, next, err := m.readOpener(lctx, meta, seen)
			if err != nil {
				if lctx.Err() == nil {
					emit(LoginEvent{Kind: EventWarning, Message: "stopped reading the URLs opened in the sandbox: " + termsafe.CleanName(err.Error())})
					watching = false
				}
				continue
			}
			seen = next
			for _, u := range urls {
				r.opened(u)
			}
		}
	}
}

// readOpener returns the complete lines of the opener log after offset and
// the offset that follows them. A log made shorter is read again from the start.
func (m *Manager) readOpener(ctx context.Context, meta Meta, offset int) ([]string, int, error) {
	b, err := m.runIn(ctx, meta, []string{"sh", "-c", "cat " + OpenerLog + " 2>/dev/null; exit 0"}, nil)
	if err != nil {
		return nil, offset, err
	}
	if len(b) < offset {
		offset = 0
	}
	end := bytes.LastIndexByte(b, '\n') + 1
	if end <= offset {
		return nil, offset, nil
	}
	var out []string
	for _, l := range strings.Split(string(b[offset:end-1]), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out, end, nil
}

// relay serves the callback ports of one login.
type relay struct {
	m      *Manager
	meta   Meta
	ctx    context.Context
	listen func(network, addr string) (net.Listener, error)
	emit   func(LoginEvent)

	mu        sync.Mutex
	ports     map[int]*relayPort
	urls      int
	callbacks int

	once   sync.Once
	failed chan struct{}
	err    error
}

// relayPort is one host listener pair and the sandbox address it replays to.
type relayPort struct {
	r    *relay
	port int
	host string // redirect_uri host inside the sandbox: localhost, 127.0.0.1 or ::1
	path string // redirect_uri path; any path when empty or "/"
	srv  *http.Server
	mu   sync.Mutex // one callback at a time
}

func (r *relay) counts() (int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.urls, r.callbacks
}

func (r *relay) fail(err error) {
	r.once.Do(func() {
		r.err = err
		close(r.failed)
	})
}

func (r *relay) close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, p := range r.ports {
		_ = p.srv.Close()
	}
}

func loopbackHost(h string) bool { return h == "localhost" || h == "127.0.0.1" || h == "::1" }

// opened handles one URL the sandbox opened.
func (r *relay) opened(raw string) {
	r.mu.Lock()
	r.urls++
	r.mu.Unlock()
	r.emit(LoginEvent{Kind: EventURL, Message: termsafe.CleanName(raw)})
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" {
		r.emit(LoginEvent{Kind: EventWarning, Message: "the sandbox opened a URL that is not https; no callback is relayed for it"})
		return
	}
	ru := u.Query().Get("redirect_uri")
	if ru == "" {
		return
	}
	cb, err := url.Parse(ru)
	if err != nil || cb.Scheme != "http" || !loopbackHost(cb.Hostname()) || cb.Port() == "" {
		where := "an address"
		if err == nil {
			where = termsafe.CleanName(cb.Scheme + "://" + cb.Host)
		}
		r.emit(LoginEvent{Kind: EventWarning, Message: "the login redirects to " + where + ", which is not an http loopback address with a port: podbox cannot relay that callback"})
		return
	}
	port, err := strconv.Atoi(cb.Port())
	if err != nil || port < 1 || port > 65535 {
		r.emit(LoginEvent{Kind: EventWarning, Message: "the login's redirect_uri has an invalid port"})
		return
	}
	r.serve(port, cb.Hostname(), cb.EscapedPath())
}

// serve starts the host listeners of port unless they run. A port another
// program holds on the host fails the login: the browser would give that
// program the callback.
func (r *relay) serve(port int, host, path string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p, ok := r.ports[port]; ok {
		p.mu.Lock()
		p.host, p.path = host, path
		p.mu.Unlock()
		return
	}
	busy := func(err error) {
		r.fail(fmt.Errorf("%w: port %d is in use on this machine (%v), so the browser's callback would not reach the sandbox; find what holds it with: lsof -nP -iTCP:%d -sTCP:LISTEN", ErrPortBusy, port, err, port))
	}
	p := strconv.Itoa(port)
	v4, err := r.listen("tcp", net.JoinHostPort("127.0.0.1", p))
	if err != nil {
		busy(err)
		return
	}
	// Browsers may resolve localhost to ::1 first: listen there too, and
	// refuse to share the port with another program; no IPv6 is fine.
	v6, err := r.listen("tcp", net.JoinHostPort("::1", p))
	if err != nil && errors.Is(err, syscall.EADDRINUSE) {
		_ = v4.Close()
		busy(err)
		return
	}
	rp := &relayPort{r: r, port: port, host: host, path: path}
	rp.srv = &http.Server{Handler: rp, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = rp.srv.Serve(v4) }()
	if v6 != nil {
		go func() { _ = rp.srv.Serve(v6) }()
	}
	r.ports[port] = rp
	r.emit(LoginEvent{Kind: EventListening, Port: port})
}

func (p *relayPort) hostOK(h string) bool {
	port := strconv.Itoa(p.port)
	for _, ok := range []string{"localhost", "127.0.0.1", "[::1]"} {
		if h == ok+":"+port {
			return true
		}
	}
	return false
}

// ServeHTTP replays one browser callback into the sandbox. Only GET on the
// callback path with a loopback Host header is relayed (the Host check
// defeats DNS rebinding); the query is never logged or shown.
func (p *relayPort) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	switch {
	case req.Method != http.MethodGet:
		page(w, http.StatusMethodNotAllowed, "podbox relays only the browser's login callback.")
		return
	case !p.hostOK(req.Host):
		page(w, http.StatusForbidden, "podbox relays only requests addressed to localhost.")
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if req.URL.Path == "/favicon.ico" || p.path != "" && p.path != "/" && req.URL.EscapedPath() != p.path {
		page(w, http.StatusNotFound, "Not the login callback.")
		return
	}
	r := p.r
	if host := issuerHost(req.URL.Query()); host != "" && !r.allowed(host) {
		clean := termsafe.CleanName(host)
		r.emit(LoginEvent{Kind: EventNeedsHost, Host: clean, Port: p.port})
		page(w, http.StatusOK, fmt.Sprintf("The login needs %s, which the network policy of sandbox %s does not allow. In a terminal, run: podbox policy allow %s %s and then reload this page.",
			html.EscapeString(clean), html.EscapeString(r.meta.Name), html.EscapeString(r.meta.Name), html.EscapeString(clean)))
		return
	}
	status, loc, err := r.deliver(p.host, p.port, req.URL.RequestURI())
	if err != nil {
		r.emit(LoginEvent{Kind: EventWarning, Port: p.port, Message: "the callback did not reach the login in the sandbox: " + termsafe.CleanName(err.Error())})
		page(w, http.StatusBadGateway, "The callback did not reach the login in the sandbox; see the terminal.")
		return
	}
	r.mu.Lock()
	r.callbacks++
	r.mu.Unlock()
	r.emit(LoginEvent{Kind: EventCallback, Port: p.port, Status: status})
	if status >= 300 && status < 400 {
		if u, err := url.Parse(loc); err == nil && u.Scheme == "https" && u.Host != "" {
			h.Set("Location", u.String())
			w.WriteHeader(http.StatusFound)
			return
		}
	}
	page(w, http.StatusOK, "Login callback delivered to sandbox "+html.EscapeString(r.meta.Name)+". You can close this tab and return to the terminal.")
}

// issuerHost is the host of an https issuer_url in a callback query: the
// identity provider the login fetches next from inside the sandbox.
func issuerHost(q url.Values) string {
	u, err := url.Parse(q.Get("issuer_url"))
	if err != nil || u.Scheme != "https" {
		return ""
	}
	return policy.NormalizeHost(u.Hostname())
}

// allowed reports whether the sandbox's current policy lets it reach host.
func (r *relay) allowed(host string) bool {
	e := policy.Compile(r.m.Store.Sources(r.meta))
	return e.Decide(host, 443).Allow
}

// deliver replays requestURI to the login's listener inside the sandbox and
// returns its status and redirect. The URL goes to curl on stdin, never in
// its arguments, so no process listing shows it.
func (r *relay) deliver(host string, port int, requestURI string) (int, string, error) {
	target := "http://" + net.JoinHostPort(host, strconv.Itoa(port)) + requestURI
	cfg := `url = "` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(target) + "\"\n"
	argv := []string{"curl", "-sS", "--noproxy", "*", "--max-time", "30", "-o", "/dev/null", "-w", "%{http_code}\n%{redirect_url}", "--config", "-"}
	out := &capWriter{max: 64 << 10}
	if err := r.m.runInto(r.ctx, r.meta, argv, nil, strings.NewReader(cfg), out); err != nil {
		return 0, "", err
	}
	code, loc, _ := strings.Cut(out.buf.String(), "\n")
	status, err := strconv.Atoi(strings.TrimSpace(code))
	if err != nil || status == 0 {
		return 0, "", errors.New("no HTTP answer from the login in the sandbox")
	}
	return status, strings.TrimSpace(loc), nil
}

// page answers the browser with a fixed message; text is already escaped.
func page(w http.ResponseWriter, status int, text string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	fmt.Fprintf(w, "<!doctype html><meta charset=\"utf-8\"><title>podbox</title><p>%s</p>\n", text)
}
