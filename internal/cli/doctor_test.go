package cli

import (
	"strings"
	"testing"

	"prison/internal/broker/control"
)

func TestDoctorEnvironmentNamesKeepsNamesOnly(t *testing.T) {
	names := doctorEnvironmentNames([]string{
		"PATH=/usr/bin",
		"PRISON_ROOT=/Users/you/.prison",
		"HOME=/Users/you",
		"PRISON_DOMAIN=cells",
		"PRISONER=not ours",
	})
	want := []string{"PRISON_DOMAIN", "PRISON_ROOT"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("doctorEnvironmentNames = %v, want %v", names, want)
	}
	for _, name := range names {
		if strings.ContainsAny(name, "=/") {
			t.Errorf("doctorEnvironmentNames name = %q, want no value", name)
		}
	}
}

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
			t.Errorf("doctorShortenHome(%q, %q) = %q, want %q",
				testCase.path, testCase.home, got, testCase.want)
		}
	}
}

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
			t.Errorf("doctorListenerText = %q, want %q", got, testCase.want)
		}
	}
}

func TestDoctorIndentLeavesBlankLinesBlank(t *testing.T) {
	var out strings.Builder
	doctorIndent(&out, "container  /usr/bin/container\n\nversion\n  1.2\n", "  ")
	want := "  container  /usr/bin/container\n\n  version\n    1.2\n"
	if out.String() != want {
		t.Errorf("doctorIndent = %q, want %q", out.String(), want)
	}
}
