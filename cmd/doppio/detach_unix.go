//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

// detach starts the daemon in a new session so it outlives the CLI.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
