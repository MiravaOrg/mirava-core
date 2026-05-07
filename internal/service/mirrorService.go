package service

import (
	"fmt"

	"github.com/MiravaOrg/mirava-core/internal/model"
)

type MiravaService struct {
	ubuntuService model.MirrorService
	debianService model.MirrorService
	npmService    model.MirrorService
	pypiService   model.MirrorService
	dockerService model.MirrorService
}

func (m *MiravaService) CheckMirrorSpeed(mirrorURL string, mirrorType model.MirrorType, verbose bool) (float64, error) {
	switch mirrorType {
	case model.MirrorTypeUbuntu:
		return m.ubuntuService.CheckMirrorSpeed(mirrorURL, verbose)
	case model.MirrorTypeDebian:
		return m.debianService.CheckMirrorSpeed(mirrorURL, verbose)
	case model.MirrorTypeNpm:
		return m.npmService.CheckMirrorSpeed(mirrorURL, verbose)
	case model.MirrorTypePypi:
		return m.pypiService.CheckMirrorSpeed(mirrorURL, verbose)
	case model.MirrorTypeDocker:
		return m.dockerService.CheckMirrorSpeed(mirrorURL, verbose)
	}

	return 0, fmt.Errorf("mirror type %s is not supported", mirrorType)
}

func (m *MiravaService) CheckMirrorStatus(mirrorURL string, mirrorType model.MirrorType, verbose bool) (bool, error) {
	switch mirrorType {
	case model.MirrorTypeUbuntu:
		return m.ubuntuService.CheckMirrorStatus(mirrorURL, verbose)
	case model.MirrorTypeDebian:
		return m.debianService.CheckMirrorStatus(mirrorURL, verbose)
	case model.MirrorTypeNpm:
		return m.npmService.CheckMirrorStatus(mirrorURL, verbose)
	case model.MirrorTypePypi:
		return m.pypiService.CheckMirrorStatus(mirrorURL, verbose)
	case model.MirrorTypeDocker:
		return m.dockerService.CheckMirrorStatus(mirrorURL, verbose)
	}

	return false, fmt.Errorf("mirror type %s is not supported", mirrorType)
}

func (m *MiravaService) CheckPackage(mirrorURL string, packageName string, mirrorType model.MirrorType, verbose bool) (bool, string, error) {
	switch mirrorType {
	case model.MirrorTypeUbuntu:
		return m.ubuntuService.CheckPackage(mirrorURL, packageName, verbose)
	case model.MirrorTypeDebian:
		return m.debianService.CheckPackage(mirrorURL, packageName, verbose)
	case model.MirrorTypeNpm:
		return m.npmService.CheckPackage(mirrorURL, packageName, verbose)
	case model.MirrorTypePypi:
		return m.pypiService.CheckPackage(mirrorURL, packageName, verbose)
	case model.MirrorTypeDocker:
		return m.dockerService.CheckPackage(mirrorURL, packageName, verbose)
	}

	return false, "-1", fmt.Errorf("mirror type %s is not supported", mirrorType)
}

func CreateMiravaService() *MiravaService {
	return &MiravaService{
		ubuntuService: CreateUbuntuMirrorService(),
		debianService: CreateDebianMirrorService(),
		npmService:    CreateNpmMirrorService(),
		pypiService:   CreatePyPIMirrorService(),
		dockerService: CreateDockerMirrorService(),
	}
}
