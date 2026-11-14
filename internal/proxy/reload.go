package proxy

import (
	"context"
	"os"
	"time"

	"github.com/Crocogiciel-Studio/podbox/internal/policy"
)

type fileStamp struct {
	mod  time.Time
	size int64
}

func stamp(path string) (fileStamp, bool) {
	fi, err := os.Stat(path)
	if err != nil {
		return fileStamp{}, false
	}
	return fileStamp{fi.ModTime(), fi.Size()}, true
}

// load swaps in the policy file. A bad file keeps the previous valid
// policy; without one, everything is denied.
func (s *Server) load() {
	e, err := policy.ReadEffective(s.cfg.PolicyPath)
	if err != nil {
		if s.policy.Load() == nil {
			s.logEvent("policy unavailable, denying all", err)
		} else {
			s.logEvent("policy reload failed, keeping previous", err)
		}
		return
	}
	s.policy.Store(e)
	s.logEvent("policy loaded", nil)
}

// Watch polls the policy file and reloads it on change until ctx ends.
func (s *Server) Watch(ctx context.Context) {
	if s.cfg.AllowAll {
		return
	}
	last, _ := stamp(s.cfg.PolicyPath)
	t := time.NewTicker(s.cfg.PollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			cur, ok := stamp(s.cfg.PolicyPath)
			if cur == last {
				continue
			}
			last = cur
			if !ok {
				s.logEvent("policy file missing, keeping previous", nil)
				continue
			}
			s.load()
		}
	}
}
