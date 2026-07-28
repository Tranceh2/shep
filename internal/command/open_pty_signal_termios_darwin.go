//go:build darwin

package command

import "golang.org/x/sys/unix"

const (
	getTermiosReq = unix.TIOCGETA
	setTermiosReq = unix.TIOCSETA
)
