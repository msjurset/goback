package backup

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/msjurset/goback/internal/config"
	"github.com/msjurset/goback/internal/storage"
)

const sshCommandKillGrace = 5 * time.Second

type SSHProvider struct{}

func (p *SSHProvider) Run(ctx context.Context, cfg config.BackupConfig, store *storage.Manager, opts Options) (Result, error) {
	client, err := p.connect(cfg)
	if err != nil {
		return Result{}, fmt.Errorf("ssh connect: %w", err)
	}
	defer client.Close()

	log.Printf("[%s] ssh connection to %s: ok", cfg.Name, cfg.Host)

	if opts.DryRun {
		return Result{}, p.dryRun(client, cfg)
	}

	if cfg.PreCommand != "" {
		log.Printf("[%s] running pre_command: %s", cfg.Name, cfg.PreCommand)
		if err := p.runCommand(ctx, client, cfg.PreCommand); err != nil {
			return Result{}, fmt.Errorf("pre_command: %w", err)
		}
	}

	remotePath := cfg.RemotePath
	if remotePath == "" && cfg.RemotePattern != "" {
		resolved, err := p.resolvePattern(client, cfg.RemotePattern)
		if err != nil {
			return Result{}, fmt.Errorf("resolving remote_pattern: %w", err)
		}
		log.Printf("[%s] remote_pattern matched: %s", cfg.Name, resolved)
		remotePath = resolved
	}

	ext := compoundExt(remotePath)
	if ext == "" {
		ext = ".backup"
	}
	dest := store.BackupPath(cfg.Folder, cfg.Name, ext, cfg.Filename)

	expected, err := p.remoteSize(client, remotePath)
	if err != nil {
		log.Printf("[%s] could not pre-stat remote (size check skipped): %v", cfg.Name, err)
		expected = -1
	}

	log.Printf("[%s] downloading %s -> %s", cfg.Name, remotePath, dest)
	size, err := AtomicDownload(dest, expected, func(w io.Writer) (int64, error) {
		return p.scpDownloadTo(client, remotePath, w)
	})
	if err != nil {
		return Result{}, fmt.Errorf("scp download: %w", err)
	}

	if cfg.PostCommand != "" {
		log.Printf("[%s] running post_command: %s", cfg.Name, cfg.PostCommand)
		if err := p.runCommand(ctx, client, cfg.PostCommand); err != nil {
			log.Printf("[%s] warning: post_command failed: %v", cfg.Name, err)
		}
	}

	return Result{Filename: filepath.Base(dest), Size: size}, nil
}

func (p *SSHProvider) dryRun(client *ssh.Client, cfg config.BackupConfig) error {
	if cfg.RemotePattern != "" {
		resolved, err := p.resolvePattern(client, cfg.RemotePattern)
		if err != nil {
			if cfg.PreCommand != "" {
				log.Printf("[%s] remote_pattern %s: no match yet (file may be created by pre_command)", cfg.Name, cfg.RemotePattern)
			} else {
				return fmt.Errorf("remote_pattern no match: %s", cfg.RemotePattern)
			}
		} else {
			log.Printf("[%s] remote_pattern matched: %s", cfg.Name, resolved)
		}
	} else {
		session, err := client.NewSession()
		if err != nil {
			return fmt.Errorf("creating session: %w", err)
		}
		defer session.Close()

		out, err := session.Output(fmt.Sprintf("stat -c '%%s' %q 2>/dev/null || stat -f '%%z' %q 2>/dev/null", cfg.RemotePath, cfg.RemotePath))
		if err != nil {
			log.Printf("[%s] remote file %s: not found or not accessible", cfg.Name, cfg.RemotePath)
			if cfg.PreCommand != "" {
				log.Printf("[%s] (file may be created by pre_command: %s)", cfg.Name, cfg.PreCommand)
			} else {
				return fmt.Errorf("remote file not found: %s", cfg.RemotePath)
			}
		} else {
			log.Printf("[%s] remote file %s: exists (%s bytes)", cfg.Name, cfg.RemotePath, string(out[:len(out)-1]))
		}
	}

	if cfg.PreCommand != "" {
		log.Printf("[%s] pre_command configured: %s (skipped)", cfg.Name, cfg.PreCommand)
	}
	if cfg.PostCommand != "" {
		log.Printf("[%s] post_command configured: %s (skipped)", cfg.Name, cfg.PostCommand)
	}

	return nil
}

