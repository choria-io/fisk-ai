//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package util

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// ValidateBaseURL requires raw to be a well-formed http or https URL and, for a
// non-loopback host, https, so a credential or payload is never sent in cleartext
// across a network. A loopback host (127.0.0.1, ::1, localhost) may use plain http
// so a local server keeps working. Embedded userinfo is rejected. label names the
// setting so callers get an error that points at the knob they set.
//
// This is a scheme check on the configured URL, not a guarantee about the wire: an
// operator who points a base URL at a host they do not control has already chosen
// their trust boundary. It stops the common accident, an http endpoint or a typo,
// not a hostile endpoint.
func ValidateBaseURL(label, raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid %s %q: %w", label, raw, err)
	}
	if u.User != nil {
		return fmt.Errorf("invalid %s %q: must not embed userinfo credentials", label, raw)
	}

	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if !isLoopbackHost(u.Hostname()) {
			return fmt.Errorf("%s %q uses http to a non-loopback host; use https, or a loopback address (127.0.0.1, ::1, localhost) for a local server", label, raw)
		}
		return nil
	default:
		return fmt.Errorf("invalid %s %q: scheme must be http or https", label, raw)
	}
}

// isLoopbackHost reports whether host is a loopback address or the localhost name.
// It does not resolve names, so a hostname that happens to resolve to a loopback
// address is not treated as loopback.
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}

	return false
}
