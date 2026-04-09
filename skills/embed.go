package skills

import "embed"

// BundledCommands contains all skill files that should be installed as
// Claude Code custom commands (~/.claude/commands/).
//
//go:embed *.md
var BundledCommands embed.FS

// BundledSkills contains skill directories that should be installed to
// ~/.claude/skills/. Each subdirectory becomes a skill (e.g. subagent-driven-development/).
//
//go:embed bundled-skills/*
var BundledSkills embed.FS
