package workspace

import (
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"
)

var (
	secretWords = []string{"secret", "credential", "password", "passwd", "token", "apikey", "api_key", "private"}
	secretExts  = map[string]bool{".p12": true, ".pfx": true, ".jks": true, ".keystore": true, ".kdbx": true, ".ovpn": true, ".gpg": true, ".asc": true, ".tfstate": true}
	configExts  = map[string]bool{".json": true, ".yaml": true, ".yml": true, ".toml": true, ".ini": true, ".conf": true, ".cfg": true, ".properties": true, ".txt": true, "": true}
)

const maxScan = 64 << 10

// looksSecret explains why a file looks sensitive, or returns "".
func looksSecret(r *os.Root, rel string, d fs.DirEntry) string {
	name := strings.ToLower(d.Name())
	ext := filepath.Ext(name)
	if secretExts[ext] {
		return "has a key or credential store extension"
	}
	hinted := false
	for _, w := range secretWords {
		if strings.Contains(name, w) {
			hinted = true
			break
		}
	}
	if !hinted || !configExts[ext] {
		return ""
	}
	fi, err := d.Info()
	if err != nil || fi.Size() > maxScan {
		return "is named like a secret"
	}
	b, err := ReadFile(r, rel, maxScan)
	if err != nil {
		return "is named like a secret"
	}
	if hasHighEntropyToken(string(b)) {
		return "is named like a secret and contains a high-entropy value"
	}
	return "is named like a secret"
}

func hasHighEntropyToken(s string) bool {
	tok := func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("+/=_-", r))
	}
	for _, t := range strings.FieldsFunc(s, tok) {
		if len(t) >= 24 && entropy(t) >= 4.0 {
			return true
		}
	}
	return false
}

// entropy is the Shannon entropy in bits per character.
func entropy(s string) float64 {
	freq := map[rune]float64{}
	for _, r := range s {
		freq[r]++
	}
	var h float64
	n := float64(len(s))
	for _, c := range freq {
		p := c / n
		h -= p * math.Log2(p)
	}
	return h
}
