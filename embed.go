// Package rig embeds the built-in agent files, so the binary works in any directory.
package rig

import "embed"

//go:embed .rig/agents/*.md
var BuiltinAgents embed.FS
