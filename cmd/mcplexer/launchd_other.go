//go:build !darwin

package main

import "fmt"

func launchdPlistPath() string            { return "" }
func launchdInstalled() bool              { return false }
func installLaunchd(_, _, _ string) error { return fmt.Errorf("launchd is only supported on macOS") }
func uninstallLaunchd() error             { return fmt.Errorf("launchd is only supported on macOS") }
func launchdStart() error                 { return fmt.Errorf("launchd is only supported on macOS") }
func launchdStop() error                  { return fmt.Errorf("launchd is only supported on macOS") }
func launchdStatus() (bool, error)        { return false, fmt.Errorf("launchd is only supported on macOS") }
