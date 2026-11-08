package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"syscall"
	"time"
)

type transientDownloadError struct {
	err   error
	after time.Duration
}

func (e *transientDownloadError) Error() string { return e.err.Error() }
func (e *transientDownloadError) Unwrap() error { return e.err }

// Only exhausted network retries may be isolated from the rest of a queue.
// Preserve the cause for callers while keeping storage/integrity failures fatal.
type exhaustedNetworkRetriesError struct{ err error }

func (e *exhaustedNetworkRetriesError) Error() string { return e.err.Error() }
func (e *exhaustedNetworkRetriesError) Unwrap() error { return e.err }
func retryableHTTPStatus(status int) bool {
	return status == 408 || status == 429 || status >= 500 && status <= 599
}
func responseDownloadError(response *http.Response) error {
	err := &httpDownloadError{status: response.StatusCode}
	if !retryableHTTPStatus(response.StatusCode) {
		return err
	}
	delay := time.Duration(0)
	if seconds, e := strconv.Atoi(response.Header.Get("Retry-After")); e == nil && seconds > 0 {
		delay = time.Duration(min(seconds, 30)) * time.Second
	} else if date, e := http.ParseTime(response.Header.Get("Retry-After")); e == nil {
		delay = min(30*time.Second, max(time.Duration(0), time.Until(date)))
	}
	return &transientDownloadError{err: err, after: delay}
}
func classifyNetworkError(err error) error {
	if err == nil {
		return nil
	}
	var pathError *os.PathError
	var linkError *os.LinkError
	if errors.As(err, &pathError) || errors.As(err, &linkError) {
		return err
	}
	var transient *transientDownloadError
	if errors.As(err, &transient) {
		return err
	}
	var network net.Error
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ETIMEDOUT) || errors.As(err, &network) && (network.Timeout() || network.Temporary()) {
		return &transientDownloadError{err: err}
	}
	return err
}

// Three retries reconnect using the verified partial and the same publisher details.
// Integrity, filesystem, identity, and incompatible Range errors are never retried.
func (a *app) withNetworkRetries(ctx context.Context, name string, run func() error) error {
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := run()
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var status *httpDownloadError
		if errors.As(err, &status) {
			a.mu.Lock()
			a.lastHTTPStatus = status.status
			a.mu.Unlock()
		}
		var retry *transientDownloadError
		if !errors.As(classifyNetworkError(err), &retry) {
			return err
		}
		if attempt >= 3 {
			return &exhaustedNetworkRetriesError{err: err}
		}
		a.mu.Lock()
		a.retryCount++
		a.mu.Unlock()
		delay := max(time.Second*time.Duration(1<<attempt), retry.after)
		a.setJob(job{State: "retrying", Name: name, Error: fmt.Sprintf("Connection interrupted. Retry %d of 3 in %.0f seconds…", attempt+1, delay.Seconds())})
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
func (a *app) downloadWithRetries(ctx context.Context, item downloadRequest) error {
	return a.withNetworkRetries(ctx, item.Name, func() error { return a.download(ctx, item) })
}
