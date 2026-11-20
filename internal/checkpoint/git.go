package checkpoint

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

type GitState struct {
	Head string            `json:"head"`
	Refs map[string]string `json:"refs,omitempty"`
}

type GitChange struct {
	Ref    string
	Before string
	After  string
}

type GitRestoreResult struct {
	RestoredRefs int
	KeptNewRefs  []string
	HeadChanged  bool
	IndexSynced  bool
}

var gitInProgressMarkers = []string{
	"MERGE_HEAD",
	"REBASE_HEAD",
	"CHERRY_PICK_HEAD",
	"REVERT_HEAD",
	"BISECT_LOG",
	"rebase-merge",
	"rebase-apply",
}

func (state *GitState) clone() *GitState {
	if state == nil {
		return nil
	}
	copied := &GitState{Head: state.Head}
	if len(state.Refs) > 0 {
		copied.Refs = make(map[string]string, len(state.Refs))
		for name, sha := range state.Refs {
			copied.Refs[name] = sha
		}
	}
	return copied
}

// CaptureGitState reads HEAD and local refs without running host git, so that
// untrusted repo config and hooks cannot get executed.
func CaptureGitState(projectDir string) (*GitState, error) {
	gitDir, commonDir, ok, err := resolveGitDir(projectDir)
	if err != nil || !ok {
		return nil, err
	}
	headData, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("cannot read git HEAD: %w", err)
	}
	head := strings.TrimSpace(string(headData))
	if head == "" {
		return nil, nil
	}

	refs := map[string]string{}
	if err := readPackedRefs(filepath.Join(commonDir, "packed-refs"), refs); err != nil {
		return nil, err
	}
	for _, prefix := range []string{"refs/heads", "refs/tags"} {
		if err := readLooseRefs(filepath.Join(commonDir, filepath.FromSlash(prefix)), prefix, refs); err != nil {
			return nil, err
		}
	}
	if gitDir != commonDir {
		for _, prefix := range []string{"refs/heads", "refs/tags"} {
			if err := readLooseRefs(filepath.Join(gitDir, filepath.FromSlash(prefix)), prefix, refs); err != nil {
				return nil, err
			}
		}
	}
	if len(refs) == 0 {
		refs = nil
	}
	return &GitState{Head: head, Refs: refs}, nil
}

func resolveGitDir(projectDir string) (gitDir, commonDir string, ok bool, err error) {
	dotGit := filepath.Join(projectDir, ".git")
	info, err := os.Lstat(dotGit)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", "", false, nil
		}
		return "", "", false, fmt.Errorf("cannot inspect %s: %w", dotGit, err)
	}
	switch {
	case info.IsDir():
		gitDir = dotGit
	case info.Mode().IsRegular():
		data, err := os.ReadFile(dotGit)
		if err != nil {
			return "", "", false, fmt.Errorf("cannot read %s: %w", dotGit, err)
		}
		line := strings.TrimSpace(strings.SplitN(string(data), "\n", 2)[0])
		target, found := strings.CutPrefix(line, "gitdir:")
		if !found {
			return "", "", false, nil
		}
		target = strings.TrimSpace(target)
		if target == "" {
			return "", "", false, nil
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(projectDir, target)
		}
		gitDir = filepath.Clean(target)
	default:
		return "", "", false, nil
	}

	commonDir = gitDir
	if data, err := os.ReadFile(filepath.Join(gitDir, "commondir")); err == nil {
		target := strings.TrimSpace(string(data))
		if target != "" {
			if !filepath.IsAbs(target) {
				target = filepath.Join(gitDir, target)
			}
			commonDir = filepath.Clean(target)
		}
	}
	return gitDir, commonDir, true, nil
}

func readPackedRefs(path string, refs map[string]string) error {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("cannot read %s: %w", path, err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "^") {
			continue
		}
		sha, name, found := strings.Cut(line, " ")
		if !found || !isLocalGitRef(name) || !isGitObjectID(sha) {
			continue
		}
		refs[name] = sha
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("cannot read %s: %w", path, err)
	}
	return nil
}

func readLooseRefs(root, prefix string, refs map[string]string) error {
	if _, err := os.Stat(root); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, os.ErrNotExist) {
				return nil
			}
			return walkErr
		}
		if entry.IsDir() || !entry.Type().IsRegular() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		name := prefix + "/" + filepath.ToSlash(relative)
		if !isLocalGitRef(name) {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		sha := strings.TrimSpace(string(data))
		if isGitObjectID(sha) {
			refs[name] = sha
		}
		return nil
	})
}

