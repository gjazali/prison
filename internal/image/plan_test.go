package image

import (
	"strings"
	"testing"
	"testing/fstest"

	"prison/internal/plugin"
)

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

func planFor(
	t *testing.T, assets fstest.MapFS, inmates []*plugin.Inmate,
) *Plan {
	t.Helper()
	plan, err := NewPlan(assets, "images/base", inmates,
		DefaultFoundation, 501, nil)
	if err != nil {
		t.Fatalf("NewPlan error = %v, want nil", err)
	}
	return plan
}

func tagsOf(plan *Plan) []string {
	tags := make([]string, 0, len(plan.Steps))
	for _, step := range plan.Steps {
		tags = append(tags, step.Tag)
	}
	return tags
}

func TestShortHashIsStableAndUnambiguous(t *testing.T) {
	first := ShortHash("a", "b")
	if len(first) != 12 {
		t.Errorf("len(ShortHash) = %d, want 12", len(first))
	}
	if ShortHash("a", "b") != first {
		t.Error("ShortHash = two hashes for the same parts, want one")
	}
	if ShortHash("ab") == first {
		t.Error("ShortHash(ab) = ShortHash(a, b), want different hashes")
	}
	if ShortHash("a", "b", "") == first {
		t.Error("ShortHash(a, b, empty) = ShortHash(a, b), want different hashes")
	}
}

func TestPlanShape(t *testing.T) {
	assets := testAssets()
	inmates := testInmates()
	plan := planFor(t, assets, inmates)

	if len(plan.Steps) != 4 {
		t.Fatalf("len(Steps) = %d, want 4", len(plan.Steps))
	}
	if plan.Foundation != DefaultFoundation {
		t.Errorf("Foundation = %s, want %s",
			plan.Foundation, DefaultFoundation)
	}
	base := plan.Steps[0]
	if !strings.HasPrefix(base.Tag, BaseRepository+":") {
		t.Errorf("base tag = %s, want prefix prison/base:", base.Tag)
	}
	if base.Description != "base on "+DefaultFoundation {
		t.Errorf("base Description = %q, want base on %s",
			base.Description, DefaultFoundation)
	}
	wantArgs := map[string]string{
		"PRISON_FOUNDATION": DefaultFoundation,
		"UID":               "501",
	}
	for name, want := range wantArgs {
		if base.Args[name] != want {
			t.Errorf("base arg %s = %q, want %q",
				name, base.Args[name], want)
		}
	}
	if _, err := base.Context.Open("prison-guest"); err != nil {
		t.Errorf("base context has no prison-guest, want the base directory: %v",
			err)
	}

	previous := base.Tag
	for index, inmate := range inmates {
		step := plan.Steps[index+1]
		if !strings.HasPrefix(step.Tag, BoxRepository+":") {
			t.Errorf("%s tag = %s, want prefix prison/box:", inmate.Name, step.Tag)
		}
		if step.Args["PRISON_BASE"] != previous {
			t.Errorf("%s PRISON_BASE = %q, want %q",
				inmate.Name, step.Args["PRISON_BASE"], previous)
		}
		if step.Description != "layer for "+inmate.Name {
			t.Errorf("%s Description = %q, want layer for %s",
				inmate.Name, step.Description, inmate.Name)
		}
		previous = step.Tag
	}
	if plan.Final != previous {
		t.Errorf("Final = %s, want %s", plan.Final, previous)
	}
}

func TestPlanTagsAreStable(t *testing.T) {
	first := tagsOf(planFor(t, testAssets(), testInmates()))
	second := tagsOf(planFor(t, testAssets(), testInmates()))
	for index := range first {
		if first[index] != second[index] {
			t.Fatalf("step %d = %s, then %s, want the same tag",
				index, first[index], second[index])
		}
	}
}

func TestPlanTagsFollowTheFoundationAndUID(t *testing.T) {
	assets := testAssets()
	inmates := testInmates()
	original := tagsOf(planFor(t, assets, inmates))

	other, err := NewPlan(assets, "images/base", inmates, "fedora:41", 501, nil)
	if err != nil {
		t.Fatalf("NewPlan error = %v, want nil", err)
	}
	for index, tag := range tagsOf(other) {
		if tag == original[index] {
			t.Errorf("step %d = %s after a foundation change, want a new tag",
				index, tag)
		}
	}

	other, err = NewPlan(assets, "images/base", inmates,
		DefaultFoundation, 1000, nil)
	if err != nil {
		t.Fatalf("NewPlan error = %v, want nil", err)
	}
	for index, tag := range tagsOf(other) {
		if tag == original[index] {
			t.Errorf("step %d = %s after a uid change, want a new tag", index, tag)
		}
	}
}

func TestChangingTheBaseChangesEveryTag(t *testing.T) {
	inmates := testInmates()
	original := tagsOf(planFor(t, testAssets(), inmates))

	changed := testAssets()
	changed["images/base/Dockerfile"] = &fstest.MapFile{
		Data: []byte("FROM ${PRISON_FOUNDATION}\nRUN true\n"),
	}
	for index, tag := range tagsOf(planFor(t, changed, inmates)) {
		if tag == original[index] {
			t.Errorf("step %d = %s after a base change, want a new tag", index, tag)
		}
	}
}

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
			t.Errorf("step %d = %s, then %s, want the same tag",
				index, original[index], changed[index])
		}
	}
	for _, index := range []int{2, 3} {
		if changed[index] == original[index] {
			t.Errorf("step %d = %s, want a new tag", index, changed[index])
		}
	}
}

