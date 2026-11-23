package sandbox

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Crocogiciel-Studio/podbox/internal/policy"
	"github.com/Crocogiciel-Studio/podbox/internal/runtime"
)

// loginRT is a running agent container: the opener log, curl, and a login
// command that runs until the test releases it.
type loginRT struct {
	runtime.Runtime
	mu       sync.Mutex
	opener   string
	curlIn   []string
	curlArgv [][]string
	answer   string // curl's -w output
	release  chan int
}

func (l *loginRT) ContainerInspect(context.Context, string) (runtime.Container, error) {
	return runtime.Container{State: "running"}, nil
}

func (l *loginRT) Exec(ctx context.Context, _ string, s runtime.ExecSpec) (int, error) {
	switch s.Command[0] {
	case "rm":
		return 0, nil
	case "sh":
		l.mu.Lock()
		defer l.mu.Unlock()
		io.WriteString(s.Stdout, l.opener)
		return 0, nil
	case "curl":
		b, _ := io.ReadAll(s.Stdin)
		l.mu.Lock()
		defer l.mu.Unlock()
		l.curlIn = append(l.curlIn, string(b))
		l.curlArgv = append(l.curlArgv, s.Command)
		io.WriteString(s.Stdout, l.answer)
		return 0, nil
	}
	select {
	case c := <-l.release:
		return c, nil
	case <-ctx.Done():
		return -1, ctx.Err()
	}
}

func (l *loginRT) open(u string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.opener += u + "\n"
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func signinURL(redirect string) string {
	return "https://app.kiro.dev/signin?state=s1&redirect_uri=" + url.QueryEscape(redirect)
}

type loginRun struct {
	t      *testing.T
	m      *Manager
	rt     *loginRT
	events chan LoginEvent
	done   chan struct{}
	res    LoginResult
	err    error
}

func startLogin(t *testing.T, agent string, opt LoginOptions) *loginRun {
	t.Helper()
	st := Store{Root: t.TempDir(), ConfigDir: t.TempDir()}
	if err := st.Save(Meta{Name: "s", Agent: agent, Workspace: "/w"}); err != nil {
		t.Fatal(err)
	}
	rt := &loginRT{answer: "302\nhttps://app.kiro.dev/signin?auth_status=success", release: make(chan int, 1)}
	run := &loginRun{t: t, m: &Manager{RT: rt, Store: st}, rt: rt, events: make(chan LoginEvent, 64), done: make(chan struct{})}
	if opt.Poll == 0 {
		opt.Poll = 5 * time.Millisecond
	}
	go func() {
		defer close(run.done)
		run.res, run.err = run.m.Login(context.Background(), "s", opt, IO{}, func(e LoginEvent) { run.events <- e })
	}()
	return run
}

func (r *loginRun) wait(kind LoginEventKind) LoginEvent {
	r.t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case e := <-r.events:
			if e.Kind == kind {
				return e
			}
		case <-timeout:
			r.t.Fatalf("no %s event", kind)
		}
	}
}

func (r *loginRun) finish(code int) {
	r.t.Helper()
	r.rt.release <- code
	select {
	case <-r.done:
	case <-time.After(5 * time.Second):
		r.t.Fatal("login did not return")
	}
}

var noFollow = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Timeout: 5 * time.Second}

