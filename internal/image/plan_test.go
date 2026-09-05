package image

import (
	"strings"
	"testing"
	"testing/fstest"

	"prison/internal/plugin"
)

// testAssets returns a filesystem with a base Dockerfile and guest
// binary.
func testAssets() fstest.MapFS {
	return fstest.MapFS{
		"images/base/Dockerfile": &fstest.MapFile{
			Data: []byte("FROM ${PRISON_FOUNDATION}\n"),
		},
		"images/base/prison-guest": &fstest.MapFile{
			Data: []byte("guest binary"),
		},
	}
}

// testInmates returns three inmates with distinct Dockerfiles.
func testInmates() []*plugin.Inmate {
	inmates := []*plugin.Inmate{}
	for _, name := range []string{"claude", "codex", "gemini"} {
		inmates = append(inmates, &plugin.Inmate{
			Name: name,
			FS: fstest.MapFS{
				"Dockerfile": &fstest.MapFile{
					Data: []byte("FROM ${PRISON_BASE}\n# " + name + "\n"),
				},
			},
		})
	}
	return inmates
}

// planFor builds a plan from assets and inmates. Fails the test on
// error.
func planFor(
	t *testing.T, assets fstest.MapFS, inmates []*plugin.Inmate,
) *Plan {
	t.Helper()
	plan, err := NewPlan(assets, "images/base", inmates,
		DefaultFoundation, 501)
	if err != nil {
		t.Fatalf("NewPlan: %v", err)
	}
	return plan
}

// tagsOf returns the tags of every step in build order.
func tagsOf(plan *Plan) []string {
	tags := make([]string, 0, len(plan.Steps))
	for _, step := range plan.Steps {
		tags = append(tags, step.Tag)
	}
	return tags
}

// TestShortHashIsStableAndUnambiguous checks the digest length,
// stability, and that different part lists hash differently.
func TestShortHashIsStableAndUnambiguous(t *testing.T) {
	first := ShortHash("a", "b")
	if len(first) != 12 {
		t.Errorf("got a %d character hash, want 12", len(first))
	}
	if ShortHash("a", "b") != first {
		t.Error("the same parts hashed differently twice")
	}
	if ShortHash("ab") == first {
		t.Error("the part boundary is not part of the hash")
	}
	if ShortHash("a", "b", "") == first {
		t.Error("a trailing empty part did not change the hash")
	}
}

// TestPlanShape checks tags, contexts, build arguments,
// descriptions, and that Final is the last tag.
func TestPlanShape(t *testing.T) {
	assets := testAssets()
	inmates := testInmates()
	plan := planFor(t, assets, inmates)

	if len(plan.Steps) != 4 {
		t.Fatalf("got %d steps, want 4", len(plan.Steps))
	}
	if plan.Foundation != DefaultFoundation {
		t.Errorf("got the foundation %s, want %s",
			plan.Foundation, DefaultFoundation)
	}
	base := plan.Steps[0]
	if !strings.HasPrefix(base.Tag, BaseRepository+":") {
		t.Errorf("the base step is tagged %s", base.Tag)
	}
	if base.Description != "base on "+DefaultFoundation {
		t.Errorf("the base step says %q", base.Description)
	}
	wantArgs := map[string]string{
		"PRISON_FOUNDATION": DefaultFoundation,
		"UID":               "501",
	}
	for name, want := range wantArgs {
		if base.Args[name] != want {
			t.Errorf("the base %s argument is %q, want %q",
				name, base.Args[name], want)
		}
	}
	if _, err := base.Context.Open("prison-guest"); err != nil {
		t.Errorf("the base context is not rooted at the base directory: %v",
			err)
	}

	previous := base.Tag
	for index, inmate := range inmates {
		step := plan.Steps[index+1]
		if !strings.HasPrefix(step.Tag, BoxRepository+":") {
			t.Errorf("the %s step is tagged %s", inmate.Name, step.Tag)
		}
		if step.Args["PRISON_BASE"] != previous {
			t.Errorf("the %s step builds on %q, want %q",
				inmate.Name, step.Args["PRISON_BASE"], previous)
		}
		if step.Description != "layer for "+inmate.Name {
			t.Errorf("the %s step says %q", inmate.Name, step.Description)
		}
		previous = step.Tag
	}
	if plan.Final != previous {
		t.Errorf("Final is %s, want the last tag %s", plan.Final, previous)
	}
}

// TestPlanTagsAreStable checks that identical inputs produce
// identical tags.
func TestPlanTagsAreStable(t *testing.T) {
	first := tagsOf(planFor(t, testAssets(), testInmates()))
	second := tagsOf(planFor(t, testAssets(), testInmates()))
	for index := range first {
		if first[index] != second[index] {
			t.Fatalf("step %d is %s then %s",
				index, first[index], second[index])
		}
	}
}

