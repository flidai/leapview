// Package typespec embeds the APIGen TypeSpec emitter package for the Go CLI.
package typespec

import "embed"

// Package contains the files needed to run the APIGen TypeSpec emitter from an
// installed Go binary.
//
//go:embed package.json package-lock.json lib/* dist/src/*
var Package embed.FS
