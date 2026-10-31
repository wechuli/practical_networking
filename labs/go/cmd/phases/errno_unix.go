//go:build unix

package main

import "syscall"

// Socket errors as the kernel reports them; errors.Is unwraps Go's
// *net.OpError and *os.SyscallError down to these.
var (
	errConnRefused  error = syscall.ECONNREFUSED
	errConnReset    error = syscall.ECONNRESET
	errHostUnreach  error = syscall.EHOSTUNREACH
	errNetUnreach   error = syscall.ENETUNREACH
	errAddrNotAvail error = syscall.EADDRNOTAVAIL
)
