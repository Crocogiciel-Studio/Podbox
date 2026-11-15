package proxy

import (
	"io"
	"net"
	"net/http"
	"sync"
)

func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request) {
	host, port := splitTarget(r.Host, "443")
	rt, ok := s.authorize(r.Context(), w, http.MethodConnect, host, port)
	if !ok {
		return
	}
	var upstream net.Conn
	var err error
	if rt.byName {
		upstream, err = s.dialUpstream(r.Context(), net.JoinHostPort(rt.host, rt.port))
	} else {
		upstream, err = s.cfg.Dial(r.Context(), "tcp", rt.addr)
	}
	if err != nil {
		s.logEvent("connect "+rt.host+" failed", err)
		writeError(w, http.StatusBadGateway, "podbox proxy: cannot reach "+rt.host+": "+err.Error())
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		upstream.Close()
		writeError(w, http.StatusInternalServerError, "podbox proxy: hijack unsupported")
		return
	}
	client, buf, err := hj.Hijack()
	if err != nil {
		upstream.Close()
		return
	}
	if _, err := client.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n")); err != nil {
		client.Close()
		upstream.Close()
		return
	}
	tunnel(client, buf.Reader, upstream)
}

// tunnel copies both ways until either side closes.
func tunnel(client net.Conn, clientReader io.Reader, upstream net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(upstream, clientReader)
		closeWrite(upstream)
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(client, upstream)
		closeWrite(client)
	}()
	wg.Wait()
	client.Close()
	upstream.Close()
}

func closeWrite(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
		return
	}
	_ = c.Close()
}
