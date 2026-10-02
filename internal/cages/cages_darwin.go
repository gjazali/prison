package cages

import (
	"prison/internal/cage"
	"prison/internal/cage/applecontainer"
)

const DefaultName = "apple-container"

var registered = []registration{{
	name:        DefaultName,
	constructor: func(Options) cage.Cage { return applecontainer.New() },
}}
