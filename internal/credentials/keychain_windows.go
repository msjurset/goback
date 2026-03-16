package credentials

import (
	"fmt"
	"os/exec"
	"strings"
)

func platformStore(key, value string) error {
	target := serviceName + "/" + key
	// cmdkey /generic:target /user:key /pass:value
	cmd := exec.Command("cmdkey",
		"/generic:"+target,
		"/user:"+key,
		"/pass:"+value,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("cmdkey store: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

func platformLoad(key string) (string, error) {
	target := serviceName + "/" + key
	cmd := exec.Command("powershell", "-NoProfile", "-Command",
		fmt.Sprintf(`(New-Object -TypeName PSCredential -ArgumentList "%s", (Get-StoredCredential -Target "%s").Password).GetNetworkCredential().Password`, key, target),
	)
	out, err := cmd.Output()
	if err != nil {
		return "", nil
	}
	return strings.TrimSpace(string(out)), nil
}

func platformDelete(key string) error {
	target := serviceName + "/" + key
	cmd := exec.Command("cmdkey", "/delete:"+target)
	if out, err := cmd.CombinedOutput(); err != nil {
		if strings.Contains(string(out), "not found") {
			return nil
		}
		return fmt.Errorf("cmdkey delete: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}
