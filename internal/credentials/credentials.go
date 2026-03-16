package credentials

import (
	"fmt"
	"os/exec"
	"strings"
)

const serviceName = "markback"

// Store saves a secret to the platform keychain under the given key.
func Store(key, value string) error {
	return platformStore(key, value)
}

// Load retrieves a secret from the platform keychain by key.
// Returns empty string and nil error if the key doesn't exist.
func Load(key string) (string, error) {
	return platformLoad(key)
}

// Delete removes a secret from the platform keychain by key.
func Delete(key string) error {
	return platformDelete(key)
}

// ResolveOp calls `op read` to resolve a 1Password secret reference.
func ResolveOp(ref string) (string, error) {
	out, err := exec.Command("op", "read", ref).Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("op read %s: %s", ref, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return "", fmt.Errorf("op read %s: %w", ref, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// ResolveAndCache resolves an op:// reference via 1Password CLI, stores the
// result in the platform keychain, and returns the value.
func ResolveAndCache(key, opRef string) (string, error) {
	val, err := ResolveOp(opRef)
	if err != nil {
		return "", err
	}
	if err := Store(key, val); err != nil {
		return "", fmt.Errorf("storing in keychain: %w", err)
	}
	return val, nil
}

// LoadOrResolve tries the platform keychain first, falling back to op read
// and caching the result.
func LoadOrResolve(key, opRef string) (string, error) {
	val, err := Load(key)
	if err != nil {
		return "", err
	}
	if val != "" {
		return val, nil
	}
	return ResolveAndCache(key, opRef)
}
