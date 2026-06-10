package pkg

import "github.com/MiravaOrg/mirava-core/pkg/apt"

type (
	AptMirrorService         = apt.AptMirrorService
	AptCheckStatusData       = apt.AptCheckStatusData
	AptCheckSpeedData        = apt.AptCheckSpeedData
	AptCheckPackageParams    = apt.AptCheckPackageParams
	AptCheckPackageData      = apt.AptCheckPackageData
	AptPackageVersionData    = apt.AptPackageVersionData
	AptPackageVersionSearch  = apt.AptPackageVersionSearch
)

// NewAptMirrorService creates a new APT mirror service instance.
func NewAptMirrorService() *AptMirrorService {
	return apt.NewAptMirrorService()
}
