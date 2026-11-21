//go:build e2e && linux

package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func tempBaseDir() string {
	// Use /var/tmp instead of /tmp so rootfs images live on disk rather than a
	// small tmpfs mount.
	return "/var/tmp"
}

func systemCABundlePath() string {
	return "/etc/ssl/certs/ca-certificates.crt"
}

func requirePlatformPrereqs(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("firecracker"); err != nil {
		skipOutsideCI(t, "firecracker is not on PATH")
	}
	if _, err := exec.LookPath("buildctl"); err != nil {
		skipOutsideCI(t, "buildctl is not on PATH")
	}
	if f, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0); err != nil {
		skipOutsideCI(t, fmt.Sprintf("/dev/kvm is not usable: %v", err))
	} else {
		f.Close()
	}
	if err := exec.Command("sudo", "-n", "true").Run(); err != nil {
		skipOutsideCI(t, "passwordless sudo is required for e2e tests")
	}
}

func cleanupTestNetwork(name string) {
	unit := "prison-network-" + name + ".service"
	script := "/usr/local/libexec/prison/network-" + name
	table := "prison_" + strings.ReplaceAll(name, "-", "_")

	_ = exec.Command("sudo", "-n", "systemctl", "disable", "--now", unit).Run()
	_ = exec.Command("sudo", "-n", "rm", "-f", "/etc/systemd/system/"+unit, script).Run()
	_ = exec.Command("sudo", "-n", "systemctl", "daemon-reload").Run()
	_ = exec.Command("sudo", "-n", "nft", "delete", "table", "bridge", table).Run()
	for i := range 16 {
		_ = exec.Command("sudo", "-n", "ip", "link", "delete", fmt.Sprintf("%s-%d", name, i)).Run()
	}
	_ = exec.Command("sudo", "-n", "ip", "link", "delete", name).Run()
}

func saveHostFirewall() func() {
	saved, hadFirewall := readOptionalFile("/etc/prison/firewall.nft")
	return func() {
		if !hadFirewall {
			_ = exec.Command("sudo", "-n", "systemctl", "disable", "--now", "prison-firewall.service").Run()
			_ = exec.Command("sudo", "-n", "rm", "-f",
				"/etc/systemd/system/prison-firewall.service", "/etc/prison/firewall.nft").Run()
			_ = exec.Command("sudo", "-n", "systemctl", "daemon-reload").Run()
			_ = exec.Command("sudo", "-n", "nft", "delete", "table", "inet", "prison").Run()
			return
		}
		tmp, err := os.CreateTemp("", "prison-fw-restore-")
		if err != nil {
			return
		}
		defer os.Remove(tmp.Name())
		_, _ = tmp.Write(saved)
		_ = tmp.Close()
		_ = exec.Command("sudo", "-n", "install", "-D", "-m", "0644", "-o", "root", "-g", "root",
			tmp.Name(), "/etc/prison/firewall.nft").Run()
		_ = exec.Command("sudo", "-n", "systemctl", "restart", "prison-firewall.service").Run()
	}
}
