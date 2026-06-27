//go:build !linux

package main

import "fmt"

func systemdUserAvailable() bool { return false }
func systemdUserInstalled() bool { return false }
func installSystemdUser(_, _, _ string) error {
	return fmt.Errorf("systemd user services are only supported on Linux")
}
func uninstallSystemdUser() error {
	return fmt.Errorf("systemd user services are only supported on Linux")
}
func systemdUserStart() error { return fmt.Errorf("systemd user services are only supported on Linux") }
func systemdUserStop() error  { return fmt.Errorf("systemd user services are only supported on Linux") }
func systemdUserStatus() (bool, error) {
	return false, fmt.Errorf("systemd user services are only supported on Linux")
}
func readSystemdUserAddr() string { return "" }
