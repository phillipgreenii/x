package jsonllogger

import (
	"io"
	"log/slog"
)

// Option customizes New, NewHandler and NewLogger.
type Option func(*config)

type config struct {
	service  string
	level    slog.Leveler
	renames  map[string]string
	writer   io.Writer
	maxBytes int64
}

// WithService stamps every line with a top-level "service" attribute, so lines
// from several processes sharing one stderr file or one log shipper can be told
// apart.
func WithService(name string) Option { return func(c *config) { c.service = name } }

// WithLevel sets the minimum level (default slog.LevelInfo). A *slog.LevelVar
// lets the caller change it at runtime.
func WithLevel(l slog.Leveler) Option { return func(c *config) { c.level = l } }

// WithKeyRenames renames top-level attribute keys after the package's
// normalization, for an app that must keep an existing dialect (for example
// {"time": "ts", "msg": "event"}). Group attributes are never renamed.
func WithKeyRenames(m map[string]string) Option {
	return func(c *config) {
		if c.renames == nil {
			c.renames = map[string]string{}
		}
		for k, v := range m {
			c.renames[k] = v
		}
	}
}

// WithWriter makes New write to w (for example os.Stderr, which launchd
// redirects to a file) instead of the per-app XDG file.
func WithWriter(w io.Writer) Option { return func(c *config) { c.writer = w } }

func newConfig(opts []Option) *config {
	c := &config{level: slog.LevelInfo}
	for _, o := range opts {
		o(c)
	}
	return c
}

// NewHandler returns a JSONL handler over w with the package's contract
// (lowercase level, UTC time) plus any options.
func NewHandler(w io.Writer, opts ...Option) slog.Handler {
	c := newConfig(opts)
	h := slog.Handler(slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level: c.level,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			a = ReplaceAttr(groups, a)
			if len(groups) == 0 {
				if to, ok := c.renames[a.Key]; ok {
					a.Key = to
				}
			}
			return a
		},
	}))
	if c.service != "" {
		h = h.WithAttrs([]slog.Attr{slog.String("service", c.service)})
	}
	return h
}

// NewLogger is NewHandler wrapped in a *slog.Logger.
func NewLogger(w io.Writer, opts ...Option) *slog.Logger {
	return slog.New(NewHandler(w, opts...))
}

// WithMaxBytes caps the per-app log file used by New: past n bytes the file is
// renamed to <app>.jsonl.1 and a fresh one started. Zero (the default) means
// no cap. It has no effect with WithWriter.
func WithMaxBytes(n int64) Option { return func(c *config) { c.maxBytes = n } }
