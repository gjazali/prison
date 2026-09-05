package checkpoint

import "testing"

// TestIgnoreMatches checks each pattern rule: bare names, rooted
// paths, trailing slashes, wildcards, and case sensitivity.
func TestIgnoreMatches(t *testing.T) {
	cases := []struct {
		patterns    []string
		path        string
		isDirectory bool
		want        bool
	}{
		{patterns: []string{".git/"}, path: ".git", isDirectory: true,
			want: true},
		{patterns: []string{".git/"}, path: ".git", isDirectory: false},
		{patterns: []string{".git/"}, path: "src/.git", isDirectory: true,
			want: true},
		{patterns: []string{".DS_Store"}, path: "a/b/.DS_Store", want: true},
		{patterns: []string{"*.pyc"}, path: "pkg/module.pyc", want: true},
		{patterns: []string{"*.pyc"}, path: "pkg/module.py"},
		{patterns: []string{"*.partial"}, path: "notes.txt.partial", want: true},
		{patterns: []string{"build/out"}, path: "build/out", want: true},
		{patterns: []string{"build/out"}, path: "src/build/out"},
		{patterns: []string{"build/*"}, path: "build/one", want: true},
		{patterns: []string{"build/*"}, path: "build/one/two", want: true},
		{patterns: []string{"src/*/generated"}, path: "src/api/generated",
			want: true},
		{patterns: []string{"src/*/generated"}, path: "src/api/deep/generated"},
		{patterns: []string{"logs/tmp/"}, path: "logs/tmp", isDirectory: true,
			want: true},
		{patterns: []string{"logs/tmp/"}, path: "logs/tmp",
			isDirectory: false},
		{patterns: []string{"Build"}, path: "build", isDirectory: true},
		{patterns: []string{"/"}, path: "anything"},
		{patterns: []string{"target/"}, path: "", isDirectory: true},
	}
	for _, testCase := range cases {
		ignore := NewIgnore(testCase.patterns)
		got := ignore.Matches(testCase.path, testCase.isDirectory)
		if got != testCase.want {
			t.Errorf("NewIgnore(%q).Matches(%q, %v) = %v, want %v",
				testCase.patterns, testCase.path, testCase.isDirectory,
				got, testCase.want)
		}
	}
}

// TestIgnoreNilExcludesNothing checks that a nil Ignore matches
// nothing.
func TestIgnoreNilExcludesNothing(t *testing.T) {
	var ignore *Ignore
	if ignore.Matches(".git", true) {
		t.Error("a nil matcher excluded a path")
	}
}

// TestDefaultIgnoresCoverTemporaryNames checks that default ignores
// exclude temporary files, compiled artifacts, and known
// directories.
func TestDefaultIgnoresCoverTemporaryNames(t *testing.T) {
	ignore := NewIgnore(DefaultIgnores)
	for _, path := range []string{
		"src/main.go.prison-partial", "src/main.go.partial", ".DS_Store",
		"pkg/thing.pyc",
	} {
		if !ignore.Matches(path, false) {
			t.Errorf("the default ignores kept %q", path)
		}
	}
	if !ignore.Matches("node_modules", true) {
		t.Error("the default ignores kept node_modules")
	}
}
