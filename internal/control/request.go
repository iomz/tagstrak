package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
)

// Error is the structured error body. Messages never echo headers, tokens, or
// secret material.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Field   string `json:"field,omitempty"`
}

// WriteError writes {"error": {...}} with the given status.
func WriteError(w http.ResponseWriter, status int, code, message, field string) {
	WriteJSON(w, status, struct {
		Error Error `json:"error"`
	}{Error{Code: code, Message: message, Field: field}})
}

// WriteJSON writes v as a JSON response.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// RequireJSON enforces a JSON request content type and bounds the body to
// limit bytes. It writes 415 and returns false otherwise.
func RequireJSON(w http.ResponseWriter, r *http.Request, limit int64) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		WriteError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json", "")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	return true
}

// ReadJSON enforces the content type and body limit, then strictly decodes
// one JSON value into v: unknown fields and trailing data are rejected. It
// writes 415, 413 or 400 and returns false on failure.
func ReadJSON(w http.ResponseWriter, r *http.Request, limit int64, v any) bool {
	if !RequireJSON(w, r, limit) {
		return false
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			WriteError(w, http.StatusRequestEntityTooLarge, "body_too_large", "request body exceeds the limit", "")
			return false
		}
		WriteError(w, http.StatusBadRequest, "unreadable_body", "request body could not be read", "")
		return false
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		// A fixed message: decoder errors can repeat request content.
		WriteError(w, http.StatusBadRequest, "invalid_json", "request body is not a valid JSON document for this endpoint", "")
		return false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		WriteError(w, http.StatusBadRequest, "invalid_json", "trailing data after the JSON value", "")
		return false
	}
	return true
}

// Audit records one control event for an HTTP request. Callers pass only
// non-sensitive attributes: revisions, counts, error codes. Payloads,
// patterns, headers and tokens never belong here.
func Audit(logger *slog.Logger, r *http.Request, action, outcome string, attrs ...slog.Attr) {
	AuditRecord(r.Context(), logger, Actor(r), RequestID(r), action, outcome, attrs...)
}

// AuditRecord records a control event that did not come from a request, such
// as a signal-triggered reload.
func AuditRecord(ctx context.Context, logger *slog.Logger, actor, requestID, action, outcome string, attrs ...slog.Attr) {
	base := []slog.Attr{
		slog.String("event", "control"),
		slog.String("action", action),
		slog.String("outcome", outcome),
		slog.String("actor", actor),
	}
	if requestID != "" {
		base = append(base, slog.String("request_id", requestID))
	}
	logger.LogAttrs(ctx, slog.LevelInfo, "control audit", append(base, attrs...)...)
}
