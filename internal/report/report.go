// Package report delivers match reports to destinations through bounded,
// per-destination queues, batching and bounded retries.
//
// # Delivery contract
//
// Delivery is at-least-once for events accepted into a queue, while the
// process runs and the destination recovers within the retry budget. Nothing
// survives a crash; exactly-once is not offered. A retry after a timeout can
// repeat a batch the destination already committed, so receivers deduplicate
// on Event.ID.
//
// Events reach each destination in the order they were enqueued: one batch is
// in flight per destination, and retries hold that destination's queue. There
// is no ordering across destinations.
//
// Events are lost only on queue overflow, final failure, crash, or the
// shutdown deadline. The first two are counted per destination and logged;
// the last is returned from Dispatcher.Close as ErrUndelivered. None is silent.
//
// The package imports only the standard library. Events carry values rendered
// at the application boundary, so destination adapters never depend on
// identity or translation internals.
package report

import (
	"context"
	"errors"
	"time"
)

// Schema names the JSON wire format of a batch. Additive fields keep the
// name; changing the meaning of a field needs a new one.
const Schema = "tagstrak.report.v1"

// Errors returned by the package.
var (
	ErrConfig      = errors.New("report: invalid configuration")
	ErrUndelivered = errors.New("report: undelivered events at shutdown")
)

// Identity is the canonical identity of a tag, as reported.
type Identity struct {
	// Authority is the numbering authority, such as "gs1" or "iso15459".
	Authority string
	// KeyType is the kind of business key, such as "sgtin".
	KeyType string
	// Components holds the identity's named fields.
	Components map[string]string
}

// Event reports one observation to one destination.
type Event struct {
	// ID identifies the observation. It is unique across process restarts
	// and is the key receivers deduplicate on.
	ID string
	// Source names the reader the observation came from.
	Source string
	// ObservedAt is when the reader saw the tag.
	ObservedAt time.Time
	// Subscriptions lists the matched subscription IDs routed to this
	// destination, in subscription order.
	Subscriptions []string
	// Scheme is the encoding scheme, such as "sgtin-96".
	Scheme string
	// EPC holds the encoded tag bits as read.
	EPC []byte
	// Identity is the canonical identity.
	Identity Identity
	// PureIdentityURI is the EPC Pure Identity URI, or empty when the
	// identity has no standards-backed textual form, as for ISO identities.
	PureIdentityURI string
}

// Batch is one delivery to one destination.
type Batch struct {
	// ID identifies the batch; adapters may send it as an idempotency key.
	ID string
	// Events are the batch contents, in enqueue order.
	Events []Event

	body []byte
}

// JSON returns the batch encoded in the Schema wire format. The dispatcher
// encodes each event once while batching, so adapters should send this body
// rather than re-encoding Events. The result must not be modified.
func (b Batch) JSON() []byte { return b.body }

// Reporter delivers batches to one destination.
//
// Deliver must honour ctx cancellation. It returns nil on success, an error
// wrapped by Permanent when retrying cannot succeed, and any other error when
// a retry may. Errors must not include credentials, such as a URL with
// user information.
type Reporter interface {
	Deliver(ctx context.Context, batch Batch) error
}

type permanentError struct{ err error }

func (e permanentError) Error() string { return e.err.Error() }
func (e permanentError) Unwrap() error { return e.err }

// Permanent marks err as a failure that retrying cannot fix, such as a
// destination rejecting the request as malformed.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return permanentError{err: err}
}

// IsPermanent reports whether err was marked by Permanent.
func IsPermanent(err error) bool {
	var p permanentError
	return errors.As(err, &p)
}

type retryAfterError struct {
	err   error
	after time.Duration
}

func (e retryAfterError) Error() string { return e.err.Error() }
func (e retryAfterError) Unwrap() error { return e.err }

// RetryAfter attaches a destination's requested retry delay to a retryable
// error. The dispatcher honours it up to its backoff cap.
func RetryAfter(err error, after time.Duration) error {
	if err == nil {
		return nil
	}
	return retryAfterError{err: err, after: after}
}

func retryAfterHint(err error) (time.Duration, bool) {
	var r retryAfterError
	if errors.As(err, &r) {
		return r.after, true
	}
	return 0, false
}
