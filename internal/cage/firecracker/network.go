package firecracker

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"prison/internal/cage"
	"prison/internal/nfsexport"
	"prison/internal/ui"
)

const (
	tapPoolSize         = 16
	firstBoxHostNumber  = 10
	interfaceNameLimit  = 15
	networkScriptFolder = "/usr/local/libexec/prison"
	systemdUnitFolder   = "/etc/systemd/system"
)

var networkNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// boxNetwork is a host-only network. It has a bridge with the gateway
// address and a pool of taps that the user owns.
type boxNetwork struct {
	name    string
	subnet  netip.Prefix
	gateway netip.Addr
}

// networkFor hashes the name so that a network always gets the same subnet.
func networkFor(name string) (boxNetwork, error) {
	longestTap := name + "-" + strconv.Itoa(tapPoolSize-1)
	if !networkNamePattern.MatchString(name) ||
		len(longestTap) > interfaceNameLimit {
		return boxNetwork{}, fmt.Errorf("network name %q is not valid. "+
			"Use up to %d lowercase letters, digits, and dashes", name,
			interfaceNameLimit-len("-15"))
	}
	digest := fnv.New32a()
	digest.Write([]byte(name))
	third := byte(100 + digest.Sum32()%100)
	subnet := netip.PrefixFrom(netip.AddrFrom4([4]byte{192, 168, third, 0}),
		24)
	return boxNetwork{
		name:    name,
		subnet:  subnet,
		gateway: netip.AddrFrom4([4]byte{192, 168, third, 1}),
	}, nil
}

func IsBoxAddress(address netip.Addr) bool {
	if !address.Is4() {
		return false
	}
	octets := address.As4()
	return octets[0] == 192 && octets[1] == 168 &&
		octets[2] >= 100 && octets[2] <= 199 &&
		int(octets[3]) >= firstBoxHostNumber &&
		int(octets[3]) < firstBoxHostNumber+tapPoolSize
}

func (network boxNetwork) tapName(index int) string {
	return network.name + "-" + strconv.Itoa(index)
}

func (network boxNetwork) boxAddress(index int) netip.Addr {
	octets := network.gateway.As4()
	octets[3] = byte(firstBoxHostNumber + index)
	return netip.AddrFrom4(octets)
}

func (network boxNetwork) unitName() string {
	return "prison-network-" + network.name + ".service"
}

func (network boxNetwork) scriptPath() string {
	return filepath.Join(networkScriptFolder, "network-"+network.name)
}

// script creates the bridge and the taps. The taps are isolated bridge
// ports, so boxes cannot reach each other.
func (network boxNetwork) script(uid, gid int) string {
	lines := []string{
		"#!/bin/sh",
		"# Written by prison. Creates the " + network.name + " network.",
		"set -eu",
		"bridge=" + network.name,
		`if ! ip link show dev "$bridge" >/dev/null 2>&1; then`,
		`  ip link add name "$bridge" type bridge`,
		"fi",
		fmt.Sprintf(`ip address replace %s/%d dev "$bridge"`,
			network.gateway, network.subnet.Bits()),
		`ip link set dev "$bridge" up`,
		fmt.Sprintf("for index in $(seq 0 %d); do", tapPoolSize-1),
		`  tap="$bridge-$index"`,
		`  if ! ip link show dev "$tap" >/dev/null 2>&1; then`,
		fmt.Sprintf(`    ip tuntap add dev "$tap" mode tap user %d group %d`,
			uid, gid),
		"  fi",
		`  ip link set dev "$tap" master "$bridge" up`,
		`  bridge link set dev "$tap" isolated on`,
		"done",
		"nft -f - <<'RULES'",
	}
	lines = append(lines, network.spoofingRules()...)
	lines = append(lines, "RULES")
	return strings.Join(lines, "\n") + "\n"
}

