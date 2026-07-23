//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package functool

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/choria-io/fisk-ai/internal/llm"
	"github.com/choria-io/fisk-ai/internal/toolkit"
	"github.com/choria-io/fisk-ai/internal/util"
)

// maxTraceRunes caps a tool's one-line call trace or confirm summary before it is
// shown to the operator, so a handler-supplied renderer cannot flood the terminal.
const maxTraceRunes = 500

// Handler runs a function tool in-process for a model tool_use and returns the JSON
// result string the model sees. A returned error is an invocation failure surfaced
// to the model as an error result; an outcome the model should reason about (a
// declined action, a not-found) is carried in the returned string as a normal
// result, so the model sees every tool kind the same way. tc gives the handler the
// per-run operator Prompter and working directory; a handler that needs neither
// ignores it.
type Handler func(ctx context.Context, input json.RawMessage, tc *CallContext) (string, error)

// ConfirmSpec gates a function tool behind operator confirmation. Its presence on a
// Spec makes the tool require approval before every call: the always-on
// toolkit.ConfirmTag is structural, so a confirm-gated tool can never be silently
// ungated.
type ConfirmSpec struct {
	// Tags are extra tags, on top of the always-on toolkit.ConfirmTag, that also gate
	// the tool when the operator lists them under confirm_tags.
	Tags []string
	// Summary renders the one-line description of the pending call shown to the
	// operator in the approval prompt, from the call's input. Its output is sanitized
	// for terminal display. When nil the tool name is shown alone.
	Summary func(input json.RawMessage) string
}

// RemoteSpec marks a function tool as served by another agent rather than run in
// this process. Its presence is the explicit signal the runner accounts as a remote
// call; the agent name it carries may be empty and is never itself the signal. A
// remote tool's handler makes a network call to the serving agent and touches no
// local environment or subprocess; its schema is untrusted, advertised and gated by
// that agent rather than by this process.
type RemoteSpec struct {
	// Agent names the remote agent serving the tool, shown in the call trace. It may
	// be empty when the serving agent is not named.
	Agent string
}

// Spec describes a function tool: its model-facing identity and handler, and the
// optional hooks that give it operator confirmation, argument validation, a call
// trace, deferral control, or remote presentation. New validates it into a *Tool.
type Spec struct {
	// Name is the model-facing tool name, unique within a run; it is the key the
	// runner dispatches on. Required.
	Name string
	// Description is the model-facing description advertised to the model. Required.
	Description string
	// Schema is the JSON-schema input advertised to the model. Required; it is
	// advertised verbatim, so it must be a stable, deterministic object across process
	// restarts (see the Tool doc).
	Schema map[string]any
	// Handler runs the tool in-process and returns its JSON result. Required.
	Handler Handler

	// Confirm, when set, gates the tool behind operator confirmation.
	Confirm *ConfirmSpec
	// ValidateRequired, when set, rejects a call missing a required schema parameter
	// before it runs, returning the missing names to the model. The schema must
	// declare a non-empty "required" list, or New reports an error rather than
	// validating nothing.
	ValidateRequired bool
	// Trace, when set, renders a one-line call trace from the input so the tool's call
	// and result are shown like a command's. Leave it nil for a tool that renders its
	// own operator interaction, whose trace would only duplicate it. Its output is
	// sanitized for terminal display.
	Trace func(input json.RawMessage) string
	// NoDefer forces the tool to always be sent to the model directly, never hidden
	// behind tool search, even within an otherwise deferred tool set.
	NoDefer bool
	// Remote, when set, marks the tool as served by another agent.
	Remote *RemoteSpec
}

// Tool is a function tool built from a Spec: a name, description, schema and Go
// handler exposed to the model as a toolkit.Tool, with optional confirmation,
// argument validation, tracing and remote presentation. A local function tool (one
// with no RemoteSpec) runs in-process with the agent's own privileges and unscrubbed
// ambient environment (the subprocess credential scrub that protects command tools
// does not apply here), so its handler is trusted code, not sandboxed: it must never
// read a secret from the environment or hand the ambient environment to a subprocess.
// A remote tool's handler instead calls out to its serving agent (see RemoteSpec) and
// runs no local code.
//
// A tool's Definition JSON (name, description, schema, deferral) must be
// deterministic across process restarts. The agent fingerprints the tool set to
// decide whether a checkpointed run may resume; a Definition that varies run to run
// flips that fingerprint and makes a resume refuse.
type Tool struct {
	name        string
	description string
	schema      map[string]any
	handler     Handler

	confirm          *ConfirmSpec
	validateRequired bool
	trace            func(input json.RawMessage) string
	noDefer          bool
	remote           *RemoteSpec
}

