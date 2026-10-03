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

	"prison/internal/isolator"
	"prison/internal/isolator/fake"
)

type recordingIsolator struct {
	*fake.Isolator
	directories map[string]string
	files       map[string]string
}

func newRecordingIsolator() *recordingIsolator {
	return &recordingIsolator{
		Isolator:    fake.New(),
		directories: map[string]string{},
		files:       map[string]string{},
	}
}

func (c *recordingIsolator) Build(
	ctx context.Context, spec isolator.BuildSpec,
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
	return c.Isolator.Build(ctx, spec)
}

func buildLog(c *recordingIsolator) []string {
	var built []string
	for _, line := range c.Calls {
		if strings.HasPrefix(line, "Build ") {
			built = append(built, strings.TrimPrefix(line, "Build "))
		}
	}
	return built
}

func TestEnsureBuildsEveryMissingStepInOrder(t *testing.T) {
	plan := planFor(t, testAssets(), testInmates())
	c := newRecordingIsolator()
	var out strings.Builder

	if err := Ensure(t.Context(), c, plan, &out); err != nil {
		t.Fatalf("Ensure error = %v, want nil", err)
	}

	built := buildLog(c)
	if len(built) != len(plan.Steps) {
		t.Fatalf("built %d images, want %d", len(built), len(plan.Steps))
	}
	for index, step := range plan.Steps {
		if built[index] != step.Tag {
			t.Errorf("build %d = %s, want %s", index, built[index], step.Tag)
		}
		spec := c.BuildSpecs[index]
		for name, want := range step.Args {
			if spec.Args[name] != want {
				t.Errorf("%s build arg %s = %q, want %q",
					step.Tag, name, spec.Args[name], want)
			}
		}
		if spec.Output != &out {
			t.Errorf("%s build output is not the caller's writer", step.Tag)
		}
		want := "building  " + step.Tag + " (" + step.Description + ")"
		if !strings.Contains(out.String(), want) {
			t.Errorf("output = %s, want %q", out.String(), want)
		}
	}
}

func TestEnsureSkipsImagesThatArePresent(t *testing.T) {
	plan := planFor(t, testAssets(), testInmates())
	c := newRecordingIsolator()
	c.Images[plan.Steps[0].Tag] = true
	c.Images[plan.Steps[1].Tag] = true
	var out strings.Builder

	if err := Ensure(t.Context(), c, plan, &out); err != nil {
		t.Fatalf("Ensure error = %v, want nil", err)
	}

	built := buildLog(c)
	want := []string{plan.Steps[2].Tag, plan.Steps[3].Tag}
	if len(built) != len(want) {
		t.Fatalf("built %v, want %v", built, want)
	}
	for index := range want {
		if built[index] != want[index] {
			t.Errorf("build %d = %s, want %s",
				index, built[index], want[index])
		}
	}
}

func releaseCount(c *recordingIsolator) int {
	count := 0
	for _, line := range c.Calls {
		if line == "ReleaseBuilder" {
			count++
		}
	}
	return count
}

func TestEnsureReleasesTheBuilderOnceAfterBuilding(t *testing.T) {
	plan := planFor(t, testAssets(), testInmates())
	c := newRecordingIsolator()

	if err := Ensure(t.Context(), c, plan, nil); err != nil {
		t.Fatalf("Ensure error = %v, want nil", err)
	}

	if count := releaseCount(c); count != 1 {
		t.Fatalf("ReleaseBuilder calls = %d, want 1", count)
	}
	if c.Calls[len(c.Calls)-1] != "ReleaseBuilder" {
		t.Errorf("last call = not ReleaseBuilder, want ReleaseBuilder: %v",
			c.Calls)
	}
}

func TestEnsureKeepsTheBuilderWhenNothingIsBuilt(t *testing.T) {
	plan := planFor(t, testAssets(), testInmates())
	c := newRecordingIsolator()
	for _, step := range plan.Steps {
		c.Images[step.Tag] = true
	}

	if err := Ensure(t.Context(), c, plan, nil); err != nil {
		t.Fatalf("Ensure error = %v, want nil", err)
	}

	if count := releaseCount(c); count != 0 {
		t.Errorf("ReleaseBuilder calls = %d, want 0",
			count)
	}
}

func TestEnsureReleasesTheBuilderAfterAFailure(t *testing.T) {
	plan := planFor(t, testAssets(), testInmates())
	c := newRecordingIsolator()
	c.Fail["Build"] = os.ErrPermission

	if err := Ensure(t.Context(), c, plan, nil); err == nil {
		t.Fatal("Ensure error = nil, want an error")
	}

	if count := releaseCount(c); count != 1 {
		t.Errorf("ReleaseBuilder calls = %d, want 1", count)
	}
}