// spoofingRules allows each tap to send only IPv4 and ARP from its own box
// address. The rules drop IPv6 so that a box cannot reach the link-local
// address of the host.
func (network boxNetwork) spoofingRules() []string {
	table := "prison_" + strings.ReplaceAll(network.name, "-", "_")
	lines := []string{
		"table bridge " + table,
		"delete table bridge " + table,
		"table bridge " + table + " {",
		"\tchain prerouting {",
		"\t\ttype filter hook prerouting priority filter; policy accept;",
	}
	for index := range tapPoolSize {
		tap := network.tapName(index)
		address := network.boxAddress(index)
		lines = append(lines,
			fmt.Sprintf("\t\tiifname %q ip saddr != %s drop", tap, address),
			fmt.Sprintf("\t\tiifname %q arp saddr ip != %s drop", tap,
				address))
	}
	return append(lines,
		fmt.Sprintf("\t\tiifname %q ether type != { ip, arp } drop",
			network.name+"-*"),
		"\t}",
		"}",
	)
}

func (network boxNetwork) unit() string {
	return strings.Join([]string{
		"[Unit]",
		"Description=prison " + network.name + " network",
		"After=network-pre.target",
		"",
		"[Service]",
		"Type=oneshot",
		"RemainAfterExit=yes",
		"ExecStart=" + network.scriptPath(),
		"",
		"[Install]",
		"WantedBy=multi-user.target",
		"",
	}, "\n")
}

func (driver *Driver) Network(
	ctx context.Context, name string,
) (cage.NetworkInfo, error) {
	network, err := networkFor(name)
	if err != nil {
		return cage.NetworkInfo{}, err
	}
	return cage.NetworkInfo{
		Name:     name,
		Exists:   driver.networkIsReady(network),
		HostOnly: true,
		Gateway:  network.gateway.String(),
		SubnetV4: network.subnet.String(),
	}, nil
}

func (driver *Driver) networkIsReady(network boxNetwork) bool {
	if !driver.fileExists(filepath.Join(driver.sysfsNet, network.name,
		"bridge")) {
		return false
	}
	owner := strconv.Itoa(driver.uid)
	for index := range tapPoolSize {
		tap := filepath.Join(driver.sysfsNet, network.tapName(index))
		content, err := os.ReadFile(filepath.Join(tap, "owner"))
		if err != nil || strings.TrimSpace(string(content)) != owner {
			return false
		}
		master, err := os.Readlink(filepath.Join(tap, "master"))
		if err != nil || filepath.Base(master) != network.name {
			return false
		}
	}
	return true
}

// nfsHelperBinary is a root-owned copy of prison because root must not run
// a binary that the user can change.
const nfsHelperBinary = networkScriptFolder + "/prison"

func (driver *Driver) nfsHelperName() string {
	return "prison-nfs-" + strconv.Itoa(driver.uid)
}

func (driver *Driver) nfsSocketUnit() string {
	return strings.Join([]string{
		"[Unit]",
		"Description=prison NFS helper socket for user " +
			strconv.Itoa(driver.uid),
		"",
		"[Socket]",
		"ListenStream=" + nfsexport.SocketPath(driver.uid),
		"SocketUser=" + strconv.Itoa(driver.uid),
		"SocketMode=0600",
		"DirectoryMode=0755",
		"",
		"[Install]",
		"WantedBy=sockets.target",
		"",
	}, "\n")
}

func (driver *Driver) nfsServiceUnit() string {
	return strings.Join([]string{
		"[Unit]",
		"Description=prison NFS helper for user " + strconv.Itoa(driver.uid),
		"Requires=" + driver.nfsHelperName() + ".socket",
		"After=nfs-server.service",
		"",
		"[Service]",
		fmt.Sprintf("ExecStart=%s host nfs-helper --uid %d --gid %d",
			nfsHelperBinary, driver.uid, driver.gid),
		"",
	}, "\n")
}

// nfsHelperIsCurrent compares the installed helper with this binary so that
// an upgrade of prison installs the helper again.
func (driver *Driver) nfsHelperIsCurrent(ctx context.Context) bool {
	source, err := driver.helperSource()
	if err != nil || !sameContent(source, nfsHelperBinary) {
		return false
	}
	ping, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return nfsexport.Ask(ping, nfsexport.SocketPath(driver.uid),
		nfsexport.Request{Operation: nfsexport.OperationPing}) == nil
}

