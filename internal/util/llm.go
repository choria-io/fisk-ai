//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package util

import (
	"context"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
)

// CallLLM runs a single Messages request under a per-call timeout derived from
// the configured call timeout.
func CallLLM(ctx context.Context, client anthropic.Client, params anthropic.MessageNewParams, timeout time.Duration) (*anthropic.Message, error) {
	callCtx, callCancel := context.WithTimeout(ctx, timeout)
	defer callCancel()

	return client.Messages.New(callCtx, params)
}
