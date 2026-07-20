//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package anthropic

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/choria-io/fisk-ai/internal/llm"
)

// init registers this provider under its neutral name so a build that imports this
// package can resolve it from llm.NewProvider without naming the package at the
// call site. The factory adapts neutral Config to Options; construction cannot fail
// here (the base URL is validated by the caller before resolution), so it never
// returns an error.
//
// The registered credential env vars are the secret-bearing variables the
// anthropic-sdk-go default credential chain (anthropic.NewClient ->
// DefaultClientOptions) reads to authenticate the agent's own API requests; they
// are stripped from a tool subprocess's environment so a tool, whose command line
// the model chooses, can never read them. Selector variables that merely point at
// on-disk credentials (ANTHROPIC_PROFILE, ANTHROPIC_CONFIG_DIR, XDG_CONFIG_HOME) are
// deliberately not listed: they hold no secret, and the files they locate are
// guarded by filesystem permissions, not by stripping an env var a tool could
// rediscover anyway.
func init() {
	llm.Register(ProviderName, func(cfg llm.Config) (llm.Provider, error) {
		return NewProvider(Options{
			APIKey:      cfg.APIKey,
			BaseURL:     cfg.BaseURL,
			Timeout:     cfg.Timeout,
			Middlewares: cfg.Middlewares,
		}), nil
	}, []string{
		"ANTHROPIC_API_KEY",             // API key
		"ANTHROPIC_AUTH_TOKEN",          // OAuth / bearer token
		"ANTHROPIC_IDENTITY_TOKEN",      // workload-identity-federation token (literal value)
		"ANTHROPIC_WEBHOOK_SIGNING_KEY", // webhook signing secret
		"ANTHROPIC_CUSTOM_HEADERS",      // may carry Authorization / x-api-key headers
	})
}

// Options configure a Provider. APIKey and BaseURL address the backend, Timeout
// bounds a single call, and Middlewares carry the cross-cutting request hooks
// (request trace, HTTP debug dump) the caller assembles. Middlewares is neutral
// (llm.Middleware is http-shaped, not SDK-typed); this package converts it to the
// SDK's request option when it builds the client, keeping the caller SDK-free.
type Options struct {
	APIKey      string
	BaseURL     string
	Timeout     time.Duration
	Middlewares []llm.Middleware
}

// Provider is the Anthropic implementation of llm.Provider. It wraps the SDK
// client and is the only place the SDK is spoken on the call path: it renders a
// neutral Request to MessageNewParams, issues the call under a per-call timeout,
// and converts the reply back to the neutral model.
type Provider struct {
	client  sdk.Client
	timeout time.Duration
}

// NewProvider builds a Provider from Options. The base URL is validated by the
// caller before construction so its error can name the flag the operator set.
func NewProvider(opts Options) *Provider {
	clientOpts := []option.RequestOption{option.WithAPIKey(opts.APIKey)}
	if opts.BaseURL != "" {
		clientOpts = append(clientOpts, option.WithBaseURL(opts.BaseURL))
	}
	for _, m := range opts.Middlewares {
		clientOpts = append(clientOpts, option.WithMiddleware(m))
	}

	return &Provider{
		client:  sdk.NewClient(clientOpts...),
		timeout: opts.Timeout,
	}
}

// Capabilities reports what this provider supports. Anthropic offers server-side
// tool search; the output-token ceiling is left unset because the SDK enforces it
// per model and the request already carries the caller's chosen cap.
func (p *Provider) Capabilities() llm.Caps {
	return llm.Caps{Provider: ProviderName, SupportsToolSearch: true}
}

