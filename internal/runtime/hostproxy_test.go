package runtime

import "testing"

func TestToHost(t *testing.T) {
	cases := []struct {
		in, want string
		ok       bool
	}{
		{"http://127.0.0.1:3128", "http://host.containers.internal:3128", true},
		{"http://u:p@localhost:9000/", "http://u:p@host.containers.internal:9000/", true},
		{"http://[::1]:3128", "http://host.containers.internal:3128", true},
		{"127.0.0.1:3128", "host.containers.internal:3128", true},
		{"http://127.0.0.1", "http://host.containers.internal", true},
		{"http://corp.example:8080", "http://corp.example:8080", false},
		{"corp:8080", "corp:8080", false},
		{"", "", false},
	}
	for _, c := range cases {
		if got, ok := ToHost(c.in); got != c.want || ok != c.ok {
			t.Errorf("ToHost(%q) = %q, %v; want %q, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}
