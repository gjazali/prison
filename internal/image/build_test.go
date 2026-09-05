package image

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"testing/fstest"

	"prison/internal/cage"
	"prison/internal/cage/fake"
)

// recordingCage wraps a fake cage and records each build's context
// directory contents for verification.
type recordingCage struct {
	*fake.Cage
	directories map[string]string
	files       map[string]string
}

// newRecordingCage returns a recording cage wrapping a fresh fake.
func newRecordingCage() *recordingCage {
	return &recordingCage{
		Cage:        fake.New(),
		directories: map[string]string{},
		files:       map[string]string{},
	}
}

// Build snapshots the context directory before delegating to the
// fake cage.
func (c *recordingCage) Build(
	ctx context.Context, spec cage.BuildSpec,
) error {
	c.directories[spec.Tag] = spec.Context
	walkError := filepath.WalkDir(spec.Context, func(
		name string, entry fs.DirEntry, err error,
	) error {
		if err != nil || entry.IsDir() {
			return err
		}
		relative, err := filepath.Rel(spec.Context, name)
		if err != nil {
			return err
		}
		contents, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		c.files[spec.Tag+"/"+filepath.ToSlash(relative)] = string(contents)
		return nil
	})
	if walkError != nil {
		return walkError
	}
	return c.Cage.Build(ctx, spec)
}

// buildLog returns the Build lines from the cage's call log.
func buildLog(c *recordingCage) []string {
	var built []string
	for _, line := range c.Calls {
		if strings.HasPrefix(line, "Build ") {
			built = append(built, strings.TrimPrefix(line, "Build "))
		}
	}
	return built
}

// TestEnsureBuildsEveryMissingStepInOrder checks build order,
// arguments, and progress output.
func TestEnsureBuildsEveryMissingStepInOrder(t *testing.T) {
	plan := planFor(t, testAssets(), testInmates())
	c := newRecordingCage()
	var out strings.Builder

	if err := Ensure(t.Context(), c, plan, &out); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	built := buildLog(c)
	if len(built) != len(plan.Steps) {
		t.Fatalf("built %d images, want %d", len(built), len(plan.Steps))
	}
	for index, step := range plan.Steps {
		if built[index] != step.Tag {
			t.Errorf("build %d was %s, want %s", index, built[index], step.Tag)
		}
		spec := c.BuildSpecs[index]
		for name, want := range step.Args {
			if spec.Args[name] != want {
				t.Errorf("%s was built with %s=%q, want %q",
					step.Tag, name, spec.Args[name], want)
			}
		}
		if spec.Output != &out {
			t.Errorf("%s was built without the caller's output", step.Tag)
		}
		want := "building  " + step.Tag + " (" + step.Description + ")"
		if !strings.Contains(out.String(), want) {
			t.Errorf("the output is missing %q: %s", want, out.String())
		}
	}
}

// TestEnsureSkipsImagesThatArePresent checks that present tags are
// skipped and the rest are built.
func TestEnsureSkipsImagesThatArePresent(t *testing.T) {
	plan := planFor(t, testAssets(), testInmates())
	c := newRecordingCage()
	c.Images[plan.Steps[0].Tag] = true
	c.Images[plan.Steps[1].Tag] = true
	var out strings.Builder

	if err := Ensure(t.Context(), c, plan, &out); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	built := buildLog(c)
	want := []string{plan.Steps[2].Tag, plan.Steps[3].Tag}
	if len(built) != len(want) {
		t.Fatalf("built %v, want %v", built, want)
	}
	for index := range want {
		if built[index] != want[index] {
			t.Errorf("build %d was %s, want %s",
				index, built[index], want[index])
		}
	}
	if !strings.Contains(out.String(), "have      "+plan.Steps[0].Tag) {
		t.Errorf("a present image was not reported: %s", out.String())
	}
}

// TestEnsureLaysOutAndRemovesTheContext checks that each build sees
// its context files and the temporary directory is removed after.
func TestEnsureLaysOutAndRemovesTheContext(t *testing.T) {
	inmates := testInmates()
	plan := planFor(t, testAssets(), inmates)
	c := newRecordingCage()

	if err := Ensure(t.Context(), c, plan, nil); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	baseTag := plan.Steps[0].Tag
	if c.files[baseTag+"/prison-guest"] != "guest binary" {
		t.Errorf("the base context is missing the guest binary: %v", c.files)
	}
	if !strings.Contains(c.files[baseTag+"/Dockerfile"], "PRISON_FOUNDATION") {
		t.Error("the base context is missing its Dockerfile")
	}
	layerTag := plan.Steps[1].Tag
	if !strings.Contains(c.files[layerTag+"/Dockerfile"], inmates[0].Name) {
		t.Errorf("the %s context holds the wrong Dockerfile", inmates[0].Name)
	}
	if _, present := c.files[layerTag+"/prison-guest"]; present {
		t.Error("an inmate context was built from the base files")
	}
	for tag, directory := range c.directories {
		if _, err := os.Stat(directory); !os.IsNotExist(err) {
			t.Errorf("the build directory for %s survived at %s",
				tag, directory)
		}
	}
}

