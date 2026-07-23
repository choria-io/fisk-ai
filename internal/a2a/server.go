//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package a2a

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"regexp"
	"time"

	"github.com/choria-io/fisk-ai/internal/toolkit"
	"github.com/choria-io/fisk-ai/internal/toolkit/fisk"
)

const (
	// defaultConcurrency bounds how many tool calls run at once on the server.
	// Like the MCP server, an a2a caller has no iteration budget, so without a cap
	// an open path could spawn unbounded concurrent commands.
	defaultConcurrency = 2
	// defaultCallTimeout bounds a single served tool call against a command that
	// never returns.
	defaultCallTimeout = 30 * time.Second
)

// toolNamePattern is the character set a tool name must match to be exposed. It
// mirrors the MCP server's rule: a name outside this set cannot be imported by a
// caller as a usable tool, so such tools are skipped rather than silently broken.
var toolNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// ServerOptions configures the a2a tool server.
type ServerOptions struct {
	// Identity is the served agent's identity; it is the Sender on replies and is
	// used by the transport to key the serving paths.
	Identity string
	// Version is reported in the agent card.
	Version string
	// ConfirmTags are the operator-configured tags that, with the always-on
	// ai:confirm, gate a command behind approval. A served agent has no operator,
	// so commands carrying any of these are never exposed (hard-deny).
	ConfirmTags []string
	// Concurrency is the maximum number of tool calls run at once; <= 0 uses the
	// default.
	Concurrency int
	// CallTimeout bounds a single tool call; <= 0 uses the default.
	CallTimeout time.Duration
	// LogOutput is the sink for the default Logger; nil means os.Stderr. It is
	// ignored when Logger is supplied.
	LogOutput io.Writer
	// Logger receives structured progress; nil builds a text logger over
	// LogOutput.
	Logger *slog.Logger
}

func (o *ServerOptions) applyDefaults() {
	if o.Concurrency <= 0 {
		o.Concurrency = defaultConcurrency
	}
	if o.CallTimeout <= 0 {
		o.CallTimeout = defaultCallTimeout
	}
	if o.LogOutput == nil {
		o.LogOutput = os.Stderr
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.NewTextHandler(o.LogOutput, nil))
	}
}

// Server exposes a set of local tools to remote agents over a Transport: it
// answers discovery with an agent card and runs tools on request. It is the
// producer side of the protocol. It owns all message validation and the
// concurrency back-pressure; the Transport only carries bytes and keeps the
// discovery and tool paths separate.
type Server struct {
	opts      ServerOptions
	identity  string
	byName    map[string]*fisk.FiskCommandTool
	card      AgentCard
	validator *Validator
	sem       chan struct{}
	transport Transport
}

// NewServer builds a Server over transport and registers its discovery and tool
// handlers. The exposed set is tools minus those gated behind operator
// confirmation (which a served agent cannot satisfy), and minus any tool whose
// name a caller could not use; skipped tools are logged. Use ai:deny to keep a
// command out of the served set entirely. The Transport is borrowed: Stop releases
// only the Transport, and the caller closes the Provider behind it.
func NewServer(transport Transport, tools []*fisk.FiskCommandTool, opts ServerOptions) (*Server, error) {
	opts.applyDefaults()

	validator, err := NewValidator()
	if err != nil {
		return nil, fmt.Errorf("building message validator: %w", err)
	}

	s := &Server{
		opts:      opts,
		identity:  opts.Identity,
		byName:    make(map[string]*fisk.FiskCommandTool, len(tools)),
		validator: validator,
		sem:       make(chan struct{}, opts.Concurrency),
		transport: transport,
	}

	exposed := s.selectExposed(tools)
	s.card = buildCard(opts.Identity, opts.Version, exposed)

	err = transport.Serve(OpDiscovery, s.handleDiscovery)
	if err != nil {
		return nil, fmt.Errorf("registering discovery handler: %w", err)
	}

	err = transport.Serve(OpTool, s.handleTool)
	if err != nil {
		return nil, fmt.Errorf("registering tool handler: %w", err)
	}

	return s, nil
}

