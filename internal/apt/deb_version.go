package apt

import (
	"bytes"
	"fmt"
	"os/exec"

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

// xzDecompress decompresses xz data by shelling out to xz, which is present
// on every Debian/Ubuntu system where apt indexes are consumed. A pure-Go
// decoder can replace this later without changing callers.
func xzDecompress(data []byte) ([]byte, error) {
	cmd := exec.Command("xz", "-d", "--single-stream")
	cmd.Stdin = bytes.NewReader(data)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("xz: %w", err)
	}
	return out, nil
}
