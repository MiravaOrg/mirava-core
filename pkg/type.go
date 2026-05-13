package pkg

type MirrorType string

// Supported mirror ecosystems for mirava-core.
// Each value should eventually have a dedicated MirrorService implementation in pkg/.
const (
	MirrorTypeApt      MirrorType = "apt"      // Debian, Ubuntu, and derivatives (deb repositories)
	MirrorTypeYum      MirrorType = "yum"      // RHEL-family RPM repositories (yum / repodata)
	MirrorTypeDnf      MirrorType = "dnf"      // DNF-compatible RPM mirrors (often same layout as yum)
	MirrorTypePacman   MirrorType = "pacman"   // Arch Linux sync databases / package mirrors
	MirrorTypeNpm      MirrorType = "npm"      // npm registry mirrors
	MirrorTypeGo       MirrorType = "go"       // Go module proxy / sumdb mirrors (GOPROXY-style)
	MirrorTypeCargo    MirrorType = "cargo"    // crates.io / sparse index mirrors
	MirrorTypePypi     MirrorType = "pypi"     // Python package index mirrors (PEP 503 / simple)
	MirrorTypeNuget    MirrorType = "nuget"    // NuGet V3 feed mirrors
	MirrorTypeDocker   MirrorType = "docker"   // OCI distribution / Docker registry mirrors
	MirrorTypeComposer MirrorType = "composer" // Composer / Packagist mirrors (PHP)
)

type MiravaService struct {
	AptService    MirrorService[*interface{}, *interface{}, AptCheckPackageParams]
	NpmService    MirrorService[*interface{}, *NpmCheckSpeedParams, *interface{}]
	PypiService   MirrorService[*interface{}, *interface{}, *interface{}]
	DockerService MirrorService[*interface{}, *DockerSpeedParams, *interface{}]
}

type MirrorService[StatusInputType any, CheckSpeedInput any, CheckPackageInput any] interface {
	CheckStatus(mirrorUrl string, verbose bool, params StatusInputType) (bool, *interface{}, error)
	CheckSpeed(mirrorURL string, timeout int, verbose bool, params CheckSpeedInput) (float64, *interface{}, error)
	CheckPackage(mirrorUrl, packageName string, verbose bool, params CheckPackageInput) (bool, *interface{}, error)
}
