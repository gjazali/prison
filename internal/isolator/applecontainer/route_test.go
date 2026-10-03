package applecontainer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

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
				t.Errorf("netmaskPrefixLength(%s) = %d, want error",
					testCase.netmask, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", testCase.netmask, err)
			continue
		}
		if got != testCase.want {
			t.Errorf("netmaskPrefixLength(%s) = %d, want %d",
				testCase.netmask, got, testCase.want)
		}
	}
}

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
				t.Errorf("networkAddressFor(%s, %d) = %s, want error",
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
			t.Errorf("networkAddressFor(%s, %d) = %s, want %s",
				testCase.address,
				testCase.prefix, got, testCase.want)
		}
	}
}

func TestInterfaceCarrying(t *testing.T) {
	output := readFixture(t, "ifconfig.txt")
	name, netmask := interfaceCarrying(output, "192.168.128.1")
	if name != "bridge101" || netmask != "0xffffff00" {
		t.Fatalf("interfaceCarrying = %q, %q, want bridge101, 0xffffff00",
			name, netmask)
	}
	name, netmask = interfaceCarrying(output, "127.0.0.1")
	if name != "lo0" || netmask != "0xff000000" {
		t.Errorf("interfaceCarrying = %q, %q, want lo0, 0xff000000",
			name, netmask)
	}
	if name, _ := interfaceCarrying(output, "10.9.9.9"); name != "" {
		t.Errorf("interfaceCarrying = %q, want empty", name)
	}
}

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
		t.Fatal("Describe = nil, want a route")
	}
	if route.Network != "192.168.128.0" || route.Prefix != 24 ||
		route.Interface != "bridge101" {
		t.Errorf("Describe = %+v, want 192.168.128.0/24 on bridge101", *route)
	}

	installed, err := helper.Installed(
		context.Background(), "192.168.128.1")
	if err != nil {
		t.Fatalf("Installed: %v", err)
	}
	if !installed {
		t.Error("Installed = false, want true")
	}

	command, err := helper.Command(
		context.Background(), "192.168.128.1")
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	want := "sudo route -n add -net 192.168.128.0/24 " +
		"-interface bridge101"
	if command != want {
		t.Errorf("Command = %q, want %q", command, want)
	}
}

func TestRouteHelperWithoutABridge(t *testing.T) {
	runner := newFixtureRunner()
	runner.answerFile(t, "ifconfig", "ifconfig.txt")
	helper := newTestDriver(runner).Route()

	route, err := helper.Describe(context.Background(), "192.168.200.1")
	if err != nil || route != nil {
		t.Fatalf("Describe = %v, %v, want nil", route, err)
	}
	installed, err := helper.Installed(
		context.Background(), "192.168.200.1")
	if err != nil || !installed {
		t.Errorf("Installed = %v, %v, want true", installed, err)
	}
	command, err := helper.Command(
		context.Background(), "192.168.200.1")
	if err != nil || command != "" {
		t.Errorf("Command = %q, %v, want empty", command, err)
	}
	if err := helper.Install(
		context.Background(), "192.168.200.1"); err == nil {
		t.Error("Install = nil, want error")
	}
}

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
		t.Errorf("calls = %v, want %s", runner.calls, want)
	}
}

func readFixture(t *testing.T, name string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("reading fixture %s: %v", name, err)
	}
	return string(content)
}
