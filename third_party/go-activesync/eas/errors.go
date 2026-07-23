// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package eas

import "fmt"

// HTTPError is returned when an EAS request receives a non-2xx HTTP
// response from the server. The body is captured (truncated to 4 KiB) so
// callers can include it in diagnostics without re-issuing the request.
type HTTPError struct {
	StatusCode int
	Status     string
	URL        string
	Body       []byte // truncated to 4 KiB
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("eas: %s %s: HTTP %s (body=%dB)", "POST", e.URL, e.Status, len(e.Body))
}

// IsHTTPStatus reports whether err is an HTTPError with the given code.
// Useful for callers that want to distinguish 401 / 403 / 449 etc. without
// type-asserting at every site.
func IsHTTPStatus(err error, code int) bool {
	for ; err != nil; err = unwrap(err) {
		if h, ok := err.(*HTTPError); ok {
			return h.StatusCode == code
		}
	}
	return false
}

// unwrap is a tiny shim around the stdlib errors.Unwrap so we can avoid
// importing errors here just for one function.
func unwrap(err error) error {
	type wrapped interface{ Unwrap() error }
	if w, ok := err.(wrapped); ok {
		return w.Unwrap()
	}
	return nil
}

// StatusError is returned when an EAS command parses successfully but the
// embedded Status element reports a non-success code. The mapping of codes
// to human-readable names lives in status.go.
type StatusError struct {
	Command string // EAS command name ("FolderSync", "Provision", ...)
	Code    int
}

func (e *StatusError) Error() string {
	if name := statusName(e.Code); name != "" {
		return fmt.Sprintf("eas: %s: status %d (%s)", e.Command, e.Code, name)
	}
	return fmt.Sprintf("eas: %s: status %d", e.Command, e.Code)
}

// IsStatusCode reports whether err is a StatusError with the given EAS
// status code.
func IsStatusCode(err error, code int) bool {
	for ; err != nil; err = unwrap(err) {
		if s, ok := err.(*StatusError); ok {
			return s.Code == code
		}
	}
	return false
}