// TestPlanTagsFollowTheFoundationAndUID checks that changing the
// foundation or uid moves every tag.
func TestPlanTagsFollowTheFoundationAndUID(t *testing.T) {
	assets := testAssets()
	inmates := testInmates()
	original := tagsOf(planFor(t, assets, inmates))

	other, err := NewPlan(assets, "images/base", inmates, "fedora:41", 501)
	if err != nil {
		t.Fatalf("NewPlan: %v", err)
	}
	for index, tag := range tagsOf(other) {
		if tag == original[index] {
			t.Errorf("a new foundation left step %d at %s", index, tag)
		}
	}

	other, err = NewPlan(assets, "images/base", inmates,
		DefaultFoundation, 1000)
	if err != nil {
		t.Fatalf("NewPlan: %v", err)
	}
	for index, tag := range tagsOf(other) {
		if tag == original[index] {
			t.Errorf("a new uid left step %d at %s", index, tag)
		}
	}
}

// TestChangingTheBaseChangesEveryTag checks that editing a base file
// changes every tag in the chain.
func TestChangingTheBaseChangesEveryTag(t *testing.T) {
	inmates := testInmates()
	original := tagsOf(planFor(t, testAssets(), inmates))

	changed := testAssets()
	changed["images/base/Dockerfile"] = &fstest.MapFile{
		Data: []byte("FROM ${PRISON_FOUNDATION}\nRUN true\n"),
	}
	for index, tag := range tagsOf(planFor(t, changed, inmates)) {
		if tag == original[index] {
			t.Errorf("a changed base left step %d at %s", index, tag)
		}
	}
}

// TestChangingOneInmateChangesOnlyItsTagAndLater checks that editing
// one inmate moves only its tag and later tags.
func TestChangingOneInmateChangesOnlyItsTagAndLater(t *testing.T) {
	assets := testAssets()
	original := tagsOf(planFor(t, assets, testInmates()))

	inmates := testInmates()
	inmates[1].FS = fstest.MapFS{
		"Dockerfile": &fstest.MapFile{Data: []byte("FROM ${PRISON_BASE}\n")},
		"setup.sh":   &fstest.MapFile{Data: []byte("#!/bin/sh\n")},
	}
	changed := tagsOf(planFor(t, assets, inmates))

	for _, index := range []int{0, 1} {
		if changed[index] != original[index] {
			t.Errorf("step %d moved from %s to %s",
				index, original[index], changed[index])
		}
	}
	for _, index := range []int{2, 3} {
		if changed[index] == original[index] {
			t.Errorf("step %d stayed at %s", index, changed[index])
		}
	}
}

// TestRenamingAnInmateChangesItsTag checks that the name affects
// the hash even with an identical tree.
func TestRenamingAnInmateChangesItsTag(t *testing.T) {
	assets := testAssets()
	inmates := testInmates()
	original := tagsOf(planFor(t, assets, inmates))

	inmates[0].Name = "claude-code"
	if tagsOf(planFor(t, assets, inmates))[1] == original[1] {
		t.Error("a renamed inmate kept its tag")
	}
}

// TestNewPlanRefusesAnInmateWithoutAContext checks that a missing
// inmate filesystem is refused with an error naming the inmate.
func TestNewPlanRefusesAnInmateWithoutAContext(t *testing.T) {
	inmates := []*plugin.Inmate{{Name: "claude"}}
	_, err := NewPlan(testAssets(), "images/base", inmates,
		DefaultFoundation, 501)
	if err == nil {
		t.Fatal("an inmate with no context was accepted")
	}
	if !strings.Contains(err.Error(), "claude") {
		t.Errorf("the error does not name the inmate: %v", err)
	}
}

// TestNewPlanRefusesAMissingBaseDirectory checks that a missing base
// directory produces an error.
func TestNewPlanRefusesAMissingBaseDirectory(t *testing.T) {
	_, err := NewPlan(testAssets(), "images/nowhere", nil,
		DefaultFoundation, 501)
	if err == nil {
		t.Fatal("a missing base directory was accepted")
	}
}

// TestGuestBinaryPresent checks detection of the guest binary in
// the base directory.
func TestGuestBinaryPresent(t *testing.T) {
	assets := testAssets()
	if !GuestBinaryPresent(assets, "images/base") {
		t.Error("the embedded guest binary was not found")
	}
	delete(assets, "images/base/prison-guest")
	if GuestBinaryPresent(assets, "images/base") {
		t.Error("a missing guest binary was reported present")
	}
	if GuestBinaryPresent(testAssets(), "images/nowhere") {
		t.Error("a missing base directory was reported present")
	}
}
