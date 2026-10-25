//go:build unix

package main

import "syscall"

// restrictUmask keeps everything gopress creates (database, WAL files,
// backups, imported uploads) unreadable for other users of the host.
func restrictUmask() { syscall.Umask(0o027) }
