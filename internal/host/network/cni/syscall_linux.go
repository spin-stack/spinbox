//go:build linux

package cni

import (
	"golang.org/x/sys/unix"
)

// mount wraps the mount syscall.
func mount(source string, target string, fstype string, flags uintptr, data string) error {
	return unix.Mount(source, target, fstype, flags, data)
}

// unmount wraps the unmount syscall.
func unmount(target string, flags int) error {
	return unix.Unmount(target, flags)
}