func TestEnsureReportsAnUnreleasedBuilderWithoutFailing(t *testing.T) {
	plan := planFor(t, testAssets(), testInmates())
	c := newRecordingIsolator()
	c.Fail["ReleaseBuilder"] = os.ErrPermission
	var out strings.Builder

	if err := Ensure(t.Context(), c, plan, &out); err != nil {
		t.Fatalf("Ensure error = %v, want nil", err)
	}

	if !strings.Contains(out.String(), "cannot stop the image builder") {
		t.Errorf("output = %s, want a builder note", out.String())
	}
}

func TestEnsureLaysOutAndRemovesTheContext(t *testing.T) {
	inmates := testInmates()
	plan := planFor(t, testAssets(), inmates)
	c := newRecordingIsolator()

	if err := Ensure(t.Context(), c, plan, nil); err != nil {
		t.Fatalf("Ensure error = %v, want nil", err)
	}

	baseTag := plan.Steps[0].Tag
	if c.files[baseTag+"/prison-guest"] != "guest binary" {
		t.Errorf("base context = %v, want the guest binary", c.files)
	}
	if !strings.Contains(c.files[baseTag+"/Dockerfile"], "PRISON_FOUNDATION") {
		t.Error("base context has no Dockerfile, want one")
	}
	layerTag := plan.Steps[1].Tag
	if !strings.Contains(c.files[layerTag+"/Dockerfile"], inmates[0].Name) {
		t.Errorf("%s Dockerfile is wrong, want the inmate's", inmates[0].Name)
	}
	if _, present := c.files[layerTag+"/prison-guest"]; present {
		t.Error("inmate context has prison-guest, want no base files")
	}
	for tag, directory := range c.directories {
		if _, err := os.Stat(directory); !os.IsNotExist(err) {
			t.Errorf("build directory for %s exists at %s, want it removed",
				tag, directory)
		}
	}
}

func TestEnsureRefusesABaseWithoutTheGuestBinary(t *testing.T) {
	assets := testAssets()
	delete(assets, "images/base/prison-guest")
	plan := planFor(t, assets, testInmates())
	c := newRecordingIsolator()

	err := Ensure(t.Context(), c, plan, nil)
	if err == nil {
		t.Fatal("Ensure error = nil, want an error")
	}
	if !strings.Contains(err.Error(), "make build") {
		t.Errorf("error = %v, want it to contain make build", err)
	}
	if len(c.BuildSpecs) != 0 {
		t.Errorf("built %d images, want 0", len(c.BuildSpecs))
	}
}

func TestEnsureStopsAtTheFirstFailure(t *testing.T) {
	plan := planFor(t, testAssets(), testInmates())
	c := newRecordingIsolator()
	c.Fail["Build"] = os.ErrPermission

	err := Ensure(t.Context(), c, plan, nil)
	if err == nil {
		t.Fatal("Ensure error = nil, want an error")
	}
	if !strings.Contains(err.Error(), plan.Steps[0].Tag) {
		t.Errorf("error = %v, want the image tag", err)
	}
	if built := buildLog(c); len(built) != 1 {
		t.Errorf("built %v, want one build", built)
	}
}

func TestEnsureReportsAnUnreadableIsolator(t *testing.T) {
	plan := planFor(t, testAssets(), testInmates())
	c := newRecordingIsolator()
	c.Fail["ImageExists"] = os.ErrPermission

	if err := Ensure(t.Context(), c, plan, nil); err == nil {
		t.Fatal("Ensure error = nil, want an error")
	}
	if len(c.BuildSpecs) != 0 {
		t.Errorf("built %d images, want 0", len(c.BuildSpecs))
	}
}

func TestExtractFSRoundTrip(t *testing.T) {
	source := fstest.MapFS{
		"Dockerfile":       &fstest.MapFile{Data: []byte("FROM scratch\n")},
		"scripts/setup.sh": &fstest.MapFile{Data: []byte("#!/bin/sh\n")},
		"a/b/c/deep.txt":   &fstest.MapFile{Data: []byte("deep")},
		"empty":            &fstest.MapFile{Data: []byte{}},
	}
	destination := filepath.Join(t.TempDir(), "context")
	if err := ExtractFS(source, destination); err != nil {
		t.Fatalf("ExtractFS error = %v, want nil", err)
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
				t.Errorf("%s mode = %v, want 0755", name, info.Mode().Perm())
			}
			return nil
		}
		if info.Mode().Perm() != 0o644 {
			t.Errorf("%s mode = %v, want 0644", name, info.Mode().Perm())
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
			t.Errorf("%s = %q, want the source data", name, contents)
		}
	}
}

func TestExtractFSReportsAnUnwritableDestination(t *testing.T) {
	blocked := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocked, []byte("in the way"), 0o644); err != nil {
		t.Fatalf("writing the blocking file: %v", err)
	}
	source := fstest.MapFS{
		"Dockerfile": &fstest.MapFile{Data: []byte("FROM scratch\n")},
	}
	if err := ExtractFS(source, filepath.Join(blocked, "context")); err == nil {
		t.Fatal("ExtractFS error = nil, want an error")
	}
}
