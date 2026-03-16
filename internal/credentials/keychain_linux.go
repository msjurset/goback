package credentials

import (
	"fmt"
	"os/exec"
	"strings"
)

func platformStore(key, value string) error {
	cmd := exec.Command("secret-tool", "store",
		"--label", serviceName+" "+key,
		"service", serviceName,
		"account", key,
	)
	cmd.Stdin = strings.NewReader(value)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("secret-tool store: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

func platformLoad(key string) (string, error) {
	cmd := exec.Command("secret-tool", "lookup",
		"service", serviceName,
		"account", key,
	)
	out, err := cmd.Output()
	if err != nil {
		return "", nil
	}
	return strings.TrimSpace(string(out)), nil
}

func platformDelete(key string) error {
	cmd := exec.Command("secret-tool", "clear",
		"service", serviceName,
		"account", key,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("secret-tool clear: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}
