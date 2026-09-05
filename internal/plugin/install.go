package plugin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// ErrNotApproved is returned when the user declines the inmate.
var ErrNotApproved = errors.New("the inmate was not approved, so nothing changed")

// reservedNames are prison's own commands that inmates cannot use.
var reservedNames = map[string]bool{
	"up": true, "down": true, "rm": true, "shell": true, "run": true,
	"trust": true, "checkpoint": true, "setup": true, "ports": true,
	"status": true, "list": true, "build": true, "doctor": true,
	"inmate": true, "cage": true, "broker": true, "host": true,
	"secret": true, "help": true, "version": true,
}

// Install fetches an inmate from a git repo and installs it after
// approval. Takes a context, repo URL, ref, approval function, and
// output writer. Returns the installed inmate or ErrNotApproved.
func (r *Registry) Install(
	ctx context.Context, url, ref string,
	approve func(description string) bool, out io.Writer,
) (*Inmate, error) {
	work, err := os.MkdirTemp("", "prison-inmate-")
	if err != nil {
		return nil, fmt.Errorf("cannot create a working directory: %w", err)
	}
	defer os.RemoveAll(work)
	fetched := filepath.Join(work, "plugin")
	if err := clonePlugin(ctx, url, ref, fetched); err != nil {
		return nil, err
	}
	return r.installFetched(fetched, "", url, ref, approve, out)
}

// Update re-fetches an installed inmate from its origin and replaces
// it after approval. Does nothing if the content is unchanged.
// Bundled inmates and those with no recorded origin are refused.
func (r *Registry) Update(
	ctx context.Context, name, ref string,
	approve func(description string) bool, out io.Writer,
) (*Inmate, error) {
	out = writerOrDiscard(out)
	inmate, err := r.Get(name)
	if err != nil {
		return nil, err
	}
	if inmate.Bundled {
		return nil, fmt.Errorf("the %s inmate ships with prison, so it is updated by updating prison", name)
	}
	url, recordedRef := inmate.Origin()
	if url == "" {
		return nil, fmt.Errorf("prison has no record of where the %s inmate came from, so it cannot be fetched again", name)
	}
	if ref == "" {
		ref = recordedRef
	}
	work, err := os.MkdirTemp("", "prison-inmate-")
	if err != nil {
		return nil, fmt.Errorf("cannot create a working directory: %w", err)
	}
	defer os.RemoveAll(work)
	fetched := filepath.Join(work, "plugin")
	if err := clonePlugin(ctx, url, ref, fetched); err != nil {
		return nil, err
	}
	fetchedHash, err := TreeHash(os.DirFS(fetched))
	if err != nil {
		return nil, err
	}
	installedHash, err := TreeHash(inmate.FS)
	if err != nil {
		return nil, err
	}
	if fetchedHash == installedHash {
		fmt.Fprintf(out, "the %s inmate is already up to date\n", name)
		return inmate, nil
	}
	return r.installFetched(fetched, name, url, ref, approve, out)
}

// Remove deletes an installed inmate and its trust record. Bundled
// inmates are refused.
func (r *Registry) Remove(name string) error {
	inmate, err := r.Get(name)
	if err != nil {
		return err
	}
	if inmate.Bundled {
		return fmt.Errorf("the %s inmate ships with prison and cannot be removed", name)
	}
	if err := os.RemoveAll(inmate.Dir); err != nil {
		return fmt.Errorf("cannot remove %s: %w", inmate.Dir, err)
	}
	return r.removeTrustRecord(name)
}

