package pkg

import "github.com/MiravaOrg/mirava-core/pkg/apt"

type (
	InvalidMirrorError   = apt.InvalidMirrorError
	HttpRequestError     = apt.HttpRequestError
	PackageNotFoundError = apt.PackageNotFoundError
	ValidationError      = apt.ValidationError
	JsonParseError       = apt.JsonParseError
	ResponseReadError    = apt.ResponseReadError
	SpeedTestError       = apt.SpeedTestError
	TimeoutError         = apt.TimeoutError
)
