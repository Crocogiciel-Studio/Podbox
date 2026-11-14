//go:build embedproxy

package images

import _ "embed"

//go:embed bin/podbox-linux-amd64
var proxyAMD64 []byte

//go:embed bin/podbox-linux-arm64
var proxyARM64 []byte

func init() {
	embedded["amd64"] = proxyAMD64
	embedded["arm64"] = proxyARM64
}
