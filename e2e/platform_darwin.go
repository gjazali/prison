//go:build e2e && darwin

package e2e

import (
	"os"
	"os/exec"
	"testing"
)

func tempBaseDir() string {
	return os.TempDir()
}

func systemCABundlePath() string {
	return "/etc/ssl/cert.pem"
}

func requirePlatformPrereqs(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("container"); err != nil {
		skipOutsideCI(t, "Apple `container` CLI is not on PATH")
	}
	if err := exec.Command("container", "system", "status").Run(); err != nil {
		skipOutsideCI(t, "Apple Container system is not running (`container system start`)")
	}
	if err := exec.Command("sudo", "-n", "true").Run(); err != nil {
		skipOutsideCI(t, "passwordless sudo (or cached sudo credentials) is required for e2e tests")
	}
}

func cleanupTestNetwork(name string) {
	_ = exec.Command("container", "network", "delete", name).Run()
}

func saveHostFirewall() func() {
	const (
		anchorPath = "/etc/pf.anchors/prison"
		pfConfPath = "/etc/pf.conf"
		daemonPath = "/Library/LaunchDaemons/prison.firewall.plist"
	)
	savedAnchor, hadAnchor := readOptionalFile(anchorPath)
	savedConf, hadConf := readOptionalFile(pfConfPath)

	return func() {
		if !hadAnchor {
			_ = exec.Command("sudo", "-n", "launchctl", "bootout", "system/prison.firewall").Run()
			_ = exec.Command("sudo", "-n", "rm", "-f", anchorPath, daemonPath).Run()
			if hadConf {
				writeAsRoot(pfConfPath, savedConf)
				_ = exec.Command("sudo", "-n", "pfctl", "-f", pfConfPath).Run()
			}
			return
		}
		writeAsRoot(anchorPath, savedAnchor)
		if hadConf {
			writeAsRoot(pfConfPath, savedConf)
		}
		_ = exec.Command("sudo", "-n", "pfctl", "-f", pfConfPath).Run()
	}
}

func writeAsRoot(target string, data []byte) {
	tmp, err := os.CreateTemp("", "prison-pf-restore-")
	if err != nil {
		return
	}
	defer os.Remove(tmp.Name())
	_, _ = tmp.Write(data)
	_ = tmp.Close()
	_ = exec.Command("sudo", "-n", "install", "-m", "0644", "-o", "root", "-g", "wheel",
		tmp.Name(), target).Run()
}
