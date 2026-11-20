//go:build !unix

package hostgit

import "os"

func linkCount(os.FileInfo) uint64 { return 1 }
