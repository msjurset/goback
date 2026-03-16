package credentials

import (
	"encoding/hex"
	"fmt"
	"os/exec"
	"strings"
)

func platformStore(key, value string) error {
	// Delete first to avoid "already exists" error
	_ = platformDelete(key)

	cmd := exec.Command("security", "add-generic-password",
		"-s", serviceName,
		"-a", key,
		"-w", value,
		"-U",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("keychain store: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

func platformLoad(key string) (string, error) {
	cmd := exec.Command("security", "find-generic-password",
		"-s", serviceName,
		"-a", key,
		"-w",
	)
	out, err := cmd.Output()
	if err != nil {
		// Item not found is not an error — return empty string
		return "", nil
	}

	result := strings.TrimSpace(string(out))

	// macOS Keychain hex-encodes values that contain newlines or
	// non-ASCII characters. Detect and decode.
	if isHexEncoded(result) {
		decoded, err := hex.DecodeString(result)
		if err != nil {
			return "", fmt.Errorf("decoding hex keychain value: %w", err)
		}
		return string(decoded), nil
	}

	return result, nil
}

// isHexEncoded checks if a string looks like hex-encoded data from Keychain.
// Keychain hex output is lowercase hex with no separators.
func isHexEncoded(s string) bool {
	if len(s) == 0 || len(s)%2 != 0 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	// Sanity check: the HA token starts with "ey" (base64 JWT) which hex-encodes
	// to "6579". PEM keys start with "-----" which hex-encodes to "2d2d2d2d2d".
	// Both are long strings that wouldn't naturally be all-hex.
	return len(s) > 64
}

func platformDelete(key string) error {
	cmd := exec.Command("security", "delete-generic-password",
		"-s", serviceName,
		"-a", key,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		if strings.Contains(string(out), "could not be found") {
			return nil
		}
		return fmt.Errorf("keychain delete: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}
