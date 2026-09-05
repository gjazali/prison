package cli

import (
	"strings"
	"testing"

	"prison/internal/broker/control"
)

// TestDoctorEnvironmentNamesKeepsNamesOnly checks that only sorted
// PRISON_* variable names are returned, with no values.
func TestDoctorEnvironmentNamesKeepsNamesOnly(t *testing.T) {
	names := doctorEnvironmentNames([]string{
		"PATH=/usr/bin",
		"PRISON_ROOT=/Users/you/.prison",
		"HOME=/Users/you",
		"PRISON_CAGE=apple-container",
		"PRISONER=not ours",
	})
	want := []string{"PRISON_CAGE", "PRISON_ROOT"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("names are %v, want %v", names, want)
	}
	for _, name := range names {
		if strings.ContainsAny(name, "=/") {
			t.Errorf("%q carries part of a value", name)
		}
	}
}

// TestDoctorShortenHomeRewritesOnlyTheHomePrefix checks that paths
// under the home directory get a tilde prefix and others stay
// unchanged.
func TestDoctorShortenHomeRewritesOnlyTheHomePrefix(t *testing.T) {
	cases := []struct{ path, home, want string }{
		{"/Users/you/.prison", "/Users/you", "~/.prison"},
		{"/Users/you", "/Users/you", "~"},
		{"/tmp/prison", "/Users/you", "/tmp/prison"},
		{"/Users/younger/.prison", "/Users/you", "/Users/younger/.prison"},
		{"/tmp/prison", "", "/tmp/prison"},
	}
	for _, testCase := range cases {
		got := doctorShortenHome(testCase.path, testCase.home)
		if got != testCase.want {
			t.Errorf("doctorShortenHome(%q, %q) is %q, want %q",
				testCase.path, testCase.home, got, testCase.want)
		}
	}
}

// TestDoctorListenerTextReportsBoundState checks the listener line
// for the bound, error, and unbound states.
func TestDoctorListenerTextReportsBoundState(t *testing.T) {
	cases := []struct {
		listener control.Listener
		want     string
	}{
		{
			listener: control.Listener{
				Address: "192.168.64.1", Port: 8787, Bound: true},
			want: "192.168.64.1:8787, bound",
		},
		{
			listener: control.Listener{
				Address: "192.168.64.1", Error: "address in use"},
			want: "192.168.64.1, not bound: address in use",
		},
		{
			listener: control.Listener{Address: "192.168.64.1"},
			want:     "192.168.64.1, not bound",
		},
	}
	for _, testCase := range cases {
		if got := doctorListenerText(testCase.listener); got != testCase.want {
			t.Errorf("listener line is %q, want %q", got, testCase.want)
		}
	}
}

// TestDoctorIndentLeavesBlankLinesBlank checks that indented text
// keeps blank lines blank with no trailing spaces.
func TestDoctorIndentLeavesBlankLinesBlank(t *testing.T) {
	var out strings.Builder
	doctorIndent(&out, "container  /usr/bin/container\n\nversion\n  1.2\n", "  ")
	want := "  container  /usr/bin/container\n\n  version\n    1.2\n"
	if out.String() != want {
		t.Errorf("indented text is %q, want %q", out.String(), want)
	}
}
