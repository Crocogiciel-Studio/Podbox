package workspace

import "io/fs"

// fileID has no inode to offer on Windows: masks there follow patterns and
// recorded paths only, so a renamed parent directory is not tracked.
func fileID(fs.FileInfo) (dev, ino uint64, ok bool) { return 0, 0, false }

// nonBlock is not needed: Windows has no FIFOs in the file system.
const nonBlock = 0