// A function tool is a model-facing Tool that describes its own presentation, and
// opts into confirmation and argument validation through the narrow capability
// interfaces the runner consults; those capabilities are inert (NeedsConfirm and
// MissingRequired report nothing) unless the Spec enables them.
var (
	_ toolkit.Tool              = (*Tool)(nil)
	_ toolkit.Describer         = (*Tool)(nil)
	_ toolkit.Confirmable       = (*Tool)(nil)
	_ toolkit.ArgumentValidator = (*Tool)(nil)
)

// New validates a Spec and returns the function tool it describes. It fails when a
// required field is missing, when ValidateRequired is set but the schema declares no
// required parameters (which would silently validate nothing), or when a remote tool
// is also marked confirm-gated (a remote tool is gated by its serving agent, never
// locally).
func New(spec Spec) (*Tool, error) {
	switch {
	case spec.Name == "":
		return nil, fmt.Errorf("tool name is required")
	case spec.Description == "":
		return nil, fmt.Errorf("tool %q description is required", spec.Name)
	case spec.Schema == nil:
		return nil, fmt.Errorf("tool %q schema is required", spec.Name)
	case spec.Handler == nil:
		return nil, fmt.Errorf("tool %q handler is required", spec.Name)
	}

	if spec.ValidateRequired && len(toolkit.SchemaRequired(spec.Schema["required"])) == 0 {
		return nil, fmt.Errorf("tool %q sets ValidateRequired but its schema declares no required parameters", spec.Name)
	}

	if spec.Remote != nil && spec.Confirm != nil {
		return nil, fmt.Errorf("tool %q is remote and cannot be confirm-gated; a remote tool is gated by its serving agent", spec.Name)
	}

	return &Tool{
		name:             spec.Name,
		description:      spec.Description,
		schema:           spec.Schema,
		handler:          spec.Handler,
		confirm:          spec.Confirm,
		validateRequired: spec.ValidateRequired,
		trace:            spec.Trace,
		noDefer:          spec.NoDefer,
		remote:           spec.Remote,
	}, nil
}

// Name is the model-facing tool name.
func (t *Tool) Name() string { return t.name }

// Description is the model-facing description.
func (t *Tool) Description() string { return t.description }

// InputSchema returns the tool's JSON-schema input, for a caller that consumes the
// raw schema directly (the MCP server).
func (t *Tool) InputSchema() map[string]any { return t.schema }

// Definition renders the tool as a neutral definition. A tool marked NoDefer is
// always sent directly, even within a deferred set, so its deferral is suppressed
// here rather than in the caller.
func (t *Tool) Definition(deferLoading bool) llm.ToolDef {
	return llm.ToolDef{
		Name:         t.name,
		Description:  t.description,
		InputSchema:  t.schema,
		DeferLoading: deferLoading && !t.noDefer,
	}
}

// ExecuteUse runs the handler for a model tool_use block and returns the matching
// tool result: a handler error becomes an error result, its output a normal result,
// so the model sees every tool kind the same way. It builds the handler's
// CallContext from the per-run ExecDeps.
func (t *Tool) ExecuteUse(ctx context.Context, use llm.ToolUseBlock, deps toolkit.ExecDeps) llm.ToolResultBlock {
	out, err := t.handler(ctx, use.Input, &CallContext{workDir: deps.WorkDir, prompter: deps.Prompter})
	if err != nil {
		return llm.ToolResultBlock{ToolUseID: use.ID, Content: err.Error(), IsError: true}
	}

	return llm.ToolResultBlock{ToolUseID: use.ID, Content: out}
}

// Call runs the tool in-process and returns its JSON result string. It is the direct
// handler seam for a non-agent caller (the MCP server); the agent path uses
// ExecuteUse. prompter is the operator path a handler uses for a question; a caller
// with no operator passes toolkit.DefaultDenyPrompter so any unexpected prompt fails
// closed. It supplies no working directory. A returned error is an invocation
// failure; an outcome the caller should reason about is carried in the string.
func (t *Tool) Call(ctx context.Context, input json.RawMessage, prompter toolkit.Prompter) (string, error) {
	return t.handler(ctx, input, &CallContext{prompter: prompter})
}

// TraceLine renders the one-line display of a call: the confirm summary for a
// confirm-gated tool (shown in the approval prompt) or the call trace for a traced
// tool (shown like a command's line, and the seam the MCP server traces through),
// sanitized for terminal display since its text is handler-supplied. It is "" for a
// tool that is neither confirm-gated with a summary nor traced.
func (t *Tool) TraceLine(input json.RawMessage) string {
	switch {
	case t.confirm != nil && t.confirm.Summary != nil:
		return util.SanitizeForTerminal(t.confirm.Summary(input), maxTraceRunes)
	case t.trace != nil:
		return util.SanitizeForTerminal(t.trace(input), maxTraceRunes)
	default:
		return ""
	}
}

