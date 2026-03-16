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
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/msjurset/goback/internal/config"
	"github.com/msjurset/goback/internal/storage"
)

type SSHProvider struct{}

func (p *SSHProvider) Run(ctx context.Context, cfg config.BackupConfig, store *storage.Manager, opts Options) error {
	client, err := p.connect(cfg)
	if err != nil {
		return fmt.Errorf("ssh connect: %w", err)
	}
	defer client.Close()

	log.Printf("[%s] ssh connection to %s: ok", cfg.Name, cfg.Host)

	if opts.DryRun {
		return p.dryRun(client, cfg)
	}

	if cfg.PreCommand != "" {
		log.Printf("[%s] running pre_command: %s", cfg.Name, cfg.PreCommand)
		if err := p.runCommand(client, cfg.PreCommand); err != nil {
			return fmt.Errorf("pre_command: %w", err)
		}
	}

	remotePath := cfg.RemotePath
	if remotePath == "" && cfg.RemotePattern != "" {
		resolved, err := p.resolvePattern(client, cfg.RemotePattern)
		if err != nil {
			return fmt.Errorf("resolving remote_pattern: %w", err)
		}
		log.Printf("[%s] remote_pattern matched: %s", cfg.Name, resolved)
		remotePath = resolved
	}

	ext := compoundExt(remotePath)
	if ext == "" {
		ext = ".backup"
	}
	dest := store.BackupPath(cfg.Folder, cfg.Name, ext, cfg.Filename)

	log.Printf("[%s] downloading %s -> %s", cfg.Name, remotePath, dest)
	size, err := p.scpDownload(client, remotePath, dest)
	if err != nil {
		return fmt.Errorf("scp download: %w", err)
	}

	if cfg.PostCommand != "" {
		log.Printf("[%s] running post_command: %s", cfg.Name, cfg.PostCommand)
		if err := p.runCommand(client, cfg.PostCommand); err != nil {
			log.Printf("[%s] warning: post_command failed: %v", cfg.Name, err)
		}
	}

	return store.RecordBackup(storage.BackupRecord{
		Name:      cfg.Name,
		Filename:  filepath.Base(dest),
		Timestamp: time.Now(),
		Size:      size,
	})
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

	sshConfig := &ssh.ClientConfig{
		User:            user,
		Auth:            authMethods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
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

func (p *SSHProvider) runCommand(client *ssh.Client, cmd string) error {
	session, err := client.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()

	session.Stdout = os.Stdout
	session.Stderr = os.Stderr

	return session.Run(cmd)
}

func (p *SSHProvider) scpDownload(client *ssh.Client, remotePath, localPath string) (int64, error) {
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

	f, err := os.Create(localPath)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	n, err := io.Copy(f, stdout)
	if err != nil {
		return 0, err
	}

	if err := session.Wait(); err != nil {
		return 0, err
	}

	return n, nil
}
