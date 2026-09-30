//go:build !unix && !windows

package filelock

import "os"

// Platforms without advisory locks (plan9, wasm) fall back to the in-process
// mutexes of the callers; running several servers on one state directory
// there is unsupported.
func lock(*os.File) error { return nil }

func unlock(*os.File) error { return nil }