// ExposedTools returns the names of the tools the server exposes, in card order.
func (s *Server) ExposedTools() []string {
	names := make([]string, len(s.card.Tools))
	for i, t := range s.card.Tools {
		names[i] = t.Name
	}

	return names
}

// Describe returns the transport-neutral lines describing how this server is
// reached, for display.
func (s *Server) Describe() []DescLine {
	return s.transport.Describe(s.identity)
}

// Stop releases the transport's resources. It does not close the shared
// connection Provider, which the caller owns and closes.
func (s *Server) Stop() error {
	return s.transport.Close()
}

// selectExposed filters tools to those safe to serve and records them by name for
// invocation. Confirm-gated tools have no operator to approve them on a served
// agent and are dropped; a tool with a name a caller could not use is dropped; a
// tool that advertises no description is dropped, since a remote agent importing it
// would reject it as giving the model nothing to decide on. Each drop is logged with
// its reason.
func (s *Server) selectExposed(tools []*fisk.FiskCommandTool) []*fisk.FiskCommandTool {
	var exposed []*fisk.FiskCommandTool
	for _, t := range tools {
		switch {
		case t.NeedsConfirm(s.opts.ConfirmTags):
			s.opts.Logger.Warn("Skipping tool: confirmation-gated commands are not served over a2a (no operator to approve); use ai:deny to suppress this", "tool", t.Name())
		case !toolNamePattern.MatchString(t.Name()):
			s.opts.Logger.Warn("Skipping tool: not a valid a2a tool name", "tool", t.Name())
		case t.ModelDescription() == "":
			s.opts.Logger.Warn("Skipping tool: served tools must advertise a description; remote agents will not import a description-less tool", "tool", t.Name())
		default:
			exposed = append(exposed, t)
			s.byName[t.Name()] = t
		}
	}

	return exposed
}

// handleDiscovery answers a discovery request with the agent card. The discovery
// path carries only discovery requests, so any other message is rejected.
func (s *Server) handleDiscovery(_ context.Context, body []byte, reply Replier) {
	msg, err := s.inbound(body, DiscoveryRequestProtocol)
	if err != nil {
		_ = reply.Error("400", err.Error())
		return
	}
	dr := msg.(*DiscoveryRequest)

	out := &DiscoveryReply{AgentCard: s.card}
	out.Protocol = DiscoveryReplyProtocol
	stampReply(&out.Header, &dr.Header, s.identity)

	s.respond(reply, out)
}

// handleTool runs the requested tool and answers with a tool reply. A failed or
// denied call is reported in-band on the reply (IsError set), never as a transport
// error. The tool path carries only tool requests, so any other message is
// rejected. The concurrency semaphore is acquired synchronously here, on the
// transport's serving goroutine, before a worker is spawned, so intake is back-
// pressured: a full server does not enter another request until a slot frees.
func (s *Server) handleTool(ctx context.Context, body []byte, reply Replier) {
	msg, err := s.inbound(body, ToolRequestProtocol)
	if err != nil {
		_ = reply.Error("400", err.Error())
		return
	}
	tr := msg.(*ToolRequest)

	sender := senderName(&tr.Header)

	tool, ok := s.byName[tr.Name]
	if !ok {
		s.opts.Logger.Warn("Rejecting unknown tool call", "tool", tr.Name, "sender", sender)
		s.respond(reply, s.toolReply(&tr.Header, &ToolResult{IsError: true, Output: fmt.Sprintf("tool %q is not available", tr.Name)}))
		return
	}

	// Bound concurrency before spawning so the number of commands running at once
	// stays capped; this blocks the serving goroutine when every slot is in use,
	// which back-pressures inbound requests at intake, not just at execution.
	s.sem <- struct{}{}
	go func() {
		defer func() { <-s.sem }()

		runCtx, cancel := context.WithTimeout(ctx, s.opts.CallTimeout)
		defer cancel()

		log := s.opts.Logger.With("tool", tool.Name(), "command", tool.Command(), "sender", sender)
		log.Info("Running tool call")

		start := time.Now()
		// The served tool runs in the process working directory; a per-call scratch
		// directory for served tools is future server work, not this run path.
		result, err := tool.Execute(runCtx, tr.Input, "")
		duration := time.Since(start)

		switch {
		case err != nil:
			log.Error("Tool call failed", "duration", duration, "error", err)
		case result != nil:
			log.Info("Tool call completed", "duration", duration, "exit_code", result.ExitCode, "truncated", result.Truncated)
		default:
			log.Info("Tool call completed", "duration", duration)
		}

		s.respond(reply, s.toolReply(&tr.Header, resultToToolResult(result, err)))
	}()
}

