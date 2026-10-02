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

const shortHashLength = 12

// guestBinaryName must be in the base image context. `make build` puts it
// there before the assets are embedded.
const guestBinaryName = "prison-guest"

// ShortHash ends each part with a NUL byte so that different part lists
// give different hashes.
func ShortHash(parts ...string) string {
	digest := sha256.New()
	for _, part := range parts {
		digest.Write([]byte(part))
		digest.Write([]byte{0})
	}
	return hex.EncodeToString(digest.Sum(nil))[:shortHashLength]
}

type Step struct {
	Tag         string
	Context     fs.FS
	Dockerfile  string
	Args        map[string]string
	Description string
}

type Plan struct {
	Foundation string
	Steps      []Step
	Final      string
}

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
			return nil, fmt.Errorf("inmate %s has no image context", inmate.Name)
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

func GuestBinaryPresent(assets fs.FS, baseDir string) bool {
	return contextHasGuestBinary(assets, path.Join(baseDir, guestBinaryName))
}

func contextHasGuestBinary(fsys fs.FS, name string) bool {
	info, err := fs.Stat(fsys, name)
	return err == nil && info.Mode().IsRegular()
}

func isBaseStep(step Step) bool {
	return strings.HasPrefix(step.Tag, BaseRepository+":")
}
