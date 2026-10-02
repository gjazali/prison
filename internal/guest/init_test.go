package guest

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestReadInitConfiguration(t *testing.T) {
	environment := func(values map[string]string) func(string) (string, bool) {
		return func(name string) (string, bool) {
			value, ok := values[name]
			return value, ok
		}
	}
	got, err := readInitConfiguration([]string{"sleep", "infinity"},
		environment(map[string]string{"PRISON_TOKEN": "t"}))
	if err != nil {
		t.Fatal(err)
	}
	want := initConfiguration{token: "t", brokerPort: 8787,
		command: []string{"sleep", "infinity"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("readInitConfiguration() = %+v, want %+v", got, want)
	}
	got, err = readInitConfiguration([]string{"bash"},
		environment(map[string]string{"PRISON_TOKEN": "t",
			"PRISON_BROKER_PORT": "18787", "PRISON_SUDO": "yes",
			"PRISON_PERSIST_PATHS": "/home/dev:/workspace/.venv:"}))
	if err != nil || got.brokerPort != 18787 || !got.sudo ||
		!reflect.DeepEqual(got.persistPaths,
			[]string{"/home/dev", "/workspace/.venv"}) {
		t.Fatalf("readInitConfiguration(overrides) = %+v, %v, want port "+
			"18787, sudo, and two paths", got, err)
	}
	if _, err := readInitConfiguration(nil,
		environment(map[string]string{"PRISON_TOKEN": "t"})); err == nil {
		t.Fatal("readInitConfiguration(nil) = nil, want an error")
	}
	if _, err := readInitConfiguration([]string{"sh"},
		environment(map[string]string{})); err == nil {
		t.Fatal("readInitConfiguration(no token) = nil, want an error")
	}
	if _, err := readInitConfiguration([]string{"sh"},
		environment(map[string]string{"PRISON_TOKEN": "t",
			"PRISON_BROKER_PORT": "http"})); err == nil {
		t.Fatal("readInitConfiguration(bad port) = nil, want an error")
	}
}

func TestChildEnvironment(t *testing.T) {
	got := childEnvironment([]string{"PATH=/bin", "PRISON_TOKEN=secret",
		"HOME=/root", "PRISON_SUDO=yes", "TERM=xterm"},
		devIdentity{uid: 501, gid: 501, home: "/home/dev"})
	want := []string{"PATH=/bin", "PRISON_SUDO=yes", "TERM=xterm",
		"HOME=/home/dev", "USER=dev", "LOGNAME=dev"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("childEnvironment() = %q, want %q", got, want)
	}
	for _, entry := range got {
		if strings.Contains(entry, "secret") {
			t.Fatalf("childEnvironment() entry = %q, want no token", entry)
		}
	}
}

func TestParsePasswdEntry(t *testing.T) {
	passwd := "root:x:0:0:root:/root:/bin/bash\n" +
		"dev:x:1000:1001::/home/dev:/bin/bash\n"
	identity, ok := parsePasswdEntry(passwd, "dev")
	if !ok || identity != (devIdentity{uid: 1000, gid: 1001, home: "/home/dev"}) {
		t.Fatalf("parsePasswdEntry(dev) = %+v, %v, want 1000, 1001", identity, ok)
	}
	if _, ok := parsePasswdEntry(passwd, "nobody"); ok {
		t.Fatal("parsePasswdEntry(nobody) = found, want not found")
	}
	if _, ok := parsePasswdEntry("dev:x:abc:501::/home/dev:/bin/sh\n",
		"dev"); ok {
		t.Fatal("parsePasswdEntry(bad uid) = found, want not found")
	}
}

func TestGatewayParsing(t *testing.T) {
	address, ok := parseIPRouteDefault(
		"default via 192.168.128.1 dev eth0 proto dhcp src 192.168.128.2\n")
	if !ok || address.String() != "192.168.128.1" {
		t.Fatalf("parseIPRouteDefault() = %v, %v, want 192.168.128.1", address, ok)
	}
	if _, ok := parseIPRouteDefault("192.168.128.0/24 dev eth0\n"); ok {
		t.Fatal("parseIPRouteDefault(no default) = found, want not found")
	}
	procRoute := "Iface\tDestination\tGateway\tFlags\n" +
		"eth0\t0080A8C0\t00000000\t0001\n" +
		"eth0\t00000000\t0180A8C0\t0003\n"
	address, err := parseProcNetRoute(procRoute)
	if err != nil || address.String() != "192.168.128.1" {
		t.Fatalf("parseProcNetRoute() = %v, %v, want 192.168.128.1", address, err)
	}
}

func TestResolvConf(t *testing.T) {
	content := resolvConfContent()
	want := "# written by prison\nnameserver 127.0.0.53\noptions ndots:1\n"
	if content != want {
		t.Fatalf("resolvConfContent() = %q, want %q", content, want)
	}
	if !resolvConfClaimed(content) {
		t.Fatal("resolvConfClaimed(guest content) = false, want true")
	}
	if resolvConfClaimed("nameserver 192.168.64.1\n") {
		t.Fatal("resolvConfClaimed(runtime content) = true, want false")
	}
	path := filepath.Join(t.TempDir(), "resolv.conf")
	_ = os.WriteFile(path, []byte("nameserver 1.1.1.1\n"), 0o644)
	if err := checkReady(path, "127.0.0.1:1", readyTimeout); err == nil {
		t.Fatal("checkReady(unclaimed) = nil, want an error")
	}
}
