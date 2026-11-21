package main

import (
	"fmt"
	"syscall"
)

func waitForProcess(pid int) error {
	const synchronize = 0x00100000
	handle, err := syscall.OpenProcess(synchronize, false, uint32(pid))
	if err != nil {
		if err == syscall.Errno(87) {
			return nil
		}
		return err
	}
	defer syscall.CloseHandle(handle)
	result, err := syscall.WaitForSingleObject(handle, 20000)
	if err != nil {
		return err
	}
	if result != syscall.WAIT_OBJECT_0 {
		return fmt.Errorf("timed out waiting for plugin hook process %d", pid)
	}
	return nil
}
