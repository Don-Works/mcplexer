//go:build !darwin || !cgo

package main

import "log/slog"

// requestAccessibility is a no-op on every build that isn't darwin with
// cgo. Linux + Windows daemons have no equivalent system permission
// model, and cross-compiled darwin builds without cgo (the
// `GOOS=darwin GOARCH=amd64` cross from an arm64 host in our Makefile)
// can't link the ApplicationServices framework — those builds are for
// distribution to other machines, where the user's local install path
// will recompile with cgo enabled via `make install` / `make upgrade`.
func requestAccessibility(_ *slog.Logger) {}
