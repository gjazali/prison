package hostfw

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func newNftTestPaths(t *testing.T) nftPaths {
	root := t.TempDir()
	return nftPaths{
		rulesFile: filepath.Join(root, "firewall.nft"),
		unitFile:  filepath.Join(root, nftUnitName),
	}
}

func TestNftRules(t *testing.T) {
	rules := nftRules(ipv4Spec)
	for _, want := range []string{
		"delete table inet prison",
		"ip saddr 192.168.64.0/24 ct state established,related accept",
		"ip saddr 192.168.64.0/24 tcp dport { 8787, 2049 } accept",
		"ip saddr 192.168.64.0/24 drop",
		"ip daddr 192.168.64.0/24 drop",
	} {
		if !strings.Contains(rules, want) {
			t.Errorf("nftRules = %s, want %q", rules, want)
		}
	}
	accept := strings.Index(rules, "tcp dport")
	drop := strings.Index(rules, "ip saddr 192.168.64.0/24 drop")
	if accept > drop {
		t.Error("nftRules has drop before accept, want accept first")
	}
}

func TestNftStatusTransitions(t *testing.T) {
	ctx := context.Background()
	p := newNftTestPaths(t)
	runner := newRecordingRunner()
	expect := func(spec Spec, want State, situation string) {
		t.Helper()
		if state, _ := nftStatus(ctx, spec, p, runner.run); state != want {
			t.Errorf("nftStatus = %q %s, want %q", state, situation, want)
		}
	}
	expect(ipv4Spec, StateMissing, "with nothing installed")
	writeFile(t, p.rulesFile, nftRules(ipv4Spec))
	expect(ipv4Spec, StateStale, "without the unit")
	writeFile(t, p.unitFile, nftUnit(p))
	expect(ipv4Spec, StateInstalled, "with both files")
	runner.failures["systemctl is-active --quiet "+nftUnitName] = true
	expect(ipv4Spec, StateStale, "with the unit off")
	runner.failures = map[string]bool{}
	other := Spec{SubnetV4: "192.168.65.0/24", BrokerPort: 8787}
	expect(other, StateStale, "for another subnet")
}

func TestNftInstallCommandSequence(t *testing.T) {
	p := newNftTestPaths(t)
	staging := t.TempDir()
	runner := newRecordingRunner()
	err := nftInstall(context.Background(), ipv4Spec, staging, p, runner.run)
	if err != nil {
		t.Fatalf("nftInstall error = %v, want nil", err)
	}
	rules := filepath.Join(staging, "nft-rules")
	checkCommands(t, runner.commands, []string{
		"sudo nft --check -f " + rules,
		"sudo install -D -m 0644 -o root -g root " + rules + " " +
			p.rulesFile,
		"sudo install -m 0644 -o root -g root " +
			filepath.Join(staging, "nft-unit") + " " + p.unitFile,
		"sudo systemctl daemon-reload",
		"sudo systemctl enable " + nftUnitName,
		"sudo systemctl restart " + nftUnitName,
	})
}

func TestNftInstallStopsWhenTheRulesDoNotParse(t *testing.T) {
	p := newNftTestPaths(t)
	staging := t.TempDir()
	runner := newRecordingRunner()
	check := "sudo nft --check -f " + filepath.Join(staging, "nft-rules")
	runner.failures[check] = true
	err := nftInstall(context.Background(), ipv4Spec, staging, p, runner.run)
	if err == nil || len(runner.commands) != 1 {
		t.Errorf("nftInstall = %v after %d commands, want an error after 1",
			err, len(runner.commands))
	}
}
