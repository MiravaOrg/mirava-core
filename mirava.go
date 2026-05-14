package mirava

import "github.com/MiravaOrg/mirava-core/pkg"

func CreateMiravaService() *pkg.MiravaService {
	return &pkg.MiravaService{
		Npm:      pkg.NewNpmMirrorService(),
		PyPi:     pkg.NewPyPIMirrorService(),
		Docker:   pkg.NewDockerMirrorService(),
		Apt:      pkg.NewAptMirrorService(),
		Pacman:   pkg.NewPacmanMirrorService(),
		Go:       pkg.NewGoMirrorService(),
		Cargo:    pkg.NewCargoMirrorService(),
		Composer: pkg.NewComposerMirrorService(),
	}
}
