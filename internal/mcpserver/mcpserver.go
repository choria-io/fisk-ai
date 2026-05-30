//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

// Package mcpserver exposes a fisk application's tools over the Model Context
// Protocol. The same util.Tool values the agent calls are registered as MCP
// tools, so an external MCP client can invoke the underlying commands directly,
// without an LLM in the loop.
package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"regexp"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/choria-io/fisk-ai/internal/util"
)

const (
	// defaultConcurrency bounds how many tool calls run at once. Unlike the agent,
	// an MCP client has no iteration budget, so without a cap it could spawn
	// unbounded concurrent commands. It mirrors the tool budget default.
	defaultConcurrency = 2
	// defaultCallTimeout bounds a single tool call. It mirrors the tool budget
	// default and protects against a command that never returns.
	defaultCallTimeout = 30 * time.Second
	// shutdownTimeout bounds how long graceful HTTP shutdown waits for in-flight
	// requests before returning.
	shutdownTimeout = 5 * time.Second
	// elicitTimeout bounds how long a confirm-tagged call waits for the client's
	// user to answer the approval prompt before it is denied, so an unanswered
	// prompt cannot park a request indefinitely. It is separate from and longer
	// than defaultCallTimeout, which bounds only the command run, because a human
	// deciding may reasonably take longer than a command takes to execute.
	elicitTimeout = 2 * time.Minute
)

// toolNamePattern is the character set MCP clients accept for tool names. fisk
// tool names are underscore-joined command paths, which are normally within this
// set, but a command segment with other characters would produce a name some
// clients reject, so such tools are skipped rather than silently broken.
var toolNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// ConfirmMode selects how confirm-tagged commands (ai:confirm and Options.ConfirmTags)
// are gated over MCP.
type ConfirmMode string

const (
	// ConfirmAuto asks a client that supports elicitation to approve each
	// confirm-tagged command and runs it ungated for a client that cannot elicit.
	// It is the zero value and the default.
	ConfirmAuto ConfirmMode = "auto"
	// ConfirmAlways requires approval: a client that cannot elicit is refused rather
	// than allowed to run a confirm-tagged command ungated.
	ConfirmAlways ConfirmMode = "always"
	// ConfirmNever never elicits; confirm-tagged commands run ungated regardless of
	// client support, for when approval is delegated to the client's own UI.
	ConfirmNever ConfirmMode = "never"
)

// Options configures the MCP server.
type Options struct {
	// Name is the server name reported to clients; it shows up in the client's
	// server list, so it should identify the application (e.g. its identity).
	Name string
	// Version is the server version reported to clients.
	Version string
	// Addr is the TCP address the HTTP server listens on, e.g. ":8080".
	Addr string
	// Instructions is optional free text sent to clients at connection time, via
	// the MCP initialize response. Clients may add it to the LLM's system prompt
	// as a hint about how to use the server's tools. Empty means none is sent.
	Instructions string
	// Concurrency is the maximum number of tool calls run at once; <= 0 uses the
	// default.
	Concurrency int
	// CallTimeout bounds a single tool call; <= 0 uses the default.
	CallTimeout time.Duration
	// ConfirmTags lists the tags that, alongside the always-on ai:confirm, mark a
	// command as requiring the caller's approval before it runs. It mirrors the
	// agent's confirm_tags: an existing CLI whose commands are not ai:* aware can
	// have its own tags (e.g. impact:rw) mapped to the same confirm behavior. Empty
	// means only ai:confirm gates.
	ConfirmTags []string
	// ConfirmMode selects how confirm-tagged commands are gated: elicit-then-ungated
	// (ConfirmAuto, the default), elicit-then-refuse (ConfirmAlways), or never elicit
	// and always run (ConfirmNever). The empty value is treated as ConfirmAuto.
	ConfirmMode ConfirmMode
	// LogOutput receives human-readable progress; nil means os.Stderr. It must
	// never be the JSON-RPC stream, and may be written concurrently from multiple
	// client sessions, so it must be safe for concurrent use (os.Stderr is).
	LogOutput io.Writer
}

