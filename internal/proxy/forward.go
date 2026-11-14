package proxy

import (
	"context"
	"io"
	"net/http"
)

var hopHeaders = []string{
	"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate",
	"Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
}

func (s *Server) handleForward(w http.ResponseWriter, r *http.Request) {
	port := r.URL.Port()
	if port == "" {
		port = "80"
		if r.URL.Scheme == "https" {
			port = "443"
		}
	}
	rt, ok := s.authorize(r.Context(), w, r.Method, r.URL.Hostname(), port)
	if !ok {
		return
	}
	out := r.Clone(context.WithValue(r.Context(), dialKey{}, rt.addr))
	out.RequestURI = ""
	for _, h := range hopHeaders {
		out.Header.Del(h)
	}
	t := s.direct
	if rt.byName {
		t = s.upstreamT
	}
	resp, err := t.RoundTrip(out)
	if err != nil {
		writeError(w, http.StatusBadGateway, "podbox proxy: "+err.Error())
		return
	}
	defer resp.Body.Close()
	for _, h := range hopHeaders {
		resp.Header.Del(h)
	}
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}