// Describe reports how a call is presented and what per-run dependencies it needs. A
// remote tool is presented as remote and named by its agent; an in-process tool with
// a Trace renderer is traced like a command; an in-process tool without one is
// treated as rendering its own operator interaction. Every in-process tool is
// offered the operator Prompter and the per-run working directory, which a handler
// that needs neither ignores.
func (t *Tool) Describe(input json.RawMessage) toolkit.CallInfo {
	if t.remote != nil {
		return toolkit.CallInfo{Present: toolkit.PresentRemote, Agent: t.remote.Agent}
	}

	info := toolkit.CallInfo{
		Present:       toolkit.PresentSelfRendered,
		NeedsPrompter: true,
		NeedsWorkDir:  true,
	}
	if t.trace != nil {
		info.Present = toolkit.PresentTraced
		info.Display = util.SanitizeForTerminal(t.trace(input), maxTraceRunes)
	}

	return info
}

// NeedsConfirm reports whether a call must be approved before it runs. A tool with no
// ConfirmSpec is never gated; a confirm-gated one is always gated by the always-on
// toolkit.ConfirmTag, plus any of the operator's extra confirm tags that match the
// spec's Tags.
func (t *Tool) NeedsConfirm(extraTags []string) bool {
	if t.confirm == nil {
		return false
	}

	return toolkit.NeedsConfirm(t.confirmTags(), extraTags)
}

// ConfirmTrigger names the tag that gated the call, for the approval prompt. It is
// "" for a tool that is not confirm-gated, which NeedsConfirm reports first.
func (t *Tool) ConfirmTrigger(extraTags []string) string {
	if t.confirm == nil {
		return ""
	}

	return toolkit.ConfirmTrigger(t.confirmTags(), extraTags)
}

// Command is the bare command the call runs, shown in the approval prompt and used
// as the subject of a session-wide allow. A function tool has no command line of its
// own, so it is its model-facing name.
func (t *Tool) Command() string { return t.name }

// confirmTags is the tool's effective confirm tag set: the always-on
// toolkit.ConfirmTag unioned with the spec's extra tags, so a confirm-gated tool is
// structurally always gated by ConfirmTag.
func (t *Tool) confirmTags() []string {
	return append([]string{toolkit.ConfirmTag}, t.confirm.Tags...)
}

// MissingRequired returns the required schema parameters absent from input, or nil
// when the tool does not validate or the call is complete. A parameter counts as
// supplied when its key is present regardless of value; an empty or null input is an
// empty object, so every required parameter is reported missing; a non-object input
// is left for the handler to reject, so nothing is reported missing.
func (t *Tool) MissingRequired(input json.RawMessage) []string {
	if !t.validateRequired {
		return nil
	}

	required := toolkit.SchemaRequired(t.schema["required"])
	if len(required) == 0 {
		return nil
	}

	supplied, ok := decodeInputObject(input)
	if !ok {
		return nil
	}

	var missing []string
	for _, name := range required {
		if _, present := supplied[name]; !present {
			missing = append(missing, name)
		}
	}

	return missing
}

// MissingRequiredMessage is the result returned to the model naming the missing
// parameters and the tool's full parameter roster, split into required and optional,
// so the model can reconcile its call against what the tool accepts and correct it in
// one turn.
func (t *Tool) MissingRequiredMessage(missing []string) string {
	required, optional := t.parameters()

	msg := fmt.Sprintf("tool %q was called without required parameter(s): %s. required: %s",
		t.name, strings.Join(missing, ", "), strings.Join(required, ", "))
	if len(optional) > 0 {
		msg += "; optional: " + strings.Join(optional, ", ")
	}

	return msg
}

// parameters returns the tool's parameter names split into required and optional.
// Required keeps the schema-declared order; optional is sorted, since the schema
// properties are an unordered map, so the roster is deterministic.
func (t *Tool) parameters() (required, optional []string) {
	required = toolkit.SchemaRequired(t.schema["required"])

	props, ok := t.schema["properties"].(map[string]any)
	if !ok {
		return required, nil
	}

	names := make([]string, 0, len(props))
	for name := range props {
		names = append(names, name)
	}
	slices.Sort(names)

	for _, name := range names {
		if !slices.Contains(required, name) {
			optional = append(optional, name)
		}
	}

	return required, optional
}

// decodeInputObject decodes a model tool input into its object form. An empty or null
// input is an empty object. The second return is false when the input is present but
// not a JSON object (an array or a scalar), which the caller passes through for the
// handler to reject rather than treating every property as absent.
func decodeInputObject(input json.RawMessage) (map[string]any, bool) {
	if len(input) == 0 || string(input) == "null" {
		return map[string]any{}, true
	}

	var obj map[string]any
	if err := json.Unmarshal(input, &obj); err != nil {
		return nil, false
	}

	return obj, true
}
