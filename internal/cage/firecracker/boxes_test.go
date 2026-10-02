package firecracker

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestNetworkFor(t *testing.T) {
	network, err := networkFor("prison")
	if err != nil {
		t.Fatalf("networkFor = %v, want nil", err)
	}
	octets := network.gateway.As4()
	if octets[0] != 192 || octets[1] != 168 || octets[2] < 100 ||
		octets[2] > 199 || octets[3] != 1 {
		t.Errorf("gateway = %s, want 192.168.1xx.1", network.gateway)
	}
	again, _ := networkFor("prison")
	if again.subnet != network.subnet {
		t.Errorf("subnet = %s then %s, want the same", network.subnet,
			again.subnet)
	}
	if got := network.boxAddress(3).As4()[3]; got != 13 {
		t.Errorf("boxAddress(3) ends in %d, want 13", got)
	}
	for _, name := range []string{"Prison", "a-very-long-name", "", "9x"} {
		if _, err := networkFor(name); err == nil {
			t.Errorf("networkFor(%q) = nil error, want error", name)
		}
	}
}

func TestNetworkScript(t *testing.T) {
	network, _ := networkFor("prison")
	script := network.script(501, 1000)
	for _, want := range []string{
		"ip link add name \"$bridge\" type bridge",
		"ip address replace " + network.gateway.String() + "/24",
		"for index in $(seq 0 15)",
		"mode tap user 501 group 1000",
		"bridge link set dev \"$tap\" isolated on",
		"table bridge prison_prison {",
		`iifname "prison-3" ip saddr != ` + network.boxAddress(3).String() +
			" drop",
		`iifname "prison-*" ether type != { ip, arp } drop`,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("script = %q, want %q", script, want)
		}
	}
	if !strings.Contains(network.unit(), "ExecStart="+network.scriptPath()) {
		t.Errorf("unit = %q, want the script path", network.unit())
	}
}

func TestNetworkIsReady(t *testing.T) {
	driver := New(t.TempDir(), nil)
	driver.sysfsNet = t.TempDir()
	driver.uid = 501
	network, _ := networkFor("prison")
	if driver.networkIsReady(network) {
		t.Fatal("networkIsReady = true with no bridge, want false")
	}
	writeFile(t, filepath.Join(driver.sysfsNet, "prison", "bridge",
		"stp_state"), "0")
	for index := range tapPoolSize {
		tap := filepath.Join(driver.sysfsNet, network.tapName(index))
		writeFile(t, filepath.Join(tap, "owner"), "501\n")
		master := filepath.Join(tap, "master")
		if err := os.Symlink("../prison", master); err != nil {
			t.Fatal(err)
		}
	}
	if !driver.networkIsReady(network) {
		t.Error("networkIsReady = false, want true")
	}
	writeFile(t, filepath.Join(driver.sysfsNet, network.tapName(4), "owner"),
		"0\n")
	if driver.networkIsReady(network) {
		t.Error("networkIsReady = true with a root tap, want false")
	}
}

func TestBootArguments(t *testing.T) {
	network, _ := networkFor("prison")
	record := boxRecord{Name: "prison-demo", TapIndex: 2}
	arguments := bootArguments(record, network)
	address := network.boxAddress(2).String()
	for _, want := range []string{
		"ip=" + address + "::" + network.gateway.String() +
			":255.255.255.0:prison-demo:eth0:off",
		"init=/usr/local/bin/prison-guest",
	} {
		if !strings.Contains(arguments, want) {
			t.Errorf("bootArguments = %q, want %q", arguments, want)
		}
	}
	if !strings.HasSuffix(arguments, " -- init --machine") {
		t.Errorf("bootArguments = %q, want init arguments last", arguments)
	}
}

func TestMemoryInMiB(t *testing.T) {
	cases := map[string]int{"": 2048, "512M": 512, "8G": 8192, "2gb": 2048}
	for size, want := range cases {
		got, err := memoryInMiB(size)
		if err != nil || got != want {
			t.Errorf("memoryInMiB(%q) = %d, %v, want %d", size, got, err, want)
		}
	}
	for _, size := range []string{"G", "12", "-1G", "1.5G"} {
		if _, err := memoryInMiB(size); err == nil {
			t.Errorf("memoryInMiB(%q) = nil error, want error", size)
		}
	}
}

func TestProcessOfIgnoresReusedProcessIDs(t *testing.T) {
	driver := New(t.TempDir(), nil)
	driver.procfs = t.TempDir()
	directory := driver.boxDirectory("prison-demo")
	writeFile(t, filepath.Join(directory, processFile), "4242\n")
	commandLine := filepath.Join(driver.procfs, "4242", "cmdline")
	writeFile(t, commandLine, "sleep\x00infinity\x00")
	if pid := driver.processOf("prison-demo"); pid != 0 {
		t.Errorf("processOf = %d for another program, want 0", pid)
	}
	socket := filepath.Join(directory, apiSocketFile)
	writeFile(t, commandLine, "firecracker\x00--api-sock\x00"+socket+"\x00")
	if pid := driver.processOf("prison-demo"); pid != 4242 {
		t.Errorf("processOf = %d, want 4242", pid)
	}
}

func TestFreeTapIndexSkipsUsedTaps(t *testing.T) {
	driver := New(t.TempDir(), nil)
	driver.procfs = t.TempDir()
	network, _ := networkFor("prison")
	for index, name := range []string{"one", "two"} {
		record := `{"name":"` + name + `","network":"prison","tap_index":` +
			strconv.Itoa(index) + `}`
		writeFile(t, filepath.Join(driver.boxDirectory(name), boxRecordFile),
			record)
	}
	index, err := driver.freeTapIndex(network)
	if err != nil || index != 2 {
		t.Errorf("freeTapIndex = %d, %v, want 2", index, err)
	}
}