func TestRenamingAnInmateChangesItsTag(t *testing.T) {
	assets := testAssets()
	inmates := testInmates()
	original := tagsOf(planFor(t, assets, inmates))

	inmates[0].Name = "claude-code"
	if tagsOf(planFor(t, assets, inmates))[1] == original[1] {
		t.Error("tag is the same after a rename, want a new tag")
	}
}

func TestNewPlanRefusesAnInmateWithoutAContext(t *testing.T) {
	inmates := []*plugin.Inmate{{Name: "claude"}}
	_, err := NewPlan(testAssets(), "images/base", inmates,
		DefaultFoundation, 501, nil)
	if err == nil {
		t.Fatal("NewPlan error = nil, want an error")
	}
	if !strings.Contains(err.Error(), "claude") {
		t.Errorf("error = %v, want it to contain claude", err)
	}
}

func TestNewPlanRefusesAMissingBaseDirectory(t *testing.T) {
	_, err := NewPlan(testAssets(), "images/nowhere", nil,
		DefaultFoundation, 501, nil)
	if err == nil {
		t.Fatal("NewPlan error = nil, want an error")
	}
}

func TestPlanRevisionsAndVersionPinning(t *testing.T) {
	assets := testAssets()
	inmates := testInmates()
	inmates[0].Image.VersionArg = "CLAUDE_VERSION"
	original := planFor(t, assets, inmates)

	bumped, err := NewPlan(assets, "images/base", inmates,
		DefaultFoundation, 501, map[string]int{"codex": 1})
	if err != nil {
		t.Fatalf("NewPlan(bumped): %v", err)
	}
	if bumped.Steps[0].Tag != original.Steps[0].Tag ||
		bumped.Steps[1].Tag != original.Steps[1].Tag {
		t.Fatal("bumping codex changed base or claude tags")
	}
	if bumped.Steps[2].Tag == original.Steps[2].Tag ||
		bumped.Steps[3].Tag == original.Steps[3].Tag {
		t.Fatal("bumping codex did not change codex and downstream tags")
	}
	if !bumped.Steps[2].NoCache {
		t.Error("codex step NoCache = false, want true")
	}
	if bumped.Steps[0].NoCache || bumped.Steps[1].NoCache || bumped.Steps[3].NoCache {
		t.Error("unexpected NoCache on non-bumped steps")
	}

	pinnedInmates := testInmates()
	pinnedInmates[0].Image.VersionArg = "CLAUDE_VERSION"
	pinnedInmates[0].Version = "1.0.50"
	pinned, err := NewPlan(assets, "images/base", pinnedInmates,
		DefaultFoundation, 501, map[string]int{"claude": 2})
	if err != nil {
		t.Fatalf("NewPlan(pinned): %v", err)
	}
	if pinned.Steps[1].Tag == original.Steps[1].Tag {
		t.Error("pinned claude tag matched unpinned tag")
	}
	if pinned.Steps[1].Args["CLAUDE_VERSION"] != "1.0.50" {
		t.Errorf("CLAUDE_VERSION = %q, want 1.0.50", pinned.Steps[1].Args["CLAUDE_VERSION"])
	}
	if pinned.Steps[1].NoCache {
		t.Error("pinned step NoCache = true, want false")
	}
	pinnedAgain, err := NewPlan(assets, "images/base", pinnedInmates,
		DefaultFoundation, 501, map[string]int{"claude": 9})
	if err != nil {
		t.Fatalf("NewPlan(pinnedAgain): %v", err)
	}
	if pinnedAgain.Steps[1].Tag != pinned.Steps[1].Tag {
		t.Error("revision bump changed a version-pinned inmate tag")
	}

	movingInmates := testInmates()
	movingInmates[0].Image.VersionArg = "CLAUDE_VERSION"
	movingInmates[0].Image.MovingTags = []string{"latest", "stable"}
	movingInmates[0].Version = "stable"
	moving, err := NewPlan(assets, "images/base", movingInmates,
		DefaultFoundation, 501, nil)
	if err != nil {
		t.Fatalf("NewPlan(moving): %v", err)
	}
	if moving.Steps[1].Tag == original.Steps[1].Tag {
		t.Error("moving tag matched unpinned tag")
	}
	if moving.Steps[1].Args["CLAUDE_VERSION"] != "stable" {
		t.Errorf("CLAUDE_VERSION = %q, want stable", moving.Steps[1].Args["CLAUDE_VERSION"])
	}
	movingBumped, err := NewPlan(assets, "images/base", movingInmates,
		DefaultFoundation, 501, map[string]int{"claude": 1})
	if err != nil {
		t.Fatalf("NewPlan(movingBumped): %v", err)
	}
	if movingBumped.Steps[1].Tag == moving.Steps[1].Tag {
		t.Error("revision bump did not change a moving tag")
	}
	if !movingBumped.Steps[1].NoCache {
		t.Error("bumped moving tag NoCache = false, want true")
	}
}

func TestGuestBinaryPresent(t *testing.T) {
	assets := testAssets()
	if !GuestBinaryPresent(assets, "images/base") {
		t.Error("GuestBinaryPresent = false, want true")
	}
	delete(assets, "images/base/prison-guest")
	if GuestBinaryPresent(assets, "images/base") {
		t.Error("GuestBinaryPresent = true with no binary, want false")
	}
	if GuestBinaryPresent(testAssets(), "images/nowhere") {
		t.Error("GuestBinaryPresent = true with no directory, want false")
	}
}