func (o *Options) applyDefaults() {
	if o.Concurrency <= 0 {
		o.Concurrency = defaultConcurrency
	}
	if o.CallTimeout <= 0 {
		o.CallTimeout = defaultCallTimeout
	}
	if o.LogOutput == nil {
		o.LogOutput = os.Stderr
	}
	if o.ConfirmMode == "" {
		o.ConfirmMode = ConfirmAuto
	}
}

// BuildServer creates an MCP server exposing tools. Each tool with a client-safe
// name is registered with its raw fisk input schema and a handler that runs the
// command; tools whose names are not valid MCP tool names are skipped with a
// warning. It returns the server and the names of the tools actually registered.
//
// Confirmation tags (ai:confirm and the equivalent Options.ConfirmTags) are honored
// here: before a tagged command runs, a client that negotiated elicitation is asked
// to approve it and the command runs only on approval, while a refusal, a dismissal,
// or any elicitation error denies the call. A client that cannot elicit has no one
// to ask, so such commands run ungated as they always have, logged per call. This is
// a request for approval, not an enforcement boundary: a client is free to
// auto-approve or to not support elicitation at all, so use ai:deny to keep a
// command off MCP entirely.
//
// A single semaphore is shared across all handlers, so Concurrency bounds the
// total number of in-flight tool calls, not the count per tool.
func BuildServer(tools []*util.Tool, opts Options) (*mcp.Server, []string) {
	opts.applyDefaults()

	srv := mcp.NewServer(&mcp.Implementation{Name: opts.Name, Version: opts.Version}, &mcp.ServerOptions{
		Instructions:       opts.Instructions,
		InitializedHandler: initializedLogger(opts.ConfirmMode, opts.LogOutput),
	})
	sem := make(chan struct{}, opts.Concurrency)
	policy := confirmPolicy{tags: opts.ConfirmTags, mode: opts.ConfirmMode, serverName: opts.Name}

	var registered []string
	var confirmCount int
	for _, t := range tools {
		if !toolNamePattern.MatchString(t.Name()) {
			fmt.Fprintf(opts.LogOutput, "warning: skipping tool %q: not a valid MCP tool name\n", t.Name())
			continue
		}

		srv.AddTool(&mcp.Tool{
			Name:        t.Name(),
			Description: t.ModelDescription(),
			InputSchema: inputSchema(t),
			Annotations: toolAnnotations(t),
		}, toolHandler(t, policy, sem, opts.CallTimeout, opts.LogOutput))
		registered = append(registered, t.Name())

		if t.NeedsConfirm(opts.ConfirmTags) {
			confirmCount++
		}
	}

	if confirmCount > 0 {
		fmt.Fprintf(opts.LogOutput, "note: %d exposed tool(s) require approval (ai:confirm or confirm_tags): %s\n", confirmCount, confirmModeSummary(opts.ConfirmMode))
	}

	return srv, registered
}

// confirmModeSummary describes, for the startup note, how confirm-tagged tools are
// gated under mode so an operator sees the posture without having to know what the
// configured policy implies for clients that cannot elicit.
func confirmModeSummary(mode ConfirmMode) string {
	switch mode {
	case ConfirmAlways:
		return "clients that support elicitation are asked to approve each run, clients that do not are refused (confirm_over_mcp=always)"
	case ConfirmNever:
		return "they run ungated with approval delegated to the client (confirm_over_mcp=never)"
	default:
		return "clients that support elicitation are asked to approve each run, clients that do not run them ungated (confirm_over_mcp=auto)"
	}
}

// toolAnnotations maps a tool's metadata to MCP tool annotations: a readable
// title, the space-separated command path (e.g. "stream rm" rather than the
// underscore tool name). Annotations are advisory hints for clients, not a control
// channel, so approval (ai:confirm) is deliberately not expressed here; see
// BuildServer. The behavioral hints (ReadOnlyHint, DestructiveHint) are left unset:
// fisk-ai has no standard tag describing a command's effect, so asserting one would
// be a guess, and leaving them unset carries the spec's conservative default.
func toolAnnotations(t *util.Tool) *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{Title: t.Command()}
}