func get(t *testing.T, method, target, host string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(method, target, nil)
	if host != "" {
		req.Host = host
	}
	resp, err := noFollow.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

func TestLoginRelaysCallback(t *testing.T) {
	port := freePort(t)
	run := startLogin(t, "kiro", LoginOptions{})
	run.rt.open(signinURL("http://localhost:" + strconv.Itoa(port)))
	if e := run.wait(EventListening); e.Port != port {
		t.Fatalf("listening on %d", e.Port)
	}
	base := "http://127.0.0.1:" + strconv.Itoa(port)
	resp := get(t, "GET", base+"/signin/callback?login_option=x&code=SECRET123&state=s1", "")
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "https://app.kiro.dev/signin?auth_status=success" {
		t.Fatalf("browser got %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if e := run.wait(EventCallback); e.Status != 302 {
		t.Fatalf("callback %+v", e)
	}
	run.rt.mu.Lock()
	in, argv := run.rt.curlIn[0], strings.Join(run.rt.curlArgv[0], " ")
	run.rt.mu.Unlock()
	if in != "url = \"http://localhost:"+strconv.Itoa(port)+"/signin/callback?login_option=x&code=SECRET123&state=s1\"\n" {
		t.Fatalf("curl config %q", in)
	}
	if strings.Contains(argv, "SECRET123") || !strings.Contains(argv, "--noproxy * ") || !strings.Contains(argv, "--config -") {
		t.Fatalf("curl argv %q", argv)
	}

	// Only GET, on a loopback Host, on the callback, is relayed.
	if c := get(t, "POST", base+"/signin/callback", "").StatusCode; c != http.StatusMethodNotAllowed {
		t.Errorf("POST: %d", c)
	}
	if c := get(t, "GET", base+"/signin/callback?code=x", "evil.example:"+strconv.Itoa(port)).StatusCode; c != http.StatusForbidden {
		t.Errorf("foreign Host: %d", c)
	}
	if c := get(t, "GET", base+"/favicon.ico", "").StatusCode; c != http.StatusNotFound {
		t.Errorf("favicon: %d", c)
	}

	// A redirect the sandbox chose is followed only to https.
	run.rt.mu.Lock()
	run.rt.answer = "302\nhttp://evil.example/x"
	run.rt.mu.Unlock()
	if resp := get(t, "GET", base+"/signin/callback?code=y", "localhost:"+strconv.Itoa(port)); resp.StatusCode != http.StatusOK || resp.Header.Get("Location") != "" {
		t.Errorf("http redirect followed: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}

	run.finish(0)
	if run.err != nil || run.res.Code != 0 || run.res.URLs != 1 || run.res.Callbacks != 2 {
		t.Fatalf("%+v %v", run.res, run.err)
	}
	if _, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), time.Second); err == nil {
		t.Fatal("the relay still listens after the login")
	}
	close(run.events)
	for e := range run.events {
		if strings.Contains(e.Message, "SECRET123") || strings.Contains(e.Message, "code=y") {
			t.Fatalf("event leaks the query: %+v", e)
		}
	}
}

// A callback naming an identity provider the policy refuses is held until
// the human allows it, then relayed on reload.
func TestLoginHoldsCallbackForIssuer(t *testing.T) {
	port := freePort(t)
	run := startLogin(t, "kiro", LoginOptions{})
	run.rt.open(signinURL("http://localhost:" + strconv.Itoa(port)))
	run.wait(EventListening)
	cb := "http://localhost:" + strconv.Itoa(port) + "/signin/callback?login_option=external_idp&issuer_url=" +
		url.QueryEscape("https://login.microsoftonline.com/tenant/v2.0") + "&state=s1"
	if resp := get(t, "GET", strings.Replace(cb, "localhost", "127.0.0.1", 1), "localhost:"+strconv.Itoa(port)); resp.StatusCode != http.StatusOK {
		t.Fatalf("held callback: %d", resp.StatusCode)
	}
	if e := run.wait(EventNeedsHost); e.Host != "login.microsoftonline.com" {
		t.Fatalf("%+v", e)
	}
	run.rt.mu.Lock()
	n := len(run.rt.curlIn)
	run.rt.mu.Unlock()
	if n != 0 {
		t.Fatal("relayed before the host was allowed")
	}
	if err := policy.SetRule(run.m.Store.SessionPath("s"), "login.microsoftonline.com", true, nil); err != nil {
		t.Fatal(err)
	}
	if resp := get(t, "GET", strings.Replace(cb, "localhost", "127.0.0.1", 1), "localhost:"+strconv.Itoa(port)); resp.StatusCode != http.StatusFound {
		t.Fatalf("after allow: %d", resp.StatusCode)
	}
	run.finish(0)
}

func TestLoginPortBusy(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	run := startLogin(t, "kiro", LoginOptions{})
	run.rt.open(signinURL("http://localhost:" + strconv.Itoa(port)))
	select {
	case <-run.done:
	case <-time.After(5 * time.Second):
		t.Fatal("login kept running")
	}
	if !errors.Is(run.err, ErrPortBusy) || !strings.Contains(run.err.Error(), "lsof -nP -iTCP:"+strconv.Itoa(port)) {
		t.Fatalf("%v", run.err)
	}
}

func TestLoginCustomSchemeNotRelayed(t *testing.T) {
	run := startLogin(t, "kiro", LoginOptions{})
	run.rt.open(signinURL("kiro://kiro.oauth/callback"))
	if e := run.wait(EventWarning); !strings.Contains(e.Message, "kiro://kiro.oauth") {
		t.Fatalf("%+v", e)
	}
	run.finish(3)
	if run.err != nil || run.res.Code != 3 {
		t.Fatalf("%+v %v", run.res, run.err)
	}
}

func TestLoginNeedsCommand(t *testing.T) {
	run := startLogin(t, "shell", LoginOptions{})
	<-run.done
	if !errors.Is(run.err, ErrNoLogin) {
		t.Fatalf("%v", run.err)
	}
	run = startLogin(t, "shell", LoginOptions{Command: []string{"fake-login"}})
	run.finish(0)
	if run.err != nil || run.res.Command[0] != "fake-login" {
		t.Fatalf("%+v %v", run.res, run.err)
	}
}
