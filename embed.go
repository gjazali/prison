package prison

import "embed"

//go:embed images/base plugins/inmates
var Assets embed.FS

const BaseImageDir = "images/base"

const BundledInmatesDir = "plugins/inmates"

const GuestBinaryName = "prison-guest"

const KernelDir = "images/kernel"
