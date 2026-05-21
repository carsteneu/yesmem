package plugins

import "embed"

// BundledOpencodePlugin contains the TypeScript plugin source files
// for the opencode YesMem plugin. Installed to
// ~/.local/share/yesmem/plugins/opencode-yesmem/ during setup.
//
//go:embed opencode-yesmem/*
var BundledOpencodePlugin embed.FS
