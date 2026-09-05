package textdiff

import (
	"slices"
	"testing"
)

func TestSplitAndJoin(t *testing.T) {
	cases := []struct {
		text     string
		lines    []string
		trailing bool
	}{
		{"", nil, false},
		{"\n", []string{""}, true},
		{"a", []string{"a"}, false},
		{"a\n", []string{"a"}, true},
		{"a\nb", []string{"a", "b"}, false},
		{"a\n\nb\n", []string{"a", "", "b"}, true},
		{"\n\n", []string{"", ""}, true},
	}
	for _, tc := range cases {
		lines, trailing := Split(tc.text)
		if !slices.Equal(lines, tc.lines) || trailing != tc.trailing {
			t.Errorf("Split(%q) = %q, %v; want %q, %v",
				tc.text, lines, trailing, tc.lines, tc.trailing)
		}
		if joined := Join(lines, trailing); joined != tc.text {
			t.Errorf("Join(Split(%q)) = %q", tc.text, joined)
		}
	}
	if joined := Join(nil, true); joined != "" {
		t.Errorf("Join(nil, true) = %q, want empty", joined)
	}
}
