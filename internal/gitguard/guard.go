package gitguard

import "os"

// Guard is the single switch of the git protections. A clone sandbox's
// workspace is the agent's own repository, not the host's: nothing on the
// host runs git inside it, so every protection is off. Callers ask the
// Guard and never test the sandbox mode themselves.
type Guard struct{ Clone bool }

// Active reports whether the protections apply (a shared workspace).
func (g Guard) Active() bool { return !g.Clone }

// ReadOnly lists the git paths to bind read-only; see ReadOnly.
func (g Guard) ReadOnly(r *os.Root) []string {
	if !g.Active() {
		return nil
	}
	return ReadOnly(r)
}

// GitDir reports whether .git is to be bound on itself; see HasDir.
func (g Guard) GitDir(r *os.Root) bool { return g.Active() && HasDir(r) }

// Pointer reports a .git file whose directory is unprotected; see IsPointer.
func (g Guard) Pointer(r *os.Root) bool { return g.Active() && IsPointer(r) }

// Record fingerprints ws's .git; nil when the Guard is inactive.
func (g Guard) Record(ws string) (*Print, error) {
	if !g.Active() {
		return nil, nil
	}
	p, err := Fingerprint(ws)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// Check compares ws's .git with old; nil when the Guard is inactive.
func (g Guard) Check(ws string, old Print) (*Integrity, error) {
	if !g.Active() {
		return nil, nil
	}
	return Check(ws, old)
}

// Notice is the hook notice for ws; "" when the Guard is inactive.
func (g Guard) Notice(name, ws string) string {
	if !g.Active() {
		return ""
	}
	return Notice(name, ws)
}
