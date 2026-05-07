package service

import (
	"fmt"

	"github.com/MiravaOrg/mirava-core"
)

type MiravaService struct {
	ubuntuService mirava_core.MirrorService
	debianService mirava_core.MirrorService
	npmService    mirava_core.MirrorService
	pypiService   mirava_core.MirrorService
	dockerService mirava_core.MirrorService
}

func (m *MiravaService) CheckMirrorSpeed(mirrorURL string, mirrorType mirava_core.MirrorType, verbose bool) (float64, *interface{}, error) {
	switch mirrorType {
	case mirava_core.MirrorTypeUbuntu:
		return m.ubuntuService.CheckMirrorSpeed(mirrorURL, verbose)
	case mirava_core.MirrorTypeDebian:
		return m.debianService.CheckMirrorSpeed(mirrorURL, verbose)
	case mirava_core.MirrorTypeNpm:
		return m.npmService.CheckMirrorSpeed(mirrorURL, verbose)
	case mirava_core.MirrorTypePypi:
		return m.pypiService.CheckMirrorSpeed(mirrorURL, verbose)
	case mirava_core.MirrorTypeDocker:
		return m.dockerService.CheckMirrorSpeed(mirrorURL, verbose)
	}

	return 0, nil, fmt.Errorf("mirror type %s is not supported", mirrorType)
}

func (m *MiravaService) CheckMirrorStatus(mirrorURL string, mirrorType mirava_core.MirrorType, verbose bool) (bool, *interface{}, error) {
	switch mirrorType {
	case mirava_core.MirrorTypeUbuntu:
		return m.ubuntuService.CheckMirrorStatus(mirrorURL, verbose)
	case mirava_core.MirrorTypeDebian:
		return m.debianService.CheckMirrorStatus(mirrorURL, verbose)
	case mirava_core.MirrorTypeNpm:
		return m.npmService.CheckMirrorStatus(mirrorURL, verbose)
	case mirava_core.MirrorTypePypi:
		return m.pypiService.CheckMirrorStatus(mirrorURL, verbose)
	case mirava_core.MirrorTypeDocker:
		return m.dockerService.CheckMirrorStatus(mirrorURL, verbose)
	}

	return false, nil, fmt.Errorf("mirror type %s is not supported", mirrorType)
}

func (m *MiravaService) CheckPackage(mirrorURL string, packageName string, mirrorType mirava_core.MirrorType, verbose bool) (bool, *interface{}, error) {
	switch mirrorType {
	case mirava_core.MirrorTypeUbuntu:
		return m.ubuntuService.CheckPackage(mirrorURL, packageName, verbose)
	case mirava_core.MirrorTypeDebian:
		return m.debianService.CheckPackage(mirrorURL, packageName, verbose)
	case mirava_core.MirrorTypeNpm:
		return m.npmService.CheckPackage(mirrorURL, packageName, verbose)
	case mirava_core.MirrorTypePypi:
		return m.pypiService.CheckPackage(mirrorURL, packageName, verbose)
	case mirava_core.MirrorTypeDocker:
		return m.dockerService.CheckPackage(mirrorURL, packageName, verbose)
	}

	return false, nil, fmt.Errorf("mirror type %s is not supported", mirrorType)
}

func CreateMiravaService() *MiravaService {
	return &MiravaService{
		ubuntuService: NewUbuntuMirrorService(),
		debianService: NewDebianMirrorService(),
		npmService:    NewNpmMirrorService(),
		pypiService:   NewPyPIMirrorService(),
		dockerService: NewDockerMirrorService(),
	}
}