// Call issues one Anthropic request under the provider's per-call timeout and
// returns the reply in the neutral model. When thinking was requested and the API
// rejects the request with a 400, it adds a hint that the model may not support
// thinking, since that is the common cause and disabling it is not an obvious
// remedy; the caller wraps the result with its own "llm call" context.
func (p *Provider) Call(ctx context.Context, req llm.Request) (*llm.Response, error) {
	params, err := p.buildParams(req)
	if err != nil {
		return nil, err
	}

	callCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	msg, err := p.client.Messages.New(callCtx, params)
	if err != nil {
		var apiErr *sdk.Error
		if req.ThinkingEnabled && errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusBadRequest {
			return nil, fmt.Errorf("%w; model %q may not support thinking, set llm.thinking.enabled to false", err, req.Model)
		}
		return nil, err
	}

	resp, err := ResponseToNeutral(msg)
	if err != nil {
		return nil, err
	}

	return &resp, nil
}

// buildParams renders a neutral Request to Anthropic MessageNewParams. It is
// separated from Call so the request assembly, including the load-bearing
// prompt-cache breakpoint placement, is exercised by tests without a wire call.
func (p *Provider) buildParams(req llm.Request) (sdk.MessageNewParams, error) {
	system := make([]sdk.TextBlockParam, len(req.SystemBlocks))
	for i, s := range req.SystemBlocks {
		system[i] = sdk.TextBlockParam{Text: s}
	}

	// Prompt caching places two cache_control breakpoints. The tools+system
	// breakpoint marks the last system block; the conversation-tail breakpoint is the
	// request-level CacheControl below. The system slice is built fresh here each call
	// from req.SystemBlocks, so marking its last element cannot write through to any
	// value the caller hashes into the run fingerprint: the fingerprint stays
	// marker-free and toggling the cache never refuses a resume.
	if req.PromptCache && len(system) > 0 {
		system[len(system)-1].CacheControl = cacheControl(req.Interactive)
	}

	tools := make([]sdk.ToolUnionParam, 0, len(req.Tools)+1)
	for _, td := range req.Tools {
		tools = append(tools, ToolDefToAnthropic(td))
	}
	if req.ToolSearch {
		tools = append(tools, toolSearchTool())
	}

	messages := make([]sdk.MessageParam, len(req.Messages))
	for i, m := range req.Messages {
		mp, err := MessageToAnthropic(m)
		if err != nil {
			return sdk.MessageNewParams{}, fmt.Errorf("message %d: %w", i, err)
		}
		messages[i] = mp
	}

	params := sdk.MessageNewParams{
		Model:     sdk.Model(req.Model),
		MaxTokens: req.MaxOutputTokens,
		System:    system,
		Tools:     tools,
		Messages:  messages,
	}

	// Thinking is requested only when enabled; the zero union omits it so the model
	// and backend use their default behavior. Summarized display returns readable
	// reasoning even on models that omit it by default.
	if req.ThinkingEnabled {
		params.Thinking = sdk.ThinkingConfigParamUnion{
			OfAdaptive: &sdk.ThinkingConfigAdaptiveParam{
				Display: sdk.ThinkingConfigAdaptiveDisplaySummarized,
			},
		}
	}

	if req.PromptCache {
		params.CacheControl = cacheControl(req.Interactive)
	}

	return params, nil
}

// cacheControl is the cache_control marker for a run's breakpoints. A chat run
// uses a 1h TTL because an operator's think-time between turns commonly exceeds the
// default 5m (a 5m TTL there would pay a cache write with no read, a net cost
// increase); an autonomous loop uses the 5m default, which its tight turn-to-turn
// cadence stays within.
func cacheControl(interactive bool) sdk.CacheControlEphemeralParam {
	if interactive {
		return sdk.CacheControlEphemeralParam{TTL: sdk.CacheControlEphemeralTTLTTL1h}
	}
	return sdk.NewCacheControlEphemeralParam()
}

// toolSearchTool returns the BM25 tool search server tool. It is never deferred,
// so it is always present when requested and lets the model search the deferred
// custom tools by name and description and pull in the ones it needs.
func toolSearchTool() sdk.ToolUnionParam {
	return sdk.ToolUnionParam{OfToolSearchToolBm25_20251119: &sdk.ToolSearchToolBm25_20251119Param{
		Type: sdk.ToolSearchToolBm25_20251119TypeToolSearchToolBm25,
	}}
}
