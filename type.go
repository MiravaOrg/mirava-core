package mirava_core

type Mirror struct {
	Name        string     `yaml:"name"`
	URL         string     `yaml:"url"`
	Description string     `yaml:"description"`
	MirrorType  MirrorType `yaml:"mirror_type"`
	Packages    []string   `yaml:"packages"`
}
type MirrorType string

const (
	MirrorTypeApt      MirrorType = "apt"      // Debian, Ubuntu, Linux Mint, Kali, etc.
	MirrorTypeYum      MirrorType = "yum"      // CentOS, RHEL, Rocky, AlmaLinux, Fedora
	MirrorTypeDnf      MirrorType = "dnf"      // Fedora, RHEL 8+, CentOS 8+ (could merge with yum)
	MirrorTypePacman   MirrorType = "pacman"   // Arch Linux, Manjaro, EndeavourOS
	MirrorTypeNpm      MirrorType = "npm"      // Node.js packages
	MirrorTypeGo       MirrorType = "go"       // Go modules
	MirrorTypeCargo    MirrorType = "cargo"    // Rust packages
	MirrorTypePypi     MirrorType = "pypi"     // Python packages
	MirrorTypeNuget    MirrorType = "nuget"    // .NET packages
	MirrorTypeDocker   MirrorType = "docker"   // Docker images
	MirrorTypeComposer MirrorType = "composer" // PHP Packages
)

type MirrorData struct {
	Mirrors []Mirror `yaml:"mirrors"`
}

type CheckPackageParams interface{}

type MirrorService[StatusInputType any, CheckSpeedInput any, CheckPackageInput any] interface {
	CheckStatus(mirrorUrl string, verbose bool, params StatusInputType) (bool, *interface{}, error)
	CheckSpeed(mirrorURL string, timeout int, verbose bool, params CheckSpeedInput) (float64, *interface{}, error)
	CheckPackage(mirrorUrl, packageName string, verbose bool, params CheckPackageInput) (bool, *interface{}, error)
}
