//go:build !gui || freebsd || netbsd || openbsd

package main

// runGUI is unreachable without the `gui` build tag: main only dispatches
// here after guiAvailable() has returned true, and that is compiled to a
// constant false in this build. It exists so main.go needs no build tags of
// its own.
func runGUI(server, lang string, cfg cliConfig, token string) {}
