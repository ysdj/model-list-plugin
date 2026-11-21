//go:build !windows

package main

import (
	"errors"
	"fmt"
	"syscall"
	"time"
)

func waitForProcess(pid int) error {
	deadline := time.Now().Add(20 * time.Second)
	for {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		if err != nil {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for plugin hook process %d", pid)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
