package apt

import (
	"bytes"
	"fmt"
	"io"

	"github.com/ulikunitz/xz"
	version "github.com/knqyf263/go-deb-version"
)

// debVersionGreater compares Debian versions using go-deb-version, falling
// back to lexicographic order when either side fails to parse.
func debVersionGreater(left, right string) bool {
	if right == "" {
		return left != ""
	}
	if left == "" {
		return false
	}
	lv, lerr := version.NewVersion(left)
	rv, rerr := version.NewVersion(right)
	if lerr != nil || rerr != nil {
		return left > right
	}
	return lv.GreaterThan(rv)
}

// xzDecompress decompresses xz data in pure Go via github.com/ulikunitz/xz.
// Shelling out to the xz binary is not portable: minimal containers (alpine
// images without the xz package) lack the binary, which silently broke apt
// package lookups there while they worked on dev machines.
func xzDecompress(data []byte) ([]byte, error) {
	r, err := xz.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("xz: %w", err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("xz: %w", err)
	}
	return out, nil
}