// TestEnsureRefusesABaseWithoutTheGuestBinary checks that a missing
// guest binary is caught before building and the error says how to
// fix it.
func TestEnsureRefusesABaseWithoutTheGuestBinary(t *testing.T) {
	assets := testAssets()
	delete(assets, "images/base/prison-guest")
	plan := planFor(t, assets, testInmates())
	c := newRecordingCage()

	err := Ensure(t.Context(), c, plan, nil)
	if err == nil {
		t.Fatal("a base image with no guest binary was built")
	}
	if !strings.Contains(err.Error(), "make build") {
		t.Errorf("the error does not say what to run: %v", err)
	}
	if len(c.BuildSpecs) != 0 {
		t.Errorf("%d images were built anyway", len(c.BuildSpecs))
	}
}

// TestEnsureStopsAtTheFirstFailure checks that a failed build stops
// the chain and the error names the tag.
func TestEnsureStopsAtTheFirstFailure(t *testing.T) {
	plan := planFor(t, testAssets(), testInmates())
	c := newRecordingCage()
	c.Fail["Build"] = os.ErrPermission

	err := Ensure(t.Context(), c, plan, nil)
	if err == nil {
		t.Fatal("a failed build was not reported")
	}
	if !strings.Contains(err.Error(), plan.Steps[0].Tag) {
		t.Errorf("the error does not name the image: %v", err)
	}
	if built := buildLog(c); len(built) != 1 {
		t.Errorf("kept going after a failure: %v", built)
	}
}

// TestEnsureReportsAnUnreadableCage checks that a cage that cannot
// check images stops the run instead of rebuilding.
func TestEnsureReportsAnUnreadableCage(t *testing.T) {
	plan := planFor(t, testAssets(), testInmates())
	c := newRecordingCage()
	c.Fail["ImageExists"] = os.ErrPermission

	if err := Ensure(t.Context(), c, plan, nil); err == nil {
		t.Fatal("an unreadable cage was not reported")
	}
	if len(c.BuildSpecs) != 0 {
		t.Errorf("%d images were built anyway", len(c.BuildSpecs))
	}
}

// TestExtractFSRoundTrip checks that files arrive with correct
// contents and fixed modes.
func TestExtractFSRoundTrip(t *testing.T) {
	source := fstest.MapFS{
		"Dockerfile":       &fstest.MapFile{Data: []byte("FROM scratch\n")},
		"scripts/setup.sh": &fstest.MapFile{Data: []byte("#!/bin/sh\n")},
		"a/b/c/deep.txt":   &fstest.MapFile{Data: []byte("deep")},
		"empty":            &fstest.MapFile{Data: []byte{}},
	}
	destination := filepath.Join(t.TempDir(), "context")
	if err := ExtractFS(source, destination); err != nil {
		t.Fatalf("ExtractFS: %v", err)
	}

	var arrived []string
	err := filepath.WalkDir(destination, func(
		name string, entry fs.DirEntry, err error,
	) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if info.Mode().Perm() != 0o755 {
				t.Errorf("%s is mode %v", name, info.Mode().Perm())
			}
			return nil
		}
		if info.Mode().Perm() != 0o644 {
			t.Errorf("%s is mode %v", name, info.Mode().Perm())
		}
		relative, err := filepath.Rel(destination, name)
		if err != nil {
			return err
		}
		arrived = append(arrived, filepath.ToSlash(relative))
		return nil
	})
	if err != nil {
		t.Fatalf("walking the extracted context: %v", err)
	}

	sort.Strings(arrived)
	want := []string{"Dockerfile", "a/b/c/deep.txt", "empty",
		"scripts/setup.sh"}
	if strings.Join(arrived, " ") != strings.Join(want, " ") {
		t.Fatalf("extracted %v, want %v", arrived, want)
	}
	for _, name := range want {
		contents, err := os.ReadFile(filepath.Join(destination,
			filepath.FromSlash(name)))
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		if string(contents) != string(source[name].Data) {
			t.Errorf("%s came out as %q", name, contents)
		}
	}
}

// TestExtractFSReportsAnUnwritableDestination checks that an
// unwritable destination returns an error.
func TestExtractFSReportsAnUnwritableDestination(t *testing.T) {
	blocked := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocked, []byte("in the way"), 0o644); err != nil {
		t.Fatalf("writing the blocking file: %v", err)
	}
	source := fstest.MapFS{
		"Dockerfile": &fstest.MapFile{Data: []byte("FROM scratch\n")},
	}
	if err := ExtractFS(source, filepath.Join(blocked, "context")); err == nil {
		t.Fatal("an unwritable destination was accepted")
	}
}