func (p *SSHProvider) connect(cfg config.BackupConfig) (*ssh.Client, error) {
	host := cfg.Host
	port := "22"
	user := cfg.User

	// Resolve SSH config aliases (e.g., host alias -> actual hostname)
	resolved := resolveSSHConfig(cfg.Host)
	if resolved != nil {
		if resolved.Hostname != "" {
			host = resolved.Hostname
		}
		if resolved.Port != "" {
			port = resolved.Port
		}
		if user == "" && resolved.User != "" {
			user = resolved.User
		}
	}

	if _, _, err := net.SplitHostPort(host); err != nil {
		host = net.JoinHostPort(host, port)
	}

	var authMethods []ssh.AuthMethod

	// Prefer cached SSH key from Keychain (resolved from op://)
	if cfg.SSHKey != "" {
		signer, err := parsePrivateKey([]byte(cfg.SSHKey))
		if err != nil {
			return nil, fmt.Errorf("parsing ssh_key: %w", err)
		}
		authMethods = append(authMethods, ssh.PublicKeys(signer))
	} else {
		// Fall back to SSH agent
		sock := os.Getenv("SSH_AUTH_SOCK")
		if resolved != nil && resolved.IdentityAgent != "" {
			sock = resolved.IdentityAgent
		}
		if sock == "" {
			return nil, fmt.Errorf("no ssh_key configured and no SSH agent available")
		}

		conn, err := net.Dial("unix", sock)
		if err != nil {
			return nil, fmt.Errorf("connecting to SSH agent at %s: %w", sock, err)
		}
		agentClient := agent.NewClient(conn)
		authMethods = append(authMethods, ssh.PublicKeysCallback(agentClient.Signers))
	}

	hkCallback, err := hostKeyCallback(cfg.InsecureSkipHostKey)
	if err != nil {
		return nil, fmt.Errorf("preparing host key verification: %w", err)
	}

	sshConfig := &ssh.ClientConfig{
		User:            user,
		Auth:            authMethods,
		HostKeyCallback: hkCallback,
		Timeout:         30 * time.Second,
	}

	client, err := ssh.Dial("tcp", host, sshConfig)
	if err != nil {
		return nil, fmt.Errorf("dialing %s: %w", host, err)
	}

	return client, nil
}

// compoundExt returns the file extension, handling compound extensions
// like ".tar.gz" and ".tar.bz2" that filepath.Ext would truncate.
func compoundExt(path string) string {
	base := filepath.Base(path)
	ext := filepath.Ext(base)
	if ext == "" {
		return ""
	}
	// Check for compound extension (e.g., .tar.gz, .tar.bz2, .tar.xz)
	withoutExt := strings.TrimSuffix(base, ext)
	if secondExt := filepath.Ext(withoutExt); secondExt == ".tar" {
		return secondExt + ext
	}
	return ext
}

func parsePrivateKey(keyData []byte) (ssh.Signer, error) {
	// Try standard ssh parsing first (handles OpenSSH and PKCS#1 formats)
	signer, err := ssh.ParsePrivateKey(keyData)
	if err == nil {
		return signer, nil
	}

	// Fall back to PKCS#8 parsing (1Password exports keys in this format)
	block, _ := pem.Decode(keyData)
	if block == nil {
		return nil, fmt.Errorf("no PEM block found in key data")
	}

	key, pkcs8Err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if pkcs8Err != nil {
		return nil, fmt.Errorf("failed to parse key (ssh: %v, pkcs8: %v)", err, pkcs8Err)
	}

	return ssh.NewSignerFromKey(key)
}

func (p *SSHProvider) resolvePattern(client *ssh.Client, pattern string) (string, error) {
	session, err := client.NewSession()
	if err != nil {
		return "", err
	}
	defer session.Close()

	// ls -t sorts by modification time (newest first), glob expands the pattern
	out, err := session.Output(fmt.Sprintf("ls -t %s 2>/dev/null | head -1", pattern))
	if err != nil {
		return "", fmt.Errorf("no files matching pattern %s", pattern)
	}

	result := strings.TrimSpace(string(out))
	if result == "" {
		return "", fmt.Errorf("no files matching pattern %s", pattern)
	}
	return result, nil
}

// runCommand executes cmd on the remote host. If ctx is cancelled before the
// command exits, runCommand sends SIGTERM, waits up to sshCommandKillGrace,
// and returns ctx.Err().
func (p *SSHProvider) runCommand(ctx context.Context, client *ssh.Client, cmd string) error {
	session, err := client.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()

	session.Stdout = os.Stdout
	session.Stderr = os.Stderr

	if err := session.Start(cmd); err != nil {
		return err
	}

	done := make(chan error, 1)
	go func() { done <- session.Wait() }()

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		_ = session.Signal(ssh.SIGTERM)
		select {
		case <-done:
		case <-time.After(sshCommandKillGrace):
		}
		return ctx.Err()
	}
}

// scpDownloadTo streams the remote file via `cat` into w. Returns the number of
// bytes copied and any session error (including non-zero exit from `cat`).
func (p *SSHProvider) scpDownloadTo(client *ssh.Client, remotePath string, w io.Writer) (int64, error) {
	session, err := client.NewSession()
	if err != nil {
		return 0, err
	}
	defer session.Close()

	stdout, err := session.StdoutPipe()
	if err != nil {
		return 0, err
	}

	if err := session.Start(fmt.Sprintf("cat %q", remotePath)); err != nil {
		return 0, err
	}

	n, copyErr := io.Copy(w, stdout)
	waitErr := session.Wait()
	if copyErr != nil {
		return n, copyErr
	}
	if waitErr != nil {
		return n, waitErr
	}
	return n, nil
}

// remoteSize returns the size of remotePath via `stat`. Works on both GNU and
// BSD stat (Linux uses -c, macOS/BSD uses -f).
func (p *SSHProvider) remoteSize(client *ssh.Client, remotePath string) (int64, error) {
	session, err := client.NewSession()
	if err != nil {
		return 0, err
	}
	defer session.Close()

	out, err := session.Output(fmt.Sprintf("stat -c '%%s' %q 2>/dev/null || stat -f '%%z' %q 2>/dev/null", remotePath, remotePath))
	if err != nil {
		return 0, err
	}
	s := strings.TrimSpace(string(out))
	if s == "" {
		return 0, fmt.Errorf("empty stat output for %s", remotePath)
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parsing remote size %q: %w", s, err)
	}
	return n, nil
}
