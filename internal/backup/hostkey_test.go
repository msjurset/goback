package backup

import (
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func makeHostKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signerKey, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return signerKey
}

func fakeAddr() net.Addr {
	a, _ := net.ResolveTCPAddr("tcp", "127.0.0.1:22")
	return a
}

func TestHostKeyTOFUAppendsOnFirstSeen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "known_hosts")

	cb, err := hostKeyCallbackFromPath(path)
	if err != nil {
		t.Fatalf("hostKeyCallbackFromPath: %v", err)
	}

	key := makeHostKey(t)
	if err := cb("pi-hole.local:22", fakeAddr(), key); err != nil {
		t.Fatalf("first-time host should TOFU-accept: %v", err)
	}

	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contents), "pi-hole.local") {
		t.Errorf("known_hosts should contain pi-hole.local after TOFU; got %q", contents)
	}
}

func TestHostKeyMatchAccepts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "known_hosts")

	cb1, err := hostKeyCallbackFromPath(path)
	if err != nil {
		t.Fatal(err)
	}
	key := makeHostKey(t)
	if err := cb1("box.local:22", fakeAddr(), key); err != nil {
		t.Fatalf("TOFU first call: %v", err)
	}

	// Second callback (re-loads known_hosts) with same key — must accept.
	cb2, err := hostKeyCallbackFromPath(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := cb2("box.local:22", fakeAddr(), key); err != nil {
		t.Errorf("matching key should accept: %v", err)
	}
}

func TestHostKeyMismatchRejects(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "known_hosts")

	cb1, err := hostKeyCallbackFromPath(path)
	if err != nil {
		t.Fatal(err)
	}
	originalKey := makeHostKey(t)
	if err := cb1("box.local:22", fakeAddr(), originalKey); err != nil {
		t.Fatalf("TOFU first call: %v", err)
	}

	// New callback, different key for the same host: must reject loudly.
	cb2, err := hostKeyCallbackFromPath(path)
	if err != nil {
		t.Fatal(err)
	}
	differentKey := makeHostKey(t)
	err = cb2("box.local:22", fakeAddr(), differentKey)
	if err == nil {
		t.Fatal("mismatched host key should be rejected")
	}
	if !strings.Contains(err.Error(), "host key mismatch") {
		t.Errorf("error should describe mismatch, got: %v", err)
	}
}

func TestHostKeyInsecureAcceptsAll(t *testing.T) {
	cb, err := hostKeyCallback(true)
	if err != nil {
		t.Fatalf("insecure callback should not error: %v", err)
	}
	if err := cb("anywhere.invalid:22", fakeAddr(), makeHostKey(t)); err != nil {
		t.Errorf("insecure callback should accept any key: %v", err)
	}
}

func TestHostKeyMissingFileIsCreated(t *testing.T) {
	dir := t.TempDir()
	// Path with non-existent parent — both should be created.
	path := filepath.Join(dir, "nested", "known_hosts")

	cb, err := hostKeyCallbackFromPath(path)
	if err != nil {
		t.Fatalf("missing file should be auto-created: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("known_hosts not created: %v", err)
	}
	// And it should still TOFU-accept.
	if err := cb("new-host:22", fakeAddr(), makeHostKey(t)); err != nil {
		t.Errorf("post-create TOFU: %v", err)
	}
}

func TestHostKeyMultipleHostsTOFU(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "known_hosts")

	cb, err := hostKeyCallbackFromPath(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{"a.local:22", "b.local:22", "c.local:22"} {
		if err := cb(h, fakeAddr(), makeHostKey(t)); err != nil {
			t.Fatalf("TOFU %s: %v", h, err)
		}
	}
	contents, _ := os.ReadFile(path)
	// knownhosts.Normalize strips the default port (22), so entries are bare hostnames.
	for _, h := range []string{"a.local", "b.local", "c.local"} {
		if !strings.Contains(string(contents), h) {
			t.Errorf("known_hosts should contain %s; got %q", h, contents)
		}
	}
}