// Trust re-approves an inmate whose content has changed. Records
// the new hash after approval. Bundled or already-approved inmates
// are left alone.
func (r *Registry) Trust(
	name string, approve func(description string) bool, out io.Writer,
) (*Inmate, error) {
	out = writerOrDiscard(out)
	inmate, err := r.Get(name)
	if err != nil {
		return nil, err
	}
	if inmate.Bundled {
		fmt.Fprintf(out, "the %s inmate ships with prison and needs no approval\n", name)
		return inmate, nil
	}
	if inmate.Problem != nil {
		return nil, fmt.Errorf("the %s inmate cannot be approved: %w", name, inmate.Problem)
	}
	if inmate.Trusted {
		fmt.Fprintf(out, "the %s inmate is already approved\n", name)
		return inmate, nil
	}
	description, err := approvalDescription(&inmate.Manifest, inmate.FS)
	if err != nil {
		return nil, err
	}
	if approve == nil || !approve(description) {
		return nil, ErrNotApproved
	}
	hash, err := TreeHash(inmate.FS)
	if err != nil {
		return nil, err
	}
	url, ref := inmate.Origin()
	record := &TrustRecord{Hash: hash, OriginURL: url, OriginRef: ref}
	if err := r.writeTrustRecord(name, record); err != nil {
		return nil, err
	}
	fmt.Fprintf(out, "approved; prison uses the %s inmate again\n", name)
	return r.Get(name)
}

// installFetched validates, shows, and copies a fetched inmate after
// approval. Takes the source path, expected name (empty for first
// install), origin URL and ref.
func (r *Registry) installFetched(
	source, expectedName, url, ref string,
	approve func(description string) bool, out io.Writer,
) (*Inmate, error) {
	out = writerOrDiscard(out)
	if r.installDir == "" {
		return nil, errors.New("this registry has no install directory, so an inmate cannot be installed")
	}
	data, err := os.ReadFile(filepath.Join(source, ManifestFileName))
	if err != nil {
		return nil, fmt.Errorf("the repository holds no %s: %w", ManifestFileName, err)
	}
	manifest, err := ParseManifest(data)
	if err != nil {
		return nil, err
	}
	name := manifest.Inmate.Name
	if expectedName != "" && name != expectedName {
		return nil, fmt.Errorf("the repository now names the inmate %q, not %q; install it under its new name instead", name, expectedName)
	}
	if err := r.refuseUnusableName(name); err != nil {
		return nil, err
	}
	description, err := approvalDescription(manifest, os.DirFS(source))
	if err != nil {
		return nil, err
	}
	if approve == nil || !approve(description) {
		return nil, ErrNotApproved
	}
	destination := filepath.Join(r.installDir, name)
	if err := copyPluginTree(source, destination); err != nil {
		return nil, err
	}
	hash, err := TreeHash(os.DirFS(destination))
	if err != nil {
		return nil, err
	}
	record := &TrustRecord{Hash: hash, OriginURL: url, OriginRef: ref}
	if err := r.writeTrustRecord(name, record); err != nil {
		return nil, err
	}
	fmt.Fprintf(out, "installed the %s inmate at %s\n", name, destination)
	fmt.Fprintf(out, "enable it with PRISON_INMATES=%q, or in ~/.prison/config.toml\n", name)
	return r.Get(name)
}

// refuseUnusableName rejects reserved command names and names used
// by bundled inmates.
func (r *Registry) refuseUnusableName(name string) error {
	if reservedNames[name] {
		return fmt.Errorf("%q is one of prison's own commands, so an inmate cannot take it", name)
	}
	shipped, err := r.BundledNames()
	if err != nil {
		return err
	}
	for _, bundled := range shipped {
		if bundled == name {
			return fmt.Errorf("prison ships an inmate called %q, and a shipped inmate cannot be shadowed", name)
		}
	}
	return nil
}

// clonePlugin shallow-clones a git repo into the destination
// directory. Checks out the given ref if non-empty.
func clonePlugin(ctx context.Context, url, ref, destination string) error {
	if _, err := exec.LookPath("git"); err != nil {
		return errors.New("git is not installed, so an inmate cannot be fetched")
	}
	arguments := []string{"clone", "--depth", "1", "--quiet"}
	if ref != "" {
		arguments = append(arguments, "--branch", ref)
	}
	arguments = append(arguments, url, destination)
	command := exec.CommandContext(ctx, "git", arguments...)
	output, err := command.CombinedOutput()
	if err != nil {
		reported := strings.TrimSpace(string(output))
		if reported == "" {
			reported = err.Error()
		}
		return fmt.Errorf("could not clone %s: %s", url, reported)
	}
	return nil
}

