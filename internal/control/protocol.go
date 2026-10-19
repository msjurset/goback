// Package control implements a Unix-socket protocol that lets `goback run`
// delegate execution to the running daemon. Cron- and launchd-spawned
// invocations cannot reach the user's keychain to resolve op:// secrets;
// the daemon, running in the user's GUI session, can. Routing run requests
// through the daemon eliminates that gap.
package control

import (
	"os"
	"path/filepath"
)

// Request is the single message a client sends after dialing the socket.
type Request struct {
	Cmd     string `json:"cmd"`                // "run" or "ping"
	Name    string `json:"name,omitempty"`     // backup name for "run"; empty = all
	DryRun  bool   `json:"dry_run,omitempty"`  // for "run"
	Retries int    `json:"retries,omitempty"`  // for "run": >1 enables retry with that many attempts (default 1 = NoRetry)
}

// Response is one of N messages the server streams back. A connection ends
// after exactly one Response with Type == "result".
type Response struct {
	Type   string `json:"type"`              // "log" or "result"
	Stream string `json:"stream,omitempty"`  // for "log": "stdout"|"stderr"
	Line   string `json:"line,omitempty"`    // for "log"
	Exit   int    `json:"exit,omitempty"`    // for "result"
	Error  string `json:"error,omitempty"`   // for "result"
}

// DefaultSocketPath returns the canonical control socket path:
// $HOME/.config/goback/control.sock.
func DefaultSocketPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "/tmp/goback-control.sock"
	}
	return filepath.Join(home, ".config", "goback", "control.sock")
}
