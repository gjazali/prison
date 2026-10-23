package applecontainer

import (
	"context"
	"fmt"
	"net"
	"testing"
)

func fakeHostInterfaces(t *testing.T) {
	t.Helper()
	parse := func(cidr string) net.Addr {
		ip, network, err := net.ParseCIDR(cidr)
		if err != nil {
			t.Fatal(err)
		}
		network.IP = ip
		return network
	}
	original := hostInterfaceAddresses
	t.Cleanup(func() { hostInterfaceAddresses = original })
	hostInterfaceAddresses = func() (map[string][]net.Addr, error) {
		return map[string][]net.Addr{
			"lo0": {parse("127.0.0.1/8"), parse("::1/128")},
			"en0": {parse("fe80::1/64"), parse("10.0.0.2/20")},
			"bridge101": {
				parse("192.168.128.1/24"), parse("fd53::1/64"),
			},
		}, nil
	}
}

func TestRouteCarrying(t *testing.T) {
	fakeHostInterfaces(t)
	addresses, _ := hostInterfaceAddresses()
	cases := []struct {
		gateway string
		want    string
	}{
		{gateway: "192.168.128.1", want: "192.168.128.0/24 bridge101"},
		{gateway: "10.0.0.2", want: "10.0.0.0/20 en0"},
		{gateway: "127.0.0.1", want: "127.0.0.0/8 lo0"},
		{gateway: "10.9.9.9", want: ""},
		{gateway: "fd53::1", want: ""},
		{gateway: "nope", want: ""},
	}
	for _, testCase := range cases {
		got := ""
		if route := routeCarrying(testCase.gateway, addresses); route != nil {
			got = fmt.Sprintf("%s/%d %s",
				route.Network, route.Prefix, route.Interface)
		}
		if got != testCase.want {
			t.Errorf("routeCarrying(%s) = %q, want %q",
				testCase.gateway, got, testCase.want)
		}
	}
}

func TestRouteHelperOnFixtures(t *testing.T) {
	runner := newFixtureRunner()
	fakeHostInterfaces(t)
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
	fakeHostInterfaces(t)
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
	fakeHostInterfaces(t)
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
