package firecracker

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func testDriver(binaryFound, deviceUsable bool) *Driver {
	driver := New("", nil)
	driver.lookPath = func(file string) (string, error) {
		if !binaryFound {
			return "", errors.New("not found")
		}
		return "/usr/local/bin/" + file, nil
	}
	driver.deviceIsUsable = func(string) bool { return deviceUsable }
	driver.runVersionCheck = func(context.Context, string) (string, error) {
		return "Firecracker v1.17.0", nil
	}
	driver.getenv = func(string) string { return "" }
	driver.fileExists = func(string) bool { return false }
	return driver
}

func TestRequire(t *testing.T) {
	cases := []struct {
		name         string
		binaryFound  bool
		deviceUsable bool
		wantMessage  string
	}{
		{"ready", true, true, ""},
		{"no binary", false, true, "`firecracker` not found"},
		{"no kvm", true, false, "cannot use /dev/kvm"},
	}
	for _, c := range cases {
		driver := testDriver(c.binaryFound, c.deviceUsable)
		err := driver.Require(context.Background())
		if c.wantMessage == "" {
			if err != nil {
				t.Errorf("%s: Require = %v, want nil", c.name, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), c.wantMessage) {
			t.Errorf("%s: Require = %v, want %q", c.name, err, c.wantMessage)
		}
	}
}

func TestDoctorReportsVersionAndDevice(t *testing.T) {
	var output bytes.Buffer
	testDriver(true, false).Doctor(context.Background(), &output)
	wants := []string{"Firecracker v1.17.0", "/dev/kvm not usable",
		"no BuildKit daemon found", "kernel       missing",
		"nfs server   installed", "nfs helper   missing"}
	for _, want := range wants {
		if !strings.Contains(output.String(), want) {
			t.Errorf("Doctor output = %q, want %q", output.String(), want)
		}
	}
}
