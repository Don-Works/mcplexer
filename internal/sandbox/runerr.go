package sandbox

import (
	"errors"
	"os/exec"
)

// filterRunErr unwraps *exec.ExitError into a nil so callers see only
// "real" errors (failed to launch, signaled, ctx canceled). The exit
// code is reported through the ExitCode return value either way.
//
// Lives in a build-tag-free file because every driver (darwin, linux,
// future) needs the same translation.
func filterRunErr(err error) error {
	if err == nil {
		return nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return nil
	}
	return err
}
