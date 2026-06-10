package apt

import "fmt"

// InvalidMirrorError is returned when a mirror is unreachable or invalid.
type InvalidMirrorError struct {
	URL string
	Err error
}

func (e *InvalidMirrorError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf(
			"mirror '%s' is not responding or is not a valid apt mirror: %v",
			e.URL,
			e.Err,
		)
	}

	return fmt.Sprintf(
		"mirror '%s' is not responding or is not a valid apt mirror",
		e.URL,
	)
}

func (e *InvalidMirrorError) Unwrap() error {
	return e.Err
}

// HttpRequestError represents HTTP request failures.
type HttpRequestError struct {
	URL        string
	StatusCode int
	Err        error
}

func (e *HttpRequestError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("http request to '%s' failed: %v", e.URL, e.Err)
	}

	if e.StatusCode != 0 {
		return fmt.Sprintf(
			"http request to '%s' failed with status code %d",
			e.URL,
			e.StatusCode,
		)
	}

	return fmt.Sprintf("http request to '%s' failed", e.URL)
}

func (e *HttpRequestError) Unwrap() error {
	return e.Err
}

// PackageNotFoundError is returned when a package does not exist in a repository.
type PackageNotFoundError struct {
	Package   string
	Release   string
	Component string
	Arch      string
}

func (e *PackageNotFoundError) Error() string {
	return fmt.Sprintf(
		"package '%s' not found in release=%s component=%s arch=%s",
		e.Package,
		e.Release,
		e.Component,
		e.Arch,
	)
}

// ValidationError represents invalid user input.
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("validation failed for field '%s': %s", e.Field, e.Message)
}

// JsonParseError is returned when JSON parsing fails.
type JsonParseError struct {
	URL string
	Err error
}

func (e *JsonParseError) Error() string {
	return fmt.Sprintf("failed to parse json: %s", e.Err)
}

// ResponseReadError is returned when reading an HTTP response body fails.
type ResponseReadError struct {
	URL string
	Err error
}

func (e *ResponseReadError) Error() string {
	return fmt.Sprintf("failed to parse json: %s", e.Err)
}

// SpeedTestError is returned when a mirror speed test fails.
type SpeedTestError struct {
	URL string
	Err error
}

func (e *SpeedTestError) Error() string {
	return fmt.Sprintf("failed to speed test: %s", e.URL)
}

// TimeoutError is returned when an HTTP request times out.
type TimeoutError struct {
	URL     string
	Timeout int
	Err     error
}

func (e *TimeoutError) Error() string {
	return fmt.Sprintf("timeout %d", e.Timeout)
}