// Serve builds the MCP server and serves it over HTTP until ctx is canceled.
// It returns an error if there are no registrable tools or the listener fails;
// a clean shutdown via ctx returns nil.
func Serve(ctx context.Context, tools []*util.Tool, opts Options) error {
	opts.applyDefaults()

	srv, registered := BuildServer(tools, opts)
	if len(registered) == 0 {
		return fmt.Errorf("no tools available to expose over MCP")
	}

	ln, err := net.Listen("tcp", opts.Addr)
	if err != nil {
		return err
	}

	return serveListener(ctx, ln, srv, registered, opts)
}

// serveListener serves the MCP server over ln until ctx is canceled. It is split
// out from Serve so a test can drive the shutdown path over a listener it owns
// (and read back its address); Serve and tests exercise the same serving code.
func serveListener(ctx context.Context, ln net.Listener, srv *mcp.Server, registered []string, opts Options) error {
	// connCtx is the base context for every incoming request. The MCP streamable
	// transport keeps a standalone SSE GET request hanging until its request
	// context is canceled, and http.Server.Shutdown neither cancels request
	// contexts nor closes such connections; it only waits for them to go idle,
	// which a held-open stream never does. Canceling connCtx on shutdown unblocks
	// those streams so Shutdown completes promptly instead of waiting out
	// shutdownTimeout, which is what made a single interrupt appear to hang.
	connCtx, cancelConns := context.WithCancel(context.Background())
	defer cancelConns()

	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	httpServer := &http.Server{
		Handler:     handler,
		BaseContext: func(net.Listener) context.Context { return connCtx },
	}

	fmt.Fprintf(opts.LogOutput, "Serving %d tools over MCP on %s as %s/%s\n", len(registered), ln.Addr(), opts.Name, opts.Version)
	for _, name := range registered {
		fmt.Fprintf(opts.LogOutput, "  %s\n", name)
	}

	if hint := claudeAddHint(opts.Name, ln.Addr()); hint != "" {
		fmt.Fprintf(opts.LogOutput, "\nAdd to Claude Code with:\n  %s\n", hint)
	}

	errCh := make(chan error, 1)
	go func() {
		err := httpServer.Serve(ln)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		errCh <- err
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		// Unblock any hanging SSE streams first so graceful shutdown can drain the
		// remaining requests promptly rather than waiting out shutdownTimeout.
		cancelConns()

		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()

		err := httpServer.Shutdown(shutdownCtx)
		fmt.Fprintf(opts.LogOutput, "MCP server stopped\n")

		return err
	}
}

// claudeAddHint returns the `claude mcp add` command an operator can run to
// register this server with Claude Code, or "" if the address has no parsable
// port. The server speaks the streamable HTTP transport at the listener root, so
// the hint uses --transport http with a URL built from the listener's port. The
// listener binds an unspecified host (":port"), whose resolved address (e.g.
// "[::]:port") is not a URL an operator can paste; localhost is what they
// actually connect to on the same host, so the host is rewritten to it.
func claudeAddHint(name string, addr net.Addr) string {
	host, port, err := net.SplitHostPort(addr.String())
	if err != nil {
		return ""
	}
	if host == "" || host == "::" || host == "0.0.0.0" {
		host = "localhost"
	}

	if name == "" {
		name = "fisk-ai"
	}

	return fmt.Sprintf("claude mcp add --transport http %s http://%s:%s", name, host, port)
}

// inputSchema renders a tool's fisk input schema as raw JSON for MCP. The schema
// is passed through verbatim: it is already a restricted JSON-schema object, and
// MCP clients consume the schema directly, so no agent-specific rewriting (such
// as the optional-parameter annotation used for the Anthropic API) is applied. A
// missing schema falls back to an empty object schema.
func inputSchema(t *util.Tool) json.RawMessage {
	schema := t.InputSchema()
	if schema == nil {
		return json.RawMessage(`{"type":"object"}`)
	}

	data, err := json.Marshal(schema)
	if err != nil {
		return json.RawMessage(`{"type":"object"}`)
	}

	return data
}

