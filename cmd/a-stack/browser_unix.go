//go:build linux || darwin

package main

import (
	"os/exec"
	"runtime"
)

// openURL opens url with xdg-open on Linux and open on macOS.
func openURL(url string) error {
	opener := "xdg-open"
	if runtime.GOOS == "darwin" {
		opener = "open"
	}

	return exec.Command(opener, url).Run()
}