// copyPluginTree replaces destination with a copy of source, skipping
// files ignored by the tree hash.
func copyPluginTree(source, destination string) error {
	if err := os.RemoveAll(destination); err != nil {
		return fmt.Errorf("cannot remove %s: %w", destination, err)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return fmt.Errorf("cannot create %s: %w", filepath.Dir(destination), err)
	}
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ignoredTreeNames[entry.Name()] {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		return copyFile(path, target, info.Mode().Perm())
	})
}

// copyFile copies a file to a new path with the given permissions.
func copyFile(source, destination string, mode fs.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("cannot read %s: %w", source, err)
	}
	defer input.Close()
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return fmt.Errorf("cannot create %s: %w", filepath.Dir(destination), err)
	}
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return fmt.Errorf("cannot write %s: %w", destination, err)
	}
	if _, err := io.Copy(output, input); err != nil {
		output.Close()
		return fmt.Errorf("cannot write %s: %w", destination, err)
	}
	return output.Close()
}

// approvalDescription builds the text shown before installing or
// re-approving an inmate. Takes the manifest and the inmate's
// filesystem.
func approvalDescription(manifest *Manifest, fsys fs.FS) (string, error) {
	files, err := TreeFiles(fsys)
	if err != nil {
		return "", err
	}
	text := &strings.Builder{}
	field(text, "inmate", manifest.Inmate.Name)
	field(text, "summary", manifest.Inmate.Description)
	field(text, "command", manifest.Command.Run)
	if manifest.Auth != nil {
		variables := make([]string, 0, len(manifest.Auth.Credentials))
		for _, credential := range manifest.Auth.Credentials {
			variables = append(variables, credential.Variable)
		}
		field(text, "upstream", fmt.Sprintf("%s, credential read from %s",
			manifest.Auth.Upstream, strings.Join(variables, ", ")))
	}
	if len(manifest.Egress.Hosts) > 0 {
		field(text, "egress", "adds "+strings.Join(manifest.Egress.Hosts, ", "))
	}
	if len(manifest.Persist.Paths) > 0 {
		field(text, "persists", strings.Join(manifest.Persist.Paths, ", "))
	}
	for _, line := range hostSummary(manifest) {
		field(text, "host", line)
	}
	fmt.Fprintf(text, "\nfiles\n")
	for _, name := range files {
		fmt.Fprintf(text, "  %s\n", name)
	}
	fmt.Fprintf(text, "\nThe Dockerfile is built as root, inside the box. Nothing in this inmate runs on your machine: prison itself performs the [host] operations above.\n")
	return text.String(), nil
}

// hostSummary returns one line per host operation the manifest
// declares.
func hostSummary(manifest *Manifest) []string {
	host := manifest.Host
	var lines []string
	if len(host.Copy) > 0 {
		lines = append(lines, fmt.Sprintf("copies %s from %s into the persisted home",
			strings.Join(host.Copy, ", "), host.Root))
	}
	var edited []string
	for _, operation := range host.JSON {
		edited = append(edited, operation.To)
	}
	if len(edited) > 0 {
		lines = append(lines, "edits "+strings.Join(edited, ", ")+" in the persisted home")
	}
	var read []string
	for _, source := range host.Environment {
		read = append(read, fmt.Sprintf("%s from %s of %s", source.Name, source.Path, source.From))
	}
	sort.Strings(read)
	if len(read) > 0 {
		lines = append(lines, "reads "+strings.Join(read, ", "))
	}
	return lines
}

// field writes one aligned key-value line to out.
func field(out io.Writer, key, value string) {
	fmt.Fprintf(out, "%-11s %s\n", key, value)
}

// writerOrDiscard returns out, or io.Discard if out is nil.
func writerOrDiscard(out io.Writer) io.Writer {
	if out == nil {
		return io.Discard
	}
	return out
}