func CompareGitState(before, after *GitState) []GitChange {
	if before == nil || after == nil {
		return nil
	}
	var changes []GitChange
	if before.Head != after.Head {
		changes = append(changes, GitChange{
			Ref:    "HEAD",
			Before: before.Head,
			After:  after.Head,
		})
	}
	seen := make(map[string]bool, len(before.Refs)+len(after.Refs))
	names := make([]string, 0, len(before.Refs)+len(after.Refs))
	for name := range before.Refs {
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	for name := range after.Refs {
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	slices.Sort(names)
	for _, name := range names {
		if before.Refs[name] != after.Refs[name] {
			changes = append(changes, GitChange{
				Ref:    name,
				Before: before.Refs[name],
				After:  after.Refs[name],
			})
		}
	}
	return changes
}

func FormatGitChange(change GitChange) string {
	switch {
	case change.Before == "":
		return fmt.Sprintf("%s added (%s)", change.Ref, shortGitValue(change.After))
	case change.After == "":
		return fmt.Sprintf("%s deleted (%s)", change.Ref, shortGitValue(change.Before))
	default:
		return fmt.Sprintf("%s moved %s -> %s",
			change.Ref, shortGitValue(change.Before), shortGitValue(change.After))
	}
}

func shortGitValue(value string) string {
	if target, ok := strings.CutPrefix(value, "ref: "); ok {
		return strings.TrimPrefix(target, "refs/heads/")
	}
	if isGitObjectID(value) && len(value) > 7 {
		return value[:7]
	}
	return value
}

func CheckGitRestorable(projectDir string, target *GitState) error {
	if target == nil {
		return errors.New("this checkpoint has no recorded git state")
	}
	if err := validateGitState(target); err != nil {
		return err
	}
	gitDir, _, ok, err := resolveGitDir(projectDir)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("this project is not a git repository")
	}
	for _, marker := range gitInProgressMarkers {
		if _, err := os.Stat(filepath.Join(gitDir, marker)); err == nil {
			return fmt.Errorf(
				"cannot restore git state while a git operation (%s) is in progress",
				marker)
		}
	}

	objects := uniqueGitObjects(target)
	if len(objects) == 0 {
		return nil
	}
	var input strings.Builder
	for _, sha := range objects {
		input.WriteString(sha)
		input.WriteByte('\n')
	}
	output, err := runSafeGit(projectDir, input.String(), "cat-file", "--batch-check")
	if err != nil {
		return fmt.Errorf("cannot verify git objects: %w", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if line == "" {
			continue
		}
		if sha, missing := strings.CutSuffix(line, " missing"); missing {
			return fmt.Errorf(
				"git object %s is no longer in the repository (it may have been garbage-collected)",
				sha)
		}
	}
	return nil
}

func RestoreGitState(projectDir string, target *GitState) (GitRestoreResult, error) {
	if err := CheckGitRestorable(projectDir, target); err != nil {
		return GitRestoreResult{}, err
	}
	current, err := CaptureGitState(projectDir)
	if err != nil {
		return GitRestoreResult{}, err
	}
	if current == nil {
		return GitRestoreResult{}, errors.New("this project is not a git repository")
	}

	var result GitRestoreResult
	names := make([]string, 0, len(target.Refs))
	for name := range target.Refs {
		names = append(names, name)
	}
	slices.Sort(names)

	var transaction strings.Builder
	transaction.WriteString("start\n")
	for _, name := range names {
		sha := target.Refs[name]
		if current.Refs[name] == sha {
			continue
		}
		fmt.Fprintf(&transaction, "update %s %s\n", name, sha)
		result.RestoredRefs++
	}
	transaction.WriteString("commit\n")

	if result.RestoredRefs > 0 {
		if _, err := runSafeGit(projectDir, transaction.String(),
			"update-ref", "--stdin", "--create-reflog"); err != nil {
			return result, fmt.Errorf("cannot restore git refs: %w", err)
		}
	}

	for name := range current.Refs {
		if _, existed := target.Refs[name]; !existed {
			result.KeptNewRefs = append(result.KeptNewRefs, name)
		}
	}
	slices.Sort(result.KeptNewRefs)

	if target.Head != current.Head {
		if branch, symbolic := strings.CutPrefix(target.Head, "ref: "); symbolic {
			if _, err := runSafeGit(projectDir, "",
				"symbolic-ref", "HEAD", branch); err != nil {
				return result, fmt.Errorf("cannot restore git HEAD: %w", err)
			}
		} else {
			if _, err := runSafeGit(projectDir, "",
				"update-ref", "--no-deref", "HEAD", target.Head); err != nil {
				return result, fmt.Errorf("cannot restore git HEAD: %w", err)
			}
		}
		result.HeadChanged = true
	}

	if headResolvesToCommit(target) {
		if _, err := runSafeGit(projectDir, "", "read-tree", "HEAD"); err != nil {
			return result, fmt.Errorf("cannot sync git index: %w", err)
		}
		result.IndexSynced = true
	} else if gitDir, _, ok, err := resolveGitDir(projectDir); err == nil && ok {
		_ = os.Remove(filepath.Join(gitDir, "index"))
		result.IndexSynced = true
	}
	return result, nil
}

func headResolvesToCommit(state *GitState) bool {
	if state == nil || state.Head == "" {
		return false
	}
	if branch, symbolic := strings.CutPrefix(state.Head, "ref: "); symbolic {
		return state.Refs[branch] != ""
	}
	return isGitObjectID(state.Head)
}

func validateGitState(state *GitState) error {
	if state.Head == "" {
		return errors.New("the recorded git HEAD is empty")
	}
	if branch, symbolic := strings.CutPrefix(state.Head, "ref: "); symbolic {
		if !isLocalGitRef(branch) {
			return fmt.Errorf("recorded git HEAD %q is not a local ref", state.Head)
		}
	} else if !isGitObjectID(state.Head) {
		return fmt.Errorf("recorded git HEAD %q is not a valid commit id", state.Head)
	}
	for name, sha := range state.Refs {
		if !isLocalGitRef(name) {
			return fmt.Errorf("recorded git ref %q is not a local ref", name)
		}
		if !isGitObjectID(sha) {
			return fmt.Errorf("recorded git ref %q has invalid object id %q", name, sha)
		}
	}
	return nil
}

func uniqueGitObjects(state *GitState) []string {
	seen := map[string]bool{}
	var objects []string
	add := func(sha string) {
		if isGitObjectID(sha) && !seen[sha] {
			seen[sha] = true
			objects = append(objects, sha)
		}
	}
	if !strings.HasPrefix(state.Head, "ref: ") {
		add(state.Head)
	}
	for _, sha := range state.Refs {
		add(sha)
	}
	slices.Sort(objects)
	return objects
}

func isLocalGitRef(name string) bool {
	if !strings.HasPrefix(name, "refs/heads/") && !strings.HasPrefix(name, "refs/tags/") {
		return false
	}
	if strings.ContainsAny(name, " \t\r\n~^:?*[\\") || strings.Contains(name, "..") ||
		strings.Contains(name, "//") || strings.HasSuffix(name, "/") ||
		strings.HasSuffix(name, ".lock") {
		return false
	}
	return true
}

func isGitObjectID(text string) bool {
	if len(text) != 40 && len(text) != 64 {
		return false
	}
	for index := range len(text) {
		c := text[index]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

func runSafeGit(projectDir, stdin string, args ...string) (string, error) {
	fullArgs := append([]string{
		"-C", projectDir,
		"-c", "core.fsmonitor=false",
		"-c", "core.hooksPath=/dev/null",
		"-c", "gc.auto=0",
	}, args...)
	command := exec.Command("git", fullArgs...)
	command.Env = safeGitEnvironment()
	if stdin != "" {
		command.Stdin = strings.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail != "" {
			return "", fmt.Errorf("git %s: %s", args[0], detail)
		}
		return "", fmt.Errorf("git %s: %w", args[0], err)
	}
	return stdout.String(), nil
}

func safeGitEnvironment() []string {
	env := os.Environ()
	hasName := false
	hasEmail := false
	for _, entry := range env {
		if strings.HasPrefix(entry, "GIT_COMMITTER_NAME=") {
			hasName = true
		}
		if strings.HasPrefix(entry, "GIT_COMMITTER_EMAIL=") {
			hasEmail = true
		}
	}
	if !hasName {
		env = append(env, "GIT_COMMITTER_NAME=prison")
	}
	if !hasEmail {
		env = append(env, "GIT_COMMITTER_EMAIL=prison@local")
	}
	return env
}
