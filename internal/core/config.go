package core

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/MiravaOrg/mirava-core/internal/cache"
	"github.com/MiravaOrg/mirava-core/internal/httpx"
)

// Config is the shared wiring handed to every backend: HTTP client,
// logger, cache store. Backends must not outlive the process config; they
// read it once at construction.
type Config struct {
	HTTPClient *http.Client
	Logger     *slog.Logger
	Store      cache.Store
}

// Client returns the configured client or the shared default.
func (c Config) Client() *http.Client { return httpx.ClientOr(c.HTTPClient) }

// Log returns the configured logger or a discard logger.
func (c Config) Log() *slog.Logger {
	if c.Logger != nil {
		return c.Logger
	}
	return discardLogger
}

var discardLogger = slog.New(discardHandler{})

type discardHandler struct{}

func (discardHandler) Enabled(context.Context, slog.Level) bool  { return false }
func (discardHandler) Handle(context.Context, slog.Record) error { return nil }
func (discardHandler) WithAttrs([]slog.Attr) slog.Handler        { return discardHandler{} }
func (discardHandler) WithGroup(string) slog.Handler             { return discardHandler{} }