// confirmPolicy carries the confirm-gating configuration a handler needs: the tags
// that require approval, how to treat a client that cannot elicit, and the server
// name shown in the prompt. It is built once in BuildServer and shared by every
// handler.
type confirmPolicy struct {
	tags       []string
	mode       ConfirmMode
	serverName string
}

// toolHandler runs a tool for a tools/call request, bounding it with the shared
// concurrency semaphore and a per-call timeout. Each call logs a single line to
// logOut naming the command line that is about to run, so an operator can see
// what an MCP client is invoking.
//
// A confirm-tagged command (ai:confirm or a policy.tags match) is gated before it
// runs, per policy.mode: a client that negotiated elicitation is asked to approve it
// and the command runs only on approval, while a client that cannot elicit either
// runs it ungated (ConfirmAuto) or is refused (ConfirmAlways); ConfirmNever skips the
// gate entirely. The gate runs before the semaphore and outside the per-call timeout,
// so waiting on a human neither holds a concurrency slot nor is cut short by the run
// timeout.
//
// The result mapping matches the agent's: an execution failure (a missing
// binary, a canceled context, or arguments that cannot be turned into a command
// line) becomes a tool result with IsError set, so the caller learns the call
// failed; a command that runs but exits non-zero is a successful result whose
// JSON body carries the exit code and output. Failures are never returned as a
// Go error, which the SDK would treat as a protocol-level error the client
// cannot reason about.
func toolHandler(t *util.Tool, policy confirmPolicy, sem chan struct{}, timeout time.Duration, logOut io.Writer) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		// Resolve the command line up front: it names the command in the approval
		// prompt and the run log, and an argument-resolution failure here is the same
		// one Execute would return, so reporting it now, before any prompt or
		// concurrency slot, keeps the error identical and avoids asking a user to
		// approve a command that cannot run.
		cmdLine, err := t.CommandLine(req.Params.Arguments)
		if err != nil {
			return errorResult(err.Error()), nil
		}

		if policy.mode != ConfirmNever && t.NeedsConfirm(policy.tags) {
			if denied := confirmRun(ctx, req, t, policy, cmdLine, logOut); denied != nil {
				return denied, nil
			}
		}

		select {
		case sem <- struct{}{}:
			defer func() { <-sem }()
		case <-ctx.Done():
			return errorResult(ctx.Err().Error()), nil
		}

		callCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()

		fmt.Fprintf(logOut, "Running %s\n", cmdLine)

		result, err := t.Execute(callCtx, req.Params.Arguments)
		if err != nil {
			return errorResult(err.Error()), nil
		}

		data, err := json.Marshal(result)
		if err != nil {
			return errorResult(fmt.Sprintf("marshaling tool result: %v", err)), nil
		}

		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: string(data)}},
		}, nil
	}
}

// errorResult builds a tool result carrying an error message with IsError set.
func errorResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: text}},
	}
}

// approvalSchema is the elicitation form backing a confirm prompt: a single
// required boolean the caller sets true to approve the run. It is built once and
// shared; the server never trusts the "required" constraint to be honored and
// re-checks the returned answer (see confirmRun).
var approvalSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"approve": map[string]any{
			"type":        "boolean",
			"title":       "Approve this command",
			"description": "Set to true to run the command now, or false to refuse it.",
		},
	},
	"required": []string{"approve"},
}

