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
	MirrorTypeUbuntu MirrorType = "ubuntu"
	MirrorTypeDebian MirrorType = "debian"
	MirrorTypeFedora MirrorType = "fedora"
	MirrorTypeArch   MirrorType = "arch"
	MirrorTypeNpm    MirrorType = "npm"
	MirrorTypeGo     MirrorType = "go"
	MirrorTypeCargo  MirrorType = "cargo"
	MirrorTypePypi   MirrorType = "pypi"
	MirrorTypeNuget  MirrorType = "nuget"
	MirrorTypeDocker MirrorType = "docker"
	MirrorTypeCentos MirrorType = "centos"
)

type MirrorData struct {
	Mirrors []Mirror `yaml:"mirrors"`
}

type MirrorService interface {
	CheckMirrorStatus(mirrorUrl string, verbose bool) (bool, *interface{}, error)
	CheckMirrorSpeed(mirrorURL string, verbose bool) (float64, *interface{}, error)
	CheckPackage(mirrorUrl, packageName string, verbose bool) (bool, *interface{}, error)
}
