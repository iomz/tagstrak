package gosstrak

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	configfile "github.com/iomz/tagstrak/v2/internal/config"
	"github.com/iomz/tagstrak/v2/internal/control"
)

// ControlSettings are the management API's dependencies.
type ControlSettings struct {
	// Token authenticates requests; required for a TCP listener.
	Token []byte
	// Subscriptions is the live table the API reads and, in "api" mode,
	// replaces.
	Subscriptions *SubscriptionTable
	// Source is "file" or "api".
	Source string
	// Store persists replacements in "api" mode; nil in "file" mode.
	Store *StateStore
}

// WithControl enables the management API on the listener in Config.Control.
func WithControl(settings ControlSettings) Option {
	return func(a *App) {
		settings.Token = append([]byte(nil), settings.Token...)
		a.control = &settings
	}
}

// ControlAddress returns the bound control address once Run has opened it.
func (a *App) ControlAddress() string {
	if addr, ok := a.controlAddress.Load().(string); ok {
		return addr
	}
	return ""
}

type subscriptionDocument struct {
	Source        string                    `json:"source,omitempty"`
	Revision      uint64                    `json:"revision"`
	Subscriptions []configfile.Subscription `json:"subscriptions"`
}

type replaceRequest struct {
	// Subscriptions must be present: an explicit [] clears the set, while a
	// missing or null field is rejected so a malformed request cannot wipe it.
	Subscriptions *[]configfile.Subscription `json:"subscriptions"`
}

// controlHandler serves the management API:
//
//	GET /v1/status          the telemetry status document
//	GET /v1/subscriptions   current subscriptions, source and revision
//	PUT /v1/subscriptions   replace them atomically ("api" mode only)
//
// The revision is also the ETag; PUT honours If-Match.
func (a *App) controlHandler() http.Handler {
	settings := a.control
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/status", func(w http.ResponseWriter, r *http.Request) {
		control.WriteJSON(w, http.StatusOK, a.Status())
	})
	mux.HandleFunc("GET /v1/subscriptions", func(w http.ResponseWriter, r *http.Request) {
		spec, revision := settings.Subscriptions.Snapshot()
		w.Header().Set("ETag", etag(revision))
		control.WriteJSON(w, http.StatusOK, subscriptionDocument{Source: settings.Source, Revision: revision, Subscriptions: spec})
	})
	mux.HandleFunc("PUT /v1/subscriptions", func(w http.ResponseWriter, r *http.Request) {
		before := settings.Subscriptions.Revision()
		reject := func(status int, code, message, field string) {
			control.Audit(a.logger, r, "subscriptions.replace", outcomeFor(status),
				slog.Uint64("revision_before", before), slog.String("error_code", code))
			control.WriteError(w, status, code, message, field)
		}
		if settings.Source != configfile.SourceAPI || settings.Store == nil {
			reject(http.StatusConflict, "managed_by_file", "subscriptions are managed by the configuration file; edit it and send SIGHUP", "")
			return
		}
		expected, err := ifMatch(r.Header.Get("If-Match"))
		if err != nil {
			reject(http.StatusBadRequest, "invalid_precondition", err.Error(), "")
			return
		}
		var body replaceRequest
		if !control.ReadJSON(w, r, configfile.MaxFileBytes, &body) {
			control.Audit(a.logger, r, "subscriptions.replace", "rejected",
				slog.Uint64("revision_before", before), slog.String("error_code", "invalid_body"))
			return
		}
		if body.Subscriptions == nil {
			reject(http.StatusBadRequest, "invalid_subscriptions", "subscriptions is required; send [] to remove every subscription", "subscriptions")
			return
		}
		after, err := settings.Subscriptions.Replace(*body.Subscriptions, expected, settings.Store.Save)
		var fieldErr *FieldError
		switch {
		case errors.Is(err, ErrRevisionMismatch):
			reject(http.StatusPreconditionFailed, "revision_mismatch", "the subscriptions changed; fetch the current revision and retry", "")
			return
		case errors.As(err, &fieldErr):
			reject(http.StatusBadRequest, "invalid_subscriptions", fieldErr.Err.Error(), fieldErr.Field)
			return
		case errors.Is(err, configfile.ErrConfig):
			// The state file, which is indented, would exceed its limit.
			reject(http.StatusRequestEntityTooLarge, "state_too_large", "the subscriptions exceed the state file limit; nothing changed", "")
			return
		case err != nil:
			// The state file could not be written; nothing was published.
			a.logger.Error("subscription state not saved", "error", err)
			reject(http.StatusInternalServerError, "state_not_saved", "the subscription state could not be saved; nothing changed", "")
			return
		}
		control.Audit(a.logger, r, "subscriptions.replace", "ok",
			slog.Uint64("revision_before", before), slog.Uint64("revision_after", after), slog.Int("count", len(*body.Subscriptions)))
		w.Header().Set("ETag", etag(after))
		control.WriteJSON(w, http.StatusOK, struct {
			Revision uint64 `json:"revision"`
		}{after})
	})
	return mux
}

func outcomeFor(status int) string {
	switch status {
	case http.StatusConflict, http.StatusPreconditionFailed:
		return "conflict"
	default:
		return "rejected"
	}
}

func etag(revision uint64) string { return strconv.Quote(strconv.FormatUint(revision, 10)) }

// ifMatch parses an If-Match header holding one revision ETag. Absent and
// "*" mean unconditional.
func ifMatch(header string) (*uint64, error) {
	header = strings.TrimSpace(header)
	if header == "" || header == "*" {
		return nil, nil
	}
	tag := strings.TrimPrefix(header, "W/")
	if unquoted, err := strconv.Unquote(tag); err == nil {
		tag = unquoted
	}
	revision, err := strconv.ParseUint(tag, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("If-Match must be one revision ETag, such as \"3\"")
	}
	return &revision, nil
}
