//go:build windows

package bash

import (
	"os/exec"
	"time"
)

func configureProcess(command *exec.Cmd) {
	command.WaitDelay = 2 * time.Second
}
