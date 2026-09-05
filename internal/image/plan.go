package image

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"path"
	"strconv"
	"strings"

	"prison/internal/plugin"
)

// shortHashLength is how many hex characters a tag carries.
const shortHashLength = 12

// guestBinaryName is the file the base image context must hold.
// `make build` puts it there before the binary embeds its assets.
const guestBinaryName = "prison-guest"

// ShortHash returns the first twelve hex characters of the SHA-256 of
// the given parts. Each part is followed by a NUL byte so different
// part lists never produce the same hash.
func ShortHash(parts ...string) string {
	digest := sha256.New()
	for _, part := range parts {
		digest.Write([]byte(part))
		digest.Write([]byte{0})
	}
	return hex.EncodeToString(digest.Sum(nil))[:shortHashLength]
}

// Step is one image build. Context holds the Dockerfile and its
// files. Description is the phrase shown in progress output.
type Step struct {
	Tag         string
	Context     fs.FS
	Dockerfile  string
	Args        map[string]string
	Description string
}

// Plan is the ordered chain of builds that produces a box image.
// Final is the tag of the last step, the image a box runs.
type Plan struct {
	Foundation string
	Steps      []Step
	Final      string
}

// NewPlan returns the build chain for a set of inmates. It takes an
// asset filesystem, a base directory, inmates, a foundation image,
// and the host UID. It returns a Plan with one base step and one
// layer per inmate. Tags are hashed from the inputs. It returns an
// error when a tree cannot be read.
func NewPlan(
	assets fs.FS,
	baseDir string,
	inmates []*plugin.Inmate,
	foundation string,
	hostUID int,
) (*Plan, error) {
	baseContext, err := fs.Sub(assets, baseDir)
	if err != nil {
		return nil, fmt.Errorf(
			"cannot read the base image context %s: %w", baseDir, err)
	}
	baseTree, err := plugin.TreeHash(baseContext)
	if err != nil {
		return nil, fmt.Errorf(
			"cannot hash the base image context %s: %w", baseDir, err)
	}
	cumulative := ShortHash(foundation, strconv.Itoa(hostUID), baseTree)
	previousTag := BaseRepository + ":" + cumulative
	plan := &Plan{
		Foundation: foundation,
		Steps: []Step{{
			Tag:     previousTag,
			Context: baseContext,
			Args: map[string]string{
				"PRISON_FOUNDATION": foundation,
				"UID":               strconv.Itoa(hostUID),
			},
			Description: "base on " + foundation,
		}},
	}
	for _, inmate := range inmates {
		if inmate.FS == nil {
			return nil, fmt.Errorf(
				"the inmate %s has no image context to build", inmate.Name)
		}
		tree, err := plugin.TreeHash(inmate.FS)
		if err != nil {
			return nil, fmt.Errorf(
				"cannot hash the inmate %s: %w", inmate.Name, err)
		}
		cumulative = ShortHash(cumulative, inmate.Name, tree)
		tag := BoxRepository + ":" + cumulative
		plan.Steps = append(plan.Steps, Step{
			Tag:         tag,
			Context:     inmate.FS,
			Dockerfile:  inmate.Image.Dockerfile,
			Args:        map[string]string{"PRISON_BASE": previousTag},
			Description: "layer for " + inmate.Name,
		})
		previousTag = tag
	}
	plan.Final = previousTag
	return plan, nil
}

// GuestBinaryPresent reports whether the guest binary exists at
// `baseDir` inside `assets`. Returns true if present, false otherwise.
func GuestBinaryPresent(assets fs.FS, baseDir string) bool {
	return contextHasGuestBinary(assets, path.Join(baseDir, guestBinaryName))
}

// contextHasGuestBinary reports whether `name` is a regular file in
// `fsys`.
func contextHasGuestBinary(fsys fs.FS, name string) bool {
	info, err := fs.Stat(fsys, name)
	return err == nil && info.Mode().IsRegular()
}

// isBaseStep reports whether a step builds a base image rather than
// an inmate layer.
func isBaseStep(step Step) bool {
	return strings.HasPrefix(step.Tag, BaseRepository+":")
}