func sameContent(first, second string) bool {
	firstDigest, err := fileDigest(first)
	if err != nil {
		return false
	}
	secondDigest, err := fileDigest(second)
	return err == nil && firstDigest == secondDigest
}

func fileDigest(path string) ([sha256.Size]byte, error) {
	var digest [sha256.Size]byte
	file, err := os.Open(path)
	if err != nil {
		return digest, err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return digest, err
	}
	copy(digest[:], hash.Sum(nil))
	return digest, nil
}

func (driver *Driver) EnsureNetwork(ctx context.Context, name string) error {
	if err := driver.requireStateDirectory(); err != nil {
		return err
	}
	network, err := networkFor(name)
	if err != nil {
		return err
	}
	if driver.networkIsReady(network) && driver.nfsHelperIsCurrent(ctx) {
		return nil
	}
	if _, err := driver.lookPath("exportfs"); err != nil &&
		!driver.fileExists("/usr/sbin/exportfs") {
		return errors.New("the NFS server is not installed. Install " +
			"`nfs-kernel-server`")
	}
	if _, err := driver.lookPath("nft"); err != nil &&
		!driver.fileExists("/usr/sbin/nft") {
		return errors.New("`nft` is not installed. Install `nftables`")
	}
	helper, err := driver.helperSource()
	if err != nil {
		return fmt.Errorf("cannot find the prison binary: %w", err)
	}
	staging := filepath.Join(driver.stateDirectory, "network", name)
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return fmt.Errorf("cannot create %s: %w", staging, err)
	}
	script := filepath.Join(staging, "network")
	unit := filepath.Join(staging, network.unitName())
	helperName := driver.nfsHelperName()
	socketUnit := filepath.Join(staging, helperName+".socket")
	serviceUnit := filepath.Join(staging, helperName+".service")
	files := map[string]string{
		script:      network.script(driver.uid, driver.gid),
		unit:        network.unit(),
		socketUnit:  driver.nfsSocketUnit(),
		serviceUnit: driver.nfsServiceUnit(),
	}
	for path, content := range files {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return fmt.Errorf("cannot write %s: %w", path, err)
		}
	}
	ui.Progress("setting up the %s network, its firewall, and the NFS "+
		"helper with sudo", name)
	steps := [][]string{
		{"install", "-d", "-m", "0755", networkScriptFolder},
		{"install", "-m", "0755", script, network.scriptPath()},
		{"install", "-m", "0755", "-o", "root", "-g", "root", helper,
			nfsHelperBinary},
		{"install", "-m", "0644", unit,
			filepath.Join(systemdUnitFolder, network.unitName())},
		{"install", "-m", "0644", socketUnit,
			filepath.Join(systemdUnitFolder, helperName+".socket")},
		{"install", "-m", "0644", serviceUnit,
			filepath.Join(systemdUnitFolder, helperName+".service")},
		{"systemctl", "daemon-reload"},
		{"systemctl", "enable", network.unitName()},
		{"systemctl", "restart", network.unitName()},
		{"systemctl", "enable", "--now", "nfs-server.service"},
		{"systemctl", "stop", helperName + ".service"},
		{"systemctl", "enable", helperName + ".socket"},
		{"systemctl", "restart", helperName + ".socket"},
	}
	for _, step := range steps {
		if err := driver.runAsRoot(ctx, step); err != nil {
			return err
		}
	}
	if !driver.networkIsReady(network) {
		return fmt.Errorf("the %s network is not ready. Read "+
			"`journalctl -u %s`", name, network.unitName())
	}
	if !driver.nfsHelperIsCurrent(ctx) {
		return fmt.Errorf("the NFS helper does not answer. Read "+
			"`journalctl -u %s`", helperName)
	}
	return nil
}

func (driver *Driver) runAsRoot(ctx context.Context, step []string) error {
	status, err := driver.run(ctx, hostCommand{
		name:      "sudo",
		arguments: step,
		stdin:     os.Stdin,
		stdout:    os.Stderr,
		stderr:    os.Stderr,
	})
	if err != nil {
		return fmt.Errorf("cannot run sudo: %w", err)
	}
	if status != 0 {
		return errors.New("`sudo " + strings.Join(step, " ") + "` failed")
	}
	return nil
}
