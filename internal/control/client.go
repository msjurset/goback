package control

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"time"
)

// ErrDaemonUnreachable is returned by Run when the control socket cannot be
// dialed. Callers can use errors.Is to detect it and decide whether to fall
// back to in-process execution or surface the error.
var ErrDaemonUnreachable = errors.New("goback daemon control socket unreachable")

// Run dials the daemon's control socket, sends req, streams log lines to
// stderr, and returns the exit code from the result message.
//
// On dial failure, returns (0, ErrDaemonUnreachable wrapped with the
// underlying error).
func Run(socketPath string, req Request, stderr io.Writer) (int, error) {
	conn, err := net.DialTimeout("unix", socketPath, 5*time.Second)
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrDaemonUnreachable, err)
	}
	defer conn.Close()

	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return 0, fmt.Errorf("sending request: %w", err)
	}

	dec := json.NewDecoder(conn)
	for {
		var resp Response
		if err := dec.Decode(&resp); err != nil {
			if errors.Is(err, io.EOF) {
				return 0, errors.New("daemon closed connection without result")
			}
			return 0, fmt.Errorf("decoding response: %w", err)
		}

		switch resp.Type {
		case "log":
			if resp.Line != "" {
				fmt.Fprintln(stderr, resp.Line)
			}
		case "result":
			if resp.Error != "" {
				fmt.Fprintf(stderr, "goback: %s\n", resp.Error)
			}
			exit := resp.Exit
			if exit == 0 && resp.Error != "" {
				exit = 1
			}
			return exit, nil
		}
	}
}
