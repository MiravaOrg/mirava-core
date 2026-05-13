package mirava

import "github.com/MiravaOrg/mirava-core/pkg"

func CreateMiravaService() *pkg.MiravaService {
	return &pkg.MiravaService{
		NpmService:    pkg.NewNpmMirrorService(),
		PypiService:   pkg.NewPyPIMirrorService(),
		DockerService: pkg.NewDockerMirrorService(),
		AptService:    pkg.NewAptMirrorService(),
	}
}
