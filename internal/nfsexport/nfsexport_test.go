package nfsexport

import (
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testPolicy() Policy {
	return Policy{
		UID: 501,
		GID: 1000,
		AllowClient: func(address netip.Addr) bool {
			return netip.MustParsePrefix("192.168.150.0/24").Contains(address)
		},
	}
}

func ownedBy(owner int) func(string) (int, bool, error) {
	return func(string) (int, bool, error) { return owner, true, nil }
}

func TestExportTargetResolvesSymlinks(t *testing.T) {
	folder := t.TempDir()
	real := filepath.Join(folder, "project")
	link := filepath.Join(folder, "link")
	os.Mkdir(real, 0o755)
	os.Symlink(real, link)
	target, err := testPolicy().exportTarget(Request{
		Path: link, Client: "192.168.150.10"}, ownedBy(501))
	resolved, _ := filepath.EvalSymlinks(real)
	if err != nil || target != "192.168.150.10:"+resolved {
		t.Errorf("exportTarget = %q, %v, want the resolved folder", target,
			err)
	}
}

func TestExportTargetRefusals(t *testing.T) {
	folder := t.TempDir()
	cases := []struct {
		name    string
		request Request
		ownerOf func(string) (int, bool, error)
		want    string
	}{
		{"other owner", Request{Path: folder, Client: "192.168.150.10"},
			ownedBy(0), "does not belong"},
		{"outside the boxes", Request{Path: folder, Client: "10.0.0.5"},
			ownedBy(501), "not a box address"},
		{"not an address", Request{Path: folder, Client: "*"},
			ownedBy(501), "not a box address"},
		{"relative path", Request{Path: "project", Client: "192.168.150.10"},
			ownedBy(501), "not absolute"},
		{"root folder", Request{Path: "/", Client: "192.168.150.10"},
			ownedBy(501), "root folder"},
		{"missing folder", Request{Path: filepath.Join(folder, "none"),
			Client: "192.168.150.10"}, ownedBy(501), "cannot resolve"},
		{"file", Request{Path: folder, Client: "192.168.150.10"},
			func(string) (int, bool, error) { return 501, false, nil },
			"not a folder"},
	}
	for _, c := range cases {
		_, err := testPolicy().exportTarget(c.request, c.ownerOf)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: exportTarget = %v, want %q", c.name, err, c.want)
		}
	}
}

func TestExportOptionsSquashEveryUser(t *testing.T) {
	options := testPolicy().exportOptions(true)
	for _, want := range []string{"ro,", "all_squash", "anonuid=501",
		"anongid=1000"} {
		if !strings.Contains(options, want) {
			t.Errorf("options = %q, want %q", options, want)
		}
	}
	if options := testPolicy().exportOptions(false); !strings.HasPrefix(
		options, "rw,") {
		t.Errorf("exportOptions(false) = %q, want prefix \"rw,\"", options)
	}
}

func TestUnexportTargetChecksTheClient(t *testing.T) {
	if _, err := testPolicy().unexportTarget(Request{Path: "/gone",
		Client: "10.0.0.5"}); err == nil {
		t.Error("unexportTarget(10.0.0.5) = nil error, want an error")
	}
	target, err := testPolicy().unexportTarget(Request{Path: "/gone/./x",
		Client: "192.168.150.12"})
	if err != nil || target != "192.168.150.12:/gone/x" {
		t.Errorf("unexportTarget = %q, %v, want \"192.168.150.12:/gone/x\"",
			target, err)
	}
}
