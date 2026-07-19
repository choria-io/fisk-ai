//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package a2anats

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/micro"

	"github.com/choria-io/fisk-ai/a2a"
	"github.com/choria-io/fisk-ai/internal/conns"
	"github.com/choria-io/fisk-ai/internal/util"
)

const (
	// defaultConcurrency bounds how many tool calls run at once on the server.
	// Like the MCP server, an a2a caller has no iteration budget, so without a cap
	// an open subject could spawn unbounded concurrent commands.
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
	// Identity is the served agent's identity; it keys the subjects and the queue
	// group and is the Sender on replies.
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

// Server exposes a set of local tools to remote agents over NATS: it answers
// discovery with an agent card and runs tools on request. It is the producer side
// of the binding.
type Server struct {
	opts      ServerOptions
	identity  string
	byName    map[string]*util.Tool
	card      a2a.AgentCard
	validator *a2a.Validator
	sem       chan struct{}
	svc       micro.Service
	nc        *nats.Conn
	provider  *conns.Provider
}

// NewServer builds a Server over an existing NATS connection and registers its
// discovery and tool endpoints as a micro service. The exposed set is tools minus
// those gated behind operator confirmation (which a served agent cannot satisfy),
// and minus any tool whose name a caller could not use; skipped tools are logged.
// Use ai:deny to keep a command out of the served set entirely.
func NewServer(nc *nats.Conn, tools []*util.Tool, opts ServerOptions) (*Server, error) {
	opts.applyDefaults()

	validator, err := a2a.NewValidator()
	if err != nil {
		return nil, fmt.Errorf("building message validator: %w", err)
	}

	s := &Server{
		opts:      opts,
		identity:  opts.Identity,
		byName:    make(map[string]*util.Tool, len(tools)),
		validator: validator,
		sem:       make(chan struct{}, opts.Concurrency),
		nc:        nc,
	}

	exposed := s.selectExposed(tools)
	s.card = buildCard(opts.Identity, opts.Version, exposed)

	svc, err := micro.AddService(nc, micro.Config{
		Name:        opts.Identity,
		Version:     microVersion(opts.Version),
		Description: "fisk-ai a2a tool server",
		QueueGroup:  opts.Identity,
	})
	if err != nil {
		return nil, fmt.Errorf("registering a2a service: %w", err)
	}

	err = svc.AddEndpoint("discovery", micro.HandlerFunc(s.handleDiscovery), micro.WithEndpointSubject(DiscoverySubject(opts.Identity)))
	if err != nil {
		_ = svc.Stop()
		return nil, fmt.Errorf("registering discovery endpoint: %w", err)
	}

	err = svc.AddEndpoint("tool", micro.HandlerFunc(s.handleTool), micro.WithEndpointSubject(ToolSubject(opts.Identity)))
	if err != nil {
		_ = svc.Stop()
		return nil, fmt.Errorf("registering tool endpoint: %w", err)
	}

	s.svc = svc

	return s, nil
}

// Subjects returns the discovery and tool subjects the server listens on, for
// display.
func (s *Server) Subjects() (discovery string, tool string) {
	return DiscoverySubject(s.identity), ToolSubject(s.identity)
}

// ExposedTools returns the names of the tools the server exposes, in card order.
func (s *Server) ExposedTools() []string {
	names := make([]string, len(s.card.Tools))
	for i, t := range s.card.Tools {
		names[i] = t.Name
	}

	return names
}

// Stop deregisters the service, stops its subscriptions, and releases the
// connection through its Provider, which closes it only when the server
// established it (Serve) and leaves a borrowed one (NewServer) open.
func (s *Server) Stop() error {
	var err error
	if s.svc != nil {
		err = s.svc.Stop()
	}

	s.provider.Close()

	return err
}

// Serve establishes a NATS connection from the named context, registers the a2a
// tool server, and returns it. The returned server owns the connection through
// its Provider and releases it on Stop. The caller blocks until it wants to shut
// down, then calls Stop.
func Serve(contextName string, tools []*util.Tool, opts ServerOptions) (*Server, error) {
	provider, err := conns.Connect(contextName, opts.Identity)
	if err != nil {
		return nil, err
	}

	srv, err := NewServer(provider.Nats(), tools, opts)
	if err != nil {
		provider.Close()
		return nil, err
	}
	srv.provider = provider

	return srv, nil
}

// selectExposed filters tools to those safe to serve and records them by name for
// invocation. Confirm-gated tools have no operator to approve them on a served
// agent and are dropped; a tool with a name a caller could not use is dropped.
// Each drop is logged with its reason.
func (s *Server) selectExposed(tools []*util.Tool) []*util.Tool {
	var exposed []*util.Tool
	for _, t := range tools {
		switch {
		case t.NeedsConfirm(s.opts.ConfirmTags):
			s.opts.Logger.Warn("Skipping tool: confirmation-gated commands are not served over a2a (no operator to approve); use ai:deny to suppress this", "tool", t.Name())
		case !toolNamePattern.MatchString(t.Name()):
			s.opts.Logger.Warn("Skipping tool: not a valid a2a tool name", "tool", t.Name())
		default:
			exposed = append(exposed, t)
			s.byName[t.Name()] = t
		}
	}

	return exposed
}

// handleDiscovery answers a discovery request with the agent card. The subject
// carries only discovery requests, so any other message is rejected.
func (s *Server) handleDiscovery(req micro.Request) {
	msg, err := s.inbound(req, a2a.DiscoveryRequestProtocol)
	if err != nil {
		_ = req.Error("400", err.Error(), nil)
		return
	}
	dr := msg.(*a2a.DiscoveryRequest)

	reply := &a2a.DiscoveryReply{AgentCard: s.card}
	reply.Protocol = a2a.DiscoveryReplyProtocol
	stampReply(&reply.Header, &dr.Header, s.identity)

	s.respond(req, reply)
}

// handleTool runs the requested tool and answers with a tool reply. A failed or
// denied call is reported in-band on the reply (IsError set), never as a NATS
// error. The subject carries only tool requests, so any other message is rejected.
func (s *Server) handleTool(req micro.Request) {
	msg, err := s.inbound(req, a2a.ToolRequestProtocol)
	if err != nil {
		_ = req.Error("400", err.Error(), nil)
		return
	}
	tr := msg.(*a2a.ToolRequest)

	sender := senderName(&tr.Header)

	tool, ok := s.byName[tr.Name]
	if !ok {
		s.opts.Logger.Warn("Rejecting unknown tool call", "tool", tr.Name, "sender", sender)
		s.respond(req, s.toolReply(&tr.Header, &a2a.ToolResult{IsError: true, Output: fmt.Sprintf("tool %q is not available", tr.Name)}))
		return
	}

	// Bound concurrency before spawning so the number of commands running at once
	// stays capped; the dispatch goroutine blocks here when every slot is in use,
	// which back-pressures inbound requests.
	s.sem <- struct{}{}
	go func() {
		defer func() { <-s.sem }()

		ctx, cancel := context.WithTimeout(context.Background(), s.opts.CallTimeout)
		defer cancel()

		log := s.opts.Logger.With("tool", tool.Name(), "command", tool.Command(), "sender", sender)
		log.Info("Running tool call")

		start := time.Now()
		result, err := tool.Execute(ctx, tr.Input)
		duration := time.Since(start)

		switch {
		case err != nil:
			log.Error("Tool call failed", "duration", duration, "error", err)
		case result != nil:
			log.Info("Tool call completed", "duration", duration, "exit_code", result.ExitCode, "truncated", result.Truncated)
		default:
			log.Info("Tool call completed", "duration", duration)
		}

		s.respond(req, s.toolReply(&tr.Header, resultToToolResult(result, err)))
	}()
}

// inbound decodes and validates a request body and confirms it is the protocol
// the receiving subject is contracted to carry.
func (s *Server) inbound(req micro.Request, want string) (any, error) {
	data := req.Data()
	if len(data) > maxMessageSize {
		return nil, fmt.Errorf("request exceeds %d bytes", maxMessageSize)
	}

	err := s.validator.Validate(data)
	if err != nil {
		return nil, fmt.Errorf("invalid request: %w", err)
	}

	return expectProtocol(data, want)
}

// respond marshals a reply and sends it on the request's NATS reply inbox. The
// reply target is always the inbox NATS supplies, never a subject taken from the
// message body.
func (s *Server) respond(req micro.Request, reply any) {
	data, err := json.Marshal(reply)
	if err != nil {
		s.opts.Logger.Warn("Marshaling reply failed", "error", err)
		_ = req.Error("500", "marshaling reply", nil)
		return
	}

	err = req.Respond(data)
	if err != nil {
		s.opts.Logger.Warn("Sending reply failed", "error", err)
	}
}

// toolReply builds a ToolReply for the given request header and result.
func (s *Server) toolReply(reqHdr *a2a.Header, result *a2a.ToolResult) *a2a.ToolReply {
	reply := &a2a.ToolReply{ToolResult: *result}
	reply.Protocol = a2a.ToolReplyProtocol
	stampReply(&reply.Header, reqHdr, s.identity)

	return reply
}

// buildCard assembles an agent card from the exposed tools.
func buildCard(identity, version string, tools []*util.Tool) a2a.AgentCard {
	card := a2a.AgentCard{
		Name:      identity,
		Version:   versionOrDev(version),
		Protocols: []string{a2a.ProtocolNamespace},
	}

	for _, t := range tools {
		card.Tools = append(card.Tools, a2a.ToolDescriptor{
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
func resultToToolResult(result *util.CommandResult, err error) *a2a.ToolResult {
	if err != nil {
		return &a2a.ToolResult{IsError: true, Output: err.Error()}
	}

	return &a2a.ToolResult{
		Output: result.Output,
		Exec: &a2a.ExecResult{
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
func senderName(h *a2a.Header) string {
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

// semVerPattern matches a strict SemVer version, as the micro framework requires
// for a service version.
var semVerPattern = regexp.MustCompile(`^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$`)

// microVersion returns a SemVer string for the micro service registration. The
// agent's own version is free-form (it may be "dev" or a "v"-prefixed tag), so it
// is used when it is already valid SemVer (after stripping a leading "v") and a
// placeholder is used otherwise. This version is service metadata only; the agent
// card carries the human-facing version separately.
func microVersion(version string) string {
	v := strings.TrimPrefix(version, "v")
	if semVerPattern.MatchString(v) {
		return v
	}

	return "0.0.0"
}
