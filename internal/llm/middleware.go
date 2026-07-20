//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package llm

import "net/http"

// MiddlewareNext invokes the next handler in a provider's HTTP request chain and
// returns its response. A Middleware calls it to run the underlying request.
type MiddlewareNext = func(*http.Request) (*http.Response, error)

// Middleware is a cross-cutting request hook wrapping a provider's HTTP call: it
// sees the request, may call MiddlewareNext to run it, and sees the response. The
// caller assembles the hooks a run needs (a request trace, an HTTP debug dump) and
// hands them to the provider through Config, which is the only place the concrete
// wire client is built.
//
// It is deliberately http-shaped rather than tied to any SDK: every provider this
// project targets is an HTTP backend, so the neutral seam can name the type without
// leaking a vendor package. Providers whose SDK expects the same func shape assign
// these values through unchanged.
type Middleware = func(*http.Request, MiddlewareNext) (*http.Response, error)