// confirmRun gates a confirm-tagged command before it runs. When the client
// negotiated elicitation its user is asked to approve the command, and confirmRun
// returns a denial result unless the user explicitly approves, so a refusal, a
// dismissal, a non-boolean answer, or any elicitation error (a disconnect, or a
// client that reneges on the capability it advertised) fails closed. When the
// client cannot elicit there is no one to ask: under ConfirmAlways the call is
// refused, and otherwise it runs and a warning is logged, making the ungated
// execution visible per call rather than only at connect time. A nil return means
// the command may run. It is not called under ConfirmNever, where the caller skips
// gating entirely.
func confirmRun(ctx context.Context, req *mcp.CallToolRequest, t *util.Tool, policy confirmPolicy, cmdLine string, logOut io.Writer) *mcp.CallToolResult {
	trigger := t.ConfirmTrigger(policy.tags)

	if !sessionElicits(req.Session) {
		if policy.mode == ConfirmAlways {
			return errorResult(fmt.Sprintf("Not approved: running %q requires approval, but this client cannot be asked (it does not support elicitation). The command did not run.", cmdLine))
		}

		fmt.Fprintf(logOut, "warning: running %q ungated: gated by %s but the client cannot elicit approval\n", cmdLine, trigger)
		return nil
	}

	elicitCtx, cancel := context.WithTimeout(ctx, elicitTimeout)
	defer cancel()

	res, err := req.Session.Elicit(elicitCtx, &mcp.ElicitParams{
		Message:         confirmMessage(policy.serverName, cmdLine, trigger),
		RequestedSchema: approvalSchema,
	})
	if err != nil {
		return errorResult(fmt.Sprintf("Not approved: requesting approval to run %q failed: %v. The command did not run.", cmdLine, err))
	}

	switch res.Action {
	case "accept":
		approve, ok := res.Content["approve"].(bool)
		if ok && approve {
			return nil
		}
		return errorResult(fmt.Sprintf("Not approved: the user reviewed %q and chose not to run it. This was their decision, not an error; do not retry.", cmdLine))
	case "decline":
		return errorResult(fmt.Sprintf("Not approved: the user declined to run %q. This was their decision, not an error; do not retry, ask how they want to proceed.", cmdLine))
	default:
		return errorResult(fmt.Sprintf("Approval not completed: the user dismissed the approval prompt for %q without deciding. The command did not run; you may ask again if it is still needed.", cmdLine))
	}
}

// confirmMessage is the approval prompt shown to the calling client's user: the
// server asking, the exact command that will run, and the tag that gated it, so the
// user sees what will run and why they are being asked.
func confirmMessage(serverName, cmdLine, trigger string) string {
	return fmt.Sprintf("%s wants to run a command that requires your approval.\n\nCommand: %s\nGated by: %s\n\nApprove to run it now, or refuse to skip it.", serverName, cmdLine, trigger)
}

// sessionElicits reports whether the client on ss negotiated the elicitation
// capability. It reads the session at call time, so each client is judged on its
// own capability and a call arriving before initialization is treated as incapable
// rather than assumed either way.
func sessionElicits(ss *mcp.ServerSession) bool {
	if ss == nil {
		return false
	}

	params := ss.InitializeParams()
	if params == nil || params.Capabilities == nil {
		return false
	}

	return params.Capabilities.Elicitation != nil
}

// clientName is the connecting client's reported name, or "unknown" when it sent
// none, for identifying a client in the connection log.
func clientName(ss *mcp.ServerSession) string {
	if ss == nil {
		return "unknown"
	}

	params := ss.InitializeParams()
	if params == nil || params.ClientInfo == nil || params.ClientInfo.Name == "" {
		return "unknown"
	}

	return params.ClientInfo.Name
}

// initializedLogger logs one line per client connection recording how that client's
// confirm-tagged commands will be gated under mode. It is logging only: the gate
// decision is read from the session per call, never from here, so a race between
// this notification and a tool call cannot weaken gating.
func initializedLogger(mode ConfirmMode, logOut io.Writer) func(context.Context, *mcp.InitializedRequest) {
	return func(_ context.Context, req *mcp.InitializedRequest) {
		name := clientName(req.Session)

		switch {
		case mode == ConfirmNever:
			fmt.Fprintf(logOut, "Client %q connected: confirm-tagged tools run ungated (confirm_over_mcp=never)\n", name)
		case sessionElicits(req.Session):
			fmt.Fprintf(logOut, "Client %q connected: elicitation supported, confirm-tagged tools will request approval\n", name)
		case mode == ConfirmAlways:
			fmt.Fprintf(logOut, "Client %q connected: elicitation unsupported, confirm-tagged tools will be refused (confirm_over_mcp=always)\n", name)
		default:
			fmt.Fprintf(logOut, "Client %q connected: elicitation unsupported, confirm-tagged tools run ungated\n", name)
		}
	}
}