// inbound size-caps, validates and decodes a request body and confirms it is the
// protocol the receiving path is contracted to carry. The size cap runs first,
// before any decode or allocation.
func (s *Server) inbound(body []byte, want string) (any, error) {
	if len(body) > maxMessageSize {
		return nil, fmt.Errorf("request exceeds %d bytes", maxMessageSize)
	}

	err := s.validator.Validate(body)
	if err != nil {
		return nil, fmt.Errorf("invalid request: %w", err)
	}

	return expectProtocol(body, want)
}

// respond marshals a reply and sends it through the request's Replier. The reply
// target is always the inbox the transport supplied, never a subject taken from
// the message body.
func (s *Server) respond(reply Replier, msg any) {
	data, err := json.Marshal(msg)
	if err != nil {
		s.opts.Logger.Warn("Marshaling reply failed", "error", err)
		_ = reply.Error("500", "marshaling reply")
		return
	}

	err = reply.Respond(data)
	if err != nil {
		s.opts.Logger.Warn("Sending reply failed", "error", err)
	}
}

// toolReply builds a ToolReply for the given request header and result.
func (s *Server) toolReply(reqHdr *Header, result *ToolResult) *ToolReply {
	reply := &ToolReply{ToolResult: *result}
	reply.Protocol = ToolReplyProtocol
	stampReply(&reply.Header, reqHdr, s.identity)

	return reply
}

// buildCard assembles an agent card from the exposed tools.
func buildCard(identity, version string, tools []*fisk.FiskCommandTool) AgentCard {
	card := AgentCard{
		Name:      identity,
		Version:   versionOrDev(version),
		Protocols: []string{ProtocolNamespace},
	}

	for _, t := range tools {
		card.Tools = append(card.Tools, ToolDescriptor{
			Name:        t.Name(),
			Description: t.ModelDescription(),
			InputSchema: marshalSchema(t.InputSchema()),
		})
	}

	return card
}

// resultToToolResult maps a command execution outcome to the shared ToolResult.
// A harness failure (the command could not run) sets IsError; a command that ran,
// including one that exited non-zero, is a successful result carrying the output
// and the exec metadata, so the caller can reconstruct the same CommandResult a
// local tool would have produced.
func resultToToolResult(result *toolkit.CommandResult, err error) *ToolResult {
	if err != nil {
		return &ToolResult{IsError: true, Output: err.Error()}
	}

	return &ToolResult{
		Output: result.Output,
		Exec: &ExecResult{
			Command:   result.Command,
			ExitCode:  result.ExitCode,
			Truncated: result.Truncated,
		},
	}
}

// marshalSchema renders a tool's input schema as raw JSON for a tool descriptor,
// falling back to an empty object schema when it is absent or cannot be marshaled.
func marshalSchema(schema map[string]any) json.RawMessage {
	if schema == nil {
		return json.RawMessage(`{"type":"object"}`)
	}

	data, err := json.Marshal(schema)
	if err != nil {
		return json.RawMessage(`{"type":"object"}`)
	}

	return data
}

// senderName returns the sender identity of a header for logging, or "unknown".
func senderName(h *Header) string {
	if h.Sender.Name == "" {
		return "unknown"
	}

	return h.Sender.Name
}

// versionOrDev returns the version, or "dev" when it is empty, so the agent card
// always carries a non-empty version. The card version is a free-form string.
func versionOrDev(version string) string {
	if version == "" {
		return "dev"
	}

	return version
}
