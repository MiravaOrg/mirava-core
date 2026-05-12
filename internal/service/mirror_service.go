package service

import (
	"github.com/MiravaOrg/mirava-core"
)

type MiravaService struct {
	AptService    mirava_core.MirrorService[*interface{}, *interface{}, AptCheckPackageParams]
	NpmService    mirava_core.MirrorService[*interface{}, *NpmCheckSpeedParams, *interface{}]
	PypiService   mirava_core.MirrorService[*interface{}, *interface{}, *interface{}]
	DockerService mirava_core.MirrorService[*interface{}, *DockerSpeedParams, *interface{}]
}

func CreateMiravaService() *MiravaService {
	return &MiravaService{
		NpmService:    NewNpmMirrorService(),
		PypiService:   NewPyPIMirrorService(),
		DockerService: NewDockerMirrorService(),
		AptService:    NewAptMirrorService(),
	}
}
