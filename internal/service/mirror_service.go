package service

import (
	"fmt"

	"github.com/MiravaOrg/mirava-core"
)

type MiravaService struct {
	AptService    mirava_core.MirrorService[*interface{}, *interface{}, AptCheckPackageParams]
	NpmService    mirava_core.MirrorService[*interface{}, *NpmCheckSpeedParams, *interface{}]
	PypiService   mirava_core.MirrorService[*interface{}, *interface{}, *interface{}]
	DockerService mirava_core.MirrorService[*interface{}, *DockerSpeedParams, *interface{}]
}

func (m *MiravaService) CheckSpeed(mirrorURL string, timeout int, mirrorType mirava_core.MirrorType, verbose bool, params *interface{}) (float64, *interface{}, error) {
	switch mirrorType {
	case mirava_core.MirrorTypeApt:
		return m.AptService.CheckSpeed(mirrorURL, timeout, verbose, params)
	case mirava_core.MirrorTypeNpm:
		npmParams, ok := (*params).(*NpmCheckSpeedParams)
		if !ok {
			return -1, nil, fmt.Errorf("invalid parameters")
		}
		return m.NpmService.CheckSpeed(mirrorURL, timeout, verbose, npmParams)
	case mirava_core.MirrorTypePypi:
		return m.PypiService.CheckSpeed(mirrorURL, timeout, verbose, params)
	case mirava_core.MirrorTypeDocker:
		dockerParams, ok := (*params).(*DockerSpeedParams)
		if !ok {
			return -1, nil, fmt.Errorf("invalid parameters")
		}
		return m.DockerService.CheckSpeed(mirrorURL, timeout, verbose, dockerParams)
	}

	return 0, nil, fmt.Errorf("mirror type %s is not supported", mirrorType)
}

func (m *MiravaService) CheckStatus(mirrorURL string, mirrorType mirava_core.MirrorType, verbose bool) (bool, *interface{}, error) {
	switch mirrorType {
	case mirava_core.MirrorTypeApt:
		return m.AptService.CheckStatus(mirrorURL, verbose, nil)
	case mirava_core.MirrorTypeNpm:
		return m.NpmService.CheckStatus(mirrorURL, verbose, nil)
	case mirava_core.MirrorTypePypi:
		return m.PypiService.CheckStatus(mirrorURL, verbose, nil)
	case mirava_core.MirrorTypeDocker:
		return m.DockerService.CheckStatus(mirrorURL, verbose, nil)
	}

	return false, nil, fmt.Errorf("mirror type %s is not supported", mirrorType)
}

func (m *MiravaService) CheckPackage(mirrorURL string, packageName string, mirrorType mirava_core.MirrorType, verbose bool, params interface{}) (bool, *interface{}, error) {
	switch mirrorType {
	case mirava_core.MirrorTypeApt:
		aptParams, err := ValidateAptParams(params)
		if err != nil {
			return false, nil, err
		}
		return m.AptService.CheckPackage(mirrorURL, packageName, verbose, *aptParams)
	case mirava_core.MirrorTypeNpm:
		return m.NpmService.CheckPackage(mirrorURL, packageName, verbose, nil)
	case mirava_core.MirrorTypePypi:
		return m.PypiService.CheckPackage(mirrorURL, packageName, verbose, nil)
	case mirava_core.MirrorTypeDocker:
		return m.DockerService.CheckPackage(mirrorURL, packageName, verbose, nil)
	}

	return false, nil, fmt.Errorf("mirror type %s is not supported", mirrorType)
}

func CreateMiravaService() *MiravaService {
	return &MiravaService{
		NpmService:    NewNpmMirrorService(),
		PypiService:   NewPyPIMirrorService(),
		DockerService: NewDockerMirrorService(),
		AptService:    NewAptMirrorService(),
	}
}
