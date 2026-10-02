package cages

import (
	"io/fs"

	"prison"
	"prison/internal/cage"
	"prison/internal/cage/firecracker"
)

const DefaultName = "aws-firecracker"

var registered = []registration{{
	name: DefaultName,
	constructor: func(options Options) cage.Cage {
		kernel, err := fs.Sub(prison.KernelAssets, prison.KernelDir)
		if err != nil {
			panic(err)
		}
		return firecracker.New(options.StateDirectory, kernel)
	},
}}
