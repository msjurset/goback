package backup

import (
	"testing"
)

func TestParseSSHConfigLine(t *testing.T) {
	tests := []struct {
		line    string
		wantKey string
		wantVal string
		wantOK  bool
	}{
		{"Hostname 192.168.1.1", "Hostname", "192.168.1.1", true},
		{"User pi", "User", "pi", true},
		{"Port=2222", "Port", "2222", true},
		{"Host pi-hole", "Host", "pi-hole", true},
		{"IdentityAgent \"~/Library/path\"", "IdentityAgent", "\"~/Library/path\"", true},
		{"badline", "", "", false},
		{"", "", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			key, val, ok := parseSSHConfigLine(tt.line)
			if ok != tt.wantOK {
				t.Errorf("ok = %v, want %v", ok, tt.wantOK)
			}
			if key != tt.wantKey {
				t.Errorf("key = %q, want %q", key, tt.wantKey)
			}
			if val != tt.wantVal {
				t.Errorf("val = %q, want %q", val, tt.wantVal)
			}
		})
	}
}

func TestMatchHost(t *testing.T) {
	tests := []struct {
		pattern string
		alias   string
		want    bool
	}{
		{"pi-hole", "pi-hole", true},
		{"pi-hole", "pihole", false},
		{"*", "*", true},
		{"ha pi-hole", "pi-hole", true},
		{"ha pi-hole", "ha", true},
		{"ha pi-hole", "wrt", false},
	}

	for _, tt := range tests {
		t.Run(tt.pattern+"_"+tt.alias, func(t *testing.T) {
			got := matchHost(tt.pattern, tt.alias)
			if got != tt.want {
				t.Errorf("matchHost(%q, %q) = %v, want %v", tt.pattern, tt.alias, got, tt.want)
			}
		})
	}
}
