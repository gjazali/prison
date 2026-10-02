//go:build integration && linux

package hostfw

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestIntegrationNftRulesParse(t *testing.T) {
	if _, err := exec.LookPath("nft"); err != nil {
		t.Skip("nft is not on PATH")
	}
	path := filepath.Join(t.TempDir(), "rules.nft")
	spec := Spec{SubnetV4: "192.168.198.0/24", SubnetV6: "fd00:1::/64",
		BrokerPort: 8787}
	if err := os.WriteFile(path, []byte(nftRules(spec)), 0o644); err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command("sudo", "-n", "nft", "--check", "-f",
		path).CombinedOutput()
	if err != nil {
		t.Fatalf("nft --check: %v\n%s", err, output)
	}
}
