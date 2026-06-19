//go:build linux

package sandbox

// candidates on linux prefers bubblewrap (best isolation, supports
// user-ns + bind-mounts) and falls back to unshare (worse but ships
// with util-linux on practically every distro).
func candidates() []Driver {
	return []Driver{&bwrapDriver{}, &unshareDriver{}}
}
