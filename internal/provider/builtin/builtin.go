// Package builtin links every built-in provider into the binary. Import it for its side effects.
package builtin

import (
	_ "router-go/internal/provider/cline"
	_ "router-go/internal/provider/codebuddy"
	_ "router-go/internal/provider/freebuff"
	_ "router-go/internal/provider/kiro"
	_ "router-go/internal/provider/opencode-free"
	_ "router-go/internal/provider/qoder"
)
