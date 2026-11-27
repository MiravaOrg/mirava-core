package core

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// Sentinel errors. Callers should test with errors.Is, not by type-asserting
// the concrete wrappers below.
var (
	// ErrNotFound is returned when a requested package/release does not
	// exist on the mirror. Note: Package probes report a missing package as
	// a result (PackageResult.Found == false), not as this error.
	ErrNotFound = errors.New("not found")
	// ErrTimeout is returned when a probe exceeded its context deadline.
	ErrTimeout = errors.New("timeout")
	// ErrInvalidMirror is returned when a URL is not a usable mirror of the
	// expected kind (wrong scheme, not reachable, or not behaving like the
	// expected ecosystem).
	ErrInvalidMirror = errors.New("invalid mirror")
	// ErrUnreachable is returned when the mirror host could not be reached
	// at all (DNS failure, connection refused, network down).
	ErrUnreachable = errors.New("unreachable")
	// ErrNotImplemented is returned for mirror types that have no backend
	// yet (yum, dnf, nuget) or options the backend does not support.
	ErrNotImplemented = errors.New("not implemented")
)

// RequestError reports a failed HTTP request. Err wraps either a transport
// error (network layer) or nothing when the failure is status-based.
type RequestError struct {
	URL        string
	StatusCode int // 0 when the request did not complete
	Err        error
}

func (e *RequestError) Error() string {
	switch {
	case e.Err != nil:
		return fmt.Sprintf("request to %s failed: %v", e.URL, e.Err)
	case e.StatusCode != 0:
		return fmt.Sprintf("request to %s failed with status %d", e.URL, e.StatusCode)
	default:
		return fmt.Sprintf("request to %s failed", e.URL)
	}
}

func (e *RequestError) Unwrap() error {
	if e.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	return e.Err
}

// StatusError reports an unexpected HTTP status on a completed response.
type StatusError struct {
	URL        string
	StatusCode int
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("unexpected status %d from %s", e.StatusCode, e.URL)
}

func (e *StatusError) Unwrap() error {
	if e.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	return ErrUnreachable
}

// ValidationError reports bad caller input.
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("validation failed for field %q: %s", e.Field, e.Message)
}

// ParseError reports a response body that could not be decoded.
type ParseError struct {
	URL  string
	Kind string // e.g. "json", "gzip", "index"
	Err  error
}

func (e *ParseError) Error() string {
	if e.Kind != "" {
		return fmt.Sprintf("failed to parse %s from %s: %v", e.Kind, e.URL, e.Err)
	}
	return fmt.Sprintf("failed to parse response from %s: %v", e.URL, e.Err)
}

func (e *ParseError) Unwrap() error { return e.Err }

// SpeedError reports a speed measurement that produced no usable sample.
type SpeedError struct {
	URL     string
	Bytes   int64
	Elapsed time.Duration
	Err     error
}

func (e *SpeedError) Error() string {
	return fmt.Sprintf("speed test of %s produced no usable sample (%d bytes in %s)",
		e.URL, e.Bytes, e.Elapsed)
}

func (e *SpeedError) Unwrap() error { return e.Err }

// timeoutError wraps a context deadline so callers can match either the
// sentinel or the raw context error.
type timeoutError struct{ err error }

func (e *timeoutError) Error() string { return "probe timed out: " + e.err.Error() }
func (e *timeoutError) Unwrap() error { return e.err }

// AsError wraps ctx errors (deadline exceeded, canceled) so that both
// errors.Is(err, mirava.ErrTimeout) and errors.Is(err, context.DeadlineExceeded)
// hold.
func AsError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &timeoutError{err: err}
	}
	return err
}
