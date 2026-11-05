//go:build gui && !freebsd && !netbsd && !openbsd

package main

import (
	"github.com/apimgr/cairnbin/src/client/gui"
)

// guiAvailable reports whether a native GUI can actually be launched right
// now: this binary was built with the `gui` tag, the OS has a windowing
// backend (gogpu/ui ships none for netbsd/openbsd), and a display is present
// and this is not a remote session.
func guiAvailable() bool {
	return gui.IsGUIAvailable()
}
