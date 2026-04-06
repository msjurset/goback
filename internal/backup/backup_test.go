package backup

import (
	"testing"
)

func TestNewProviderSSH(t *testing.T) {
	p, err := NewProvider("ssh")
	if err != nil {
		t.Fatalf("NewProvider(ssh) error: %v", err)
	}
	if _, ok := p.(*SSHProvider); !ok {
		t.Errorf("NewProvider(ssh) returned %T, want *SSHProvider", p)
	}
}

func TestNewProviderHA(t *testing.T) {
	p, err := NewProvider("ha_api")
	if err != nil {
		t.Fatalf("NewProvider(ha_api) error: %v", err)
	}
	if _, ok := p.(*HAProvider); !ok {
		t.Errorf("NewProvider(ha_api) returned %T, want *HAProvider", p)
	}
}

func TestNewProviderLocal(t *testing.T) {
	p, err := NewProvider("local")
	if err != nil {
		t.Fatalf("NewProvider(local) error: %v", err)
	}
	if _, ok := p.(*LocalProvider); !ok {
		t.Errorf("NewProvider(local) returned %T, want *LocalProvider", p)
	}
}

func TestNewProviderUnknown(t *testing.T) {
	_, err := NewProvider("ftp")
	if err == nil {
		t.Error("NewProvider(ftp) expected error, got nil")
	}
}
