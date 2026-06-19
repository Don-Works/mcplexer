//go:build darwin

package sandbox

// candidates on darwin returns the macOS sandbox-exec driver. There is
// no Linux fallback to consider here.
func candidates() []Driver {
	return []Driver{&sandboxExecDriver{}}
}
