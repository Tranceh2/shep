//go:build linux

package command

import "golang.org/x/sys/unix"

const (
	getTermiosReq = unix.TCGETS
	setTermiosReq = unix.TCSETS
)
