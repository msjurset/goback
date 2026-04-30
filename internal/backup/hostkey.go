package backup

import (
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// hostKeyCallback returns an ssh.HostKeyCallback with trust-on-first-use
// semantics against ~/.ssh/known_hosts:
//   - matching key in known_hosts: accept
//   - host not in known_hosts: append the presented key, log the
//     fingerprint, accept (TOFU)
//   - host present but key changed: reject with a descriptive error
//
// If insecure is true, returns an unsafe callback that accepts any key —
// matches the pre-v1.7 behavior. Use only when an alternative trust path
// (e.g. a private VLAN) makes verification impractical.
func hostKeyCallback(insecure bool) (ssh.HostKeyCallback, error) {
	if insecure {
		return ssh.InsecureIgnoreHostKey(), nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolving home dir for known_hosts: %w", err)
	}
	return hostKeyCallbackFromPath(filepath.Join(home, ".ssh", "known_hosts"))
}

// hostKeyCallbackFromPath is the testable core: builds a TOFU callback
// against the given known_hosts file. Creates the file (and parent dir)
// with 0o700/0o600 perms if missing, since knownhosts.New requires the
// path to exist.
func hostKeyCallbackFromPath(knownHostsPath string) (ssh.HostKeyCallback, error) {
	dir := filepath.Dir(knownHostsPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("creating %s: %w", dir, err)
	}
	if _, err := os.Stat(knownHostsPath); os.IsNotExist(err) {
		f, err := os.OpenFile(knownHostsPath, os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, fmt.Errorf("creating %s: %w", knownHostsPath, err)
		}
		_ = f.Close()
	}

	base, err := knownhosts.New(knownHostsPath)
	if err != nil {
		return nil, fmt.Errorf("loading %s: %w", knownHostsPath, err)
	}

	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		if err := base(hostname, remote, key); err == nil {
			return nil
		} else {
			var kerr *knownhosts.KeyError
			if !errors.As(err, &kerr) {
				return err
			}
			if len(kerr.Want) > 0 {
				return fmt.Errorf(
					"ssh: host key mismatch for %s — known_hosts has a different key on file (presented %s); if the host was reinstalled, remove its entry from %s",
					hostname, ssh.FingerprintSHA256(key), knownHostsPath,
				)
			}
			// Unknown host — TOFU: append and accept.
			if err := appendKnownHost(knownHostsPath, hostname, key); err != nil {
				return fmt.Errorf("recording new host key for %s: %w", hostname, err)
			}
			log.Printf("ssh: trusting %s on first use [%s]", hostname, ssh.FingerprintSHA256(key))
			return nil
		}
	}, nil
}

func appendKnownHost(path, hostname string, key ssh.PublicKey) error {
	line := knownhosts.Line([]string{knownhosts.Normalize(hostname)}, key) + "\n"
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(line)
	return err
}
