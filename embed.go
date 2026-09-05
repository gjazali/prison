// Package prison is the module root. It embeds the base image and
// bundled inmate plugins into the host binary.
package prison

import "embed"

// Assets holds the base image context and bundled inmates from build
// time.
//
//go:embed images/base plugins/inmates
var Assets embed.FS

// BaseImageDir is the path to the base image context in Assets.
const BaseImageDir = "images/base"

// BundledInmatesDir is the path to the bundled inmates in Assets.
const BundledInmatesDir = "plugins/inmates"

// GuestBinaryName is the file name of the guest agent binary.
const GuestBinaryName = "prison-guest"
