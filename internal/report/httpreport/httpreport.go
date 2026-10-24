// Package httpreport delivers report batches as JSON over HTTP POST.
//
// Each batch is one request whose body is report.Batch.JSON and whose
// Idempotency-Key header is the batch ID. A 2xx response is success.
// 408, 425, 429 and 5xx responses and transport errors are retryable,
// honouring Retry-After. Every other status, redirects included, is a
// permanent failure: redirects are not followed, so credentials attached to
// the request never reach another host.
//
// TLS certificates are always verified against the system roots; there is no
// option to skip verification. The destination URL never appears in errors,
// because its user information, path or query may carry credentials;
// transport errors may name the host and port.
package httpreport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/iomz/tagstrak/v2/internal/report"
)

// ErrURL reports an unusable destination URL. Its message deliberately omits
// the URL.
var ErrURL = errors.New("httpreport: invalid destination URL")

// maxResponseDrain bounds how much of a response body is read so the
// connection can be reused.
const maxResponseDrain = 64 << 10

// Reporter posts batches to one URL.
type Reporter struct {
	url    string
	client *http.Client
	header func(http.Header)
}

// Option configures a Reporter.
type Option func(*Reporter)

// WithClient replaces the HTTP client, for tests or custom transports. Its
// redirect policy is replaced so redirects are never followed.
func WithClient(client *http.Client) Option {
	return func(r *Reporter) { r.client = client }
}

// WithHeader sets a function that adds headers, such as authorization, to
// every request. Secrets belong in its closure, never in the URL.
func WithHeader(header func(http.Header)) Option {
	return func(r *Reporter) { r.header = header }
}

// New validates the destination URL. It accepts absolute http and https URLs
// with a host and no fragment.
func New(rawURL string, options ...Option) (*Reporter, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("%w: unparseable", ErrURL)
	}
	switch {
	case u.Scheme != "http" && u.Scheme != "https":
		return nil, fmt.Errorf("%w: scheme must be http or https", ErrURL)
	case u.Host == "":
		return nil, fmt.Errorf("%w: host is required", ErrURL)
	case u.Fragment != "":
		return nil, fmt.Errorf("%w: fragments are not allowed", ErrURL)
	}
	r := &Reporter{url: u.String()}
	for _, option := range options {
		option(r)
	}
	client := &http.Client{Transport: http.DefaultTransport.(*http.Transport).Clone()}
	if r.client != nil {
		copied := *r.client
		client = &copied
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	// The dispatcher bounds each attempt through ctx.
	client.Timeout = 0
	r.client = client
	return r, nil
}

// Deliver posts one batch.
func (r *Reporter) Deliver(ctx context.Context, batch report.Batch) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.url, bytes.NewReader(batch.JSON()))
	if err != nil {
		return report.Permanent(fmt.Errorf("httpreport: build request: %w", stripURL(err)))
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", batch.ID)
	if r.header != nil {
		r.header(req.Header)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("httpreport: %w", stripURL(err))
	}
	defer resp.Body.Close()
	_, _ = io.CopyN(io.Discard, resp.Body, maxResponseDrain)

	status := resp.StatusCode
	switch {
	case status >= 200 && status < 300:
		return nil
	case status == http.StatusRequestTimeout, status == http.StatusTooEarly,
		status == http.StatusTooManyRequests, status >= 500:
		failure := fmt.Errorf("httpreport: destination returned %d", status)
		if after, ok := retryAfter(resp.Header.Get("Retry-After"), time.Now()); ok {
			return report.RetryAfter(failure, after)
		}
		return failure
	default:
		return report.Permanent(fmt.Errorf("httpreport: destination returned %d", status))
	}
}

// stripURL removes the URL from net/http errors, which embed it verbatim.
func stripURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("%s: %w", ue.Op, ue.Err)
	}
	return err
}

// retryAfter parses a Retry-After value in either delay-seconds or HTTP-date
// form.
func retryAfter(value string, now time.Time) (time.Duration, bool) {
	if value == "" {
		return 0, false
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds < 0 {
			return 0, false
		}
		return time.Duration(seconds) * time.Second, true
	}
	if at, err := http.ParseTime(value); err == nil {
		return max(at.Sub(now), 0), true
	}
	return 0, false
}
