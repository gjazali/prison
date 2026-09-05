package applecontainer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestNetmaskPrefixLength checks conversion of hex netmasks to
// prefix lengths, including invalid inputs.
func TestNetmaskPrefixLength(t *testing.T) {
	cases := []struct {
		netmask string
		want    int
		fails   bool
	}{
		{netmask: "0xffffff00", want: 24},
		{netmask: "0xff000000", want: 8},
		{netmask: "0xfffff000", want: 20},
		{netmask: "0xffffffff", want: 32},
		{netmask: "0x00000000", want: 0},
		{netmask: "ffffff00", want: 24},
		{netmask: "0xnope", fails: true},
	}
	for _, testCase := range cases {
		got, err := netmaskPrefixLength(testCase.netmask)
		if testCase.fails {
			if err == nil {
				t.Errorf("%s was accepted as /%d",
					testCase.netmask, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", testCase.netmask, err)
			continue
		}
		if got != testCase.want {
			t.Errorf("%s is /%d, want /%d",
				testCase.netmask, got, testCase.want)
		}
	}
}

// TestNetworkAddressFor checks network address masking at various
// prefix lengths, including invalid inputs.
func TestNetworkAddressFor(t *testing.T) {
	cases := []struct {
		address string
		prefix  int
		want    string
		fails   bool
	}{
		{address: "192.168.128.1", prefix: 24, want: "192.168.128.0"},
		{address: "192.168.64.1", prefix: 24, want: "192.168.64.0"},
		{address: "10.1.2.3", prefix: 8, want: "10.0.0.0"},
		{address: "10.67.53.2", prefix: 20, want: "10.67.48.0"},
		{address: "192.168.128.130", prefix: 25, want: "192.168.128.128"},
		{address: "192.168.128.1", prefix: 32, want: "192.168.128.1"},
		{address: "192.168.128.1", prefix: 0, want: "0.0.0.0"},
		{address: "192.168.128", prefix: 24, fails: true},
		{address: "192.168.128.1", prefix: 33, fails: true},
		{address: "192.168.128.x", prefix: 24, fails: true},
	}
	for _, testCase := range cases {
		got, err := networkAddressFor(testCase.address, testCase.prefix)
		if testCase.fails {
			if err == nil {
				t.Errorf("%s/%d was accepted as %s",
					testCase.address, testCase.prefix, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s/%d: %v",
				testCase.address, testCase.prefix, err)
			continue
		}
		if got != testCase.want {
			t.Errorf("%s/%d is %s, want %s", testCase.address,
				testCase.prefix, got, testCase.want)
		}
	}
}

// TestInterfaceCarrying finds the bridge interface carrying the
// gateway address in recorded ifconfig output.
func TestInterfaceCarrying(t *testing.T) {
	output := readFixture(t, "ifconfig.txt")
	name, netmask := interfaceCarrying(output, "192.168.128.1")
	if name != "bridge101" || netmask != "0xffffff00" {
		t.Fatalf("gateway is on %q with netmask %q", name, netmask)
	}
	name, netmask = interfaceCarrying(output, "127.0.0.1")
	if name != "lo0" || netmask != "0xff000000" {
		t.Errorf("loopback is on %q with netmask %q", name, netmask)
	}
	if name, _ := interfaceCarrying(output, "10.9.9.9"); name != "" {
		t.Errorf("an address no interface holds read as %q", name)
	}
}

// TestRouteHelperOnFixtures checks Describe, Installed, and Command
// against recorded ifconfig and route output.
func TestRouteHelperOnFixtures(t *testing.T) {
	runner := newFixtureRunner()
	runner.answerFile(t, "ifconfig", "ifconfig.txt")
	runner.answerFile(t, "route -n get 192.168.128.2", "route-get.txt")
	helper := newTestDriver(runner).Route()

	route, err := helper.Describe(context.Background(), "192.168.128.1")
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if route == nil {
		t.Fatal("the gateway's route was not described")
	}
	if route.Network != "192.168.128.0" || route.Prefix != 24 ||
		route.Interface != "bridge101" {
		t.Errorf("route is %+v", *route)
	}

	installed, err := helper.Installed(
		context.Background(), "192.168.128.1")
	if err != nil {
		t.Fatalf("Installed: %v", err)
	}
	if !installed {
		t.Error("the recorded route reads as missing")
	}

	command, err := helper.Command(
		context.Background(), "192.168.128.1")
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	want := "sudo route -n add -net 192.168.128.0/24 " +
		"-interface bridge101"
	if command != want {
		t.Errorf("command is %q, want %q", command, want)
	}
}

// TestRouteHelperWithoutABridge checks that a gateway on no
// interface returns nil from Describe and needs no repair.
func TestRouteHelperWithoutABridge(t *testing.T) {
	runner := newFixtureRunner()
	runner.answerFile(t, "ifconfig", "ifconfig.txt")
	helper := newTestDriver(runner).Route()

	route, err := helper.Describe(context.Background(), "192.168.200.1")
	if err != nil || route != nil {
		t.Fatalf("Describe returned %v, %v", route, err)
	}
	installed, err := helper.Installed(
		context.Background(), "192.168.200.1")
	if err != nil || !installed {
		t.Errorf("Installed returned %v, %v", installed, err)
	}
	command, err := helper.Command(
		context.Background(), "192.168.200.1")
	if err != nil || command != "" {
		t.Errorf("Command returned %q, %v", command, err)
	}
	if err := helper.Install(
		context.Background(), "192.168.200.1"); err == nil {
		t.Error("Install accepted a gateway on no interface")
	}
}

// TestRouteInstallBuildsExpectedArgv pins the `sudo route` command
// that Install runs.
func TestRouteInstallBuildsExpectedArgv(t *testing.T) {
	runner := newFixtureRunner()
	runner.answerFile(t, "ifconfig", "ifconfig.txt")
	want := "sudo route -n add -net 192.168.128.0/24 " +
		"-interface bridge101"
	runner.answer(want, fixtureResponse{})
	err := newTestDriver(runner).Route().Install(
		context.Background(), "192.168.128.1")
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if !containsCall(runner.calls, want) {
		t.Errorf("calls were %v", runner.calls)
	}
}

// readFixture reads a file from the testdata directory.
func readFixture(t *testing.T, name string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("reading fixture %s: %v", name, err)
	}
	return string(content)
}
