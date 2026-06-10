package pkg

import "github.com/MiravaOrg/mirava-core/pkg/aptcore"

type (
	InvalidMirrorError   = aptcore.InvalidMirrorError
	HttpRequestError     = aptcore.HttpRequestError
	PackageNotFoundError = aptcore.PackageNotFoundError
	ValidationError      = aptcore.ValidationError
	JsonParseError       = aptcore.JsonParseError
	ResponseReadError    = aptcore.ResponseReadError
	SpeedTestError       = aptcore.SpeedTestError
	TimeoutError         = aptcore.TimeoutError
)
