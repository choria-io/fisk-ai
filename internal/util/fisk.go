//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package util

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/choria-io/fisk"
	"golang.org/x/term"

	"github.com/choria-io/fisk-ai/config"
)

// denyTag is always-on: commands carrying it are never exposed as tools.
const denyTag = "ai:deny"

// noDeferTag forces a tool to always be loaded directly (defer_loading:false),
// even when the tool set is large enough that the rest are deferred behind the
// tool search tool. Use it for the few commands the model needs on most requests
// so they stay immediately available.
const noDeferTag = "ai:no_defer"

// confirmTag marks a command as requiring the operator's explicit permission
// before it runs. When the agent is about to run a tool carrying it, it prompts
// the operator at the terminal, showing the command and its arguments, and only
// runs it on an affirmative answer; an "allow for the session" answer is
// remembered in-process for that tool for the rest of the run (see ConfirmGate).
// It is always on; operators can gate further commands by listing additional tags
// under confirm_tags, which NeedsConfirm treats the same way.
//
// Confirmation does not need a terminal, so confirm-tagged commands are exposed
// over MCP: there a client that supports elicitation is asked to approve the run,
// and a client that does not runs the command ungated (see the mcpserver package).
// Use ai:deny to keep a command off MCP entirely.
const confirmTag = "ai:confirm"

// maxToolOutputBytes caps the captured output so a chatty command cannot flood
// the model's context. Output beyond this is truncated with a marker.
const maxToolOutputBytes = 64 * 1024

// commandWaitDelay bounds how long a command's I/O may linger after its context
// is canceled before the process and its pipes are forcibly torn down.
const commandWaitDelay = 10 * time.Second

// CommandResult is the structured result of running a command tool. It is
// returned to the model as JSON; the exit code plus separated output streams let
// the model distinguish success from failure and diagnostics from output.
type CommandResult struct {
	// Command is the command and arguments that were run, without the binary path.
	Command string `json:"command"`
	// ExitCode is the command's exit status; 0 on success.
	ExitCode int `json:"exit_code"`
	// Output is the command's output: stdout and stderr combined in the order they
	// were written, as they would appear in a terminal, so their interleaving (and
	// thus when in the run any diagnostics appeared) is preserved.
	Output string `json:"output"`
	// Truncated is true when Output was capped.
	Truncated bool `json:"truncated,omitempty"`
}

// Tool is our intermediate representation of a fisk command exposed as a Tool.
// It is built from an application model, filtered with FilterTools, and finally
// turned into an Anthropic tool definition with AnthropicTool. The captured
// command Model is the source of truth for the Tool's schema and tags (so
// tag-based filtering is possible, which a bare tool definition cannot express)
// and, later, for mapping a Tool call's arguments back to the command's
// arguments and flags.
type Tool struct {
	// Path is the command path components, e.g. {"auth", "user", "add"}.
	Path []string
	// Model is the leaf command this tool represents.
	Model *fisk.CmdModel
	// AppPath is the filesystem path of the application binary this command
	// belongs to and which is executed when the tool is called. It is set by
	// ToolsForApp; it is empty for tools built directly with ApplicationTools,
	// whose handlers therefore cannot run until it is populated.
	AppPath string
}

// Name is the LLM tool name: the command path joined with "_", e.g. the command
// "auth user add" becomes "auth_user_add".
func (t *Tool) Name() string {
	return strings.Join(t.Path, "_")
}

// Command is the full command path as a human would type it, space separated.
func (t *Tool) Command() string {
	return strings.Join(t.Path, " ")
}

// Description is the command help, falling back to the long help. It is the
// plain, tag-free help used for human-facing listings; ModelDescription is what
// a model sees.
func (t *Tool) Description() string {
	if t.Model.Help != "" {
		return t.Model.Help
	}
	return t.Model.HelpLong
}

// ModelDescription is the description presented to a model: the command help
// followed by the command's tags, if any. Unlike Description, which serves
// human-facing listings and shows a single help string, this surfaces both the
// short help and the long help so the model gets the richer guidance an author
// wrote, which matters most when the tools are exposed over MCP. The tags are
// surfaced so an operator can write prompt instructions that key off them, for
// example "always ask for approval before running commands tagged impact:rw",
// and so the model can search for tools by tag when the tool set is deferred.
// Reserved ai: tags are included as-is; an ai:deny command is never exposed to a
// model in the first place, so it cannot appear here.
func (t *Tool) ModelDescription() string {
	desc := t.modelHelp()

	tags := t.Tags()
	if len(tags) == 0 {
		return desc
	}

	tagLine := "Tags: " + strings.Join(tags, ", ")
	if desc == "" {
		return tagLine
	}

	return desc + "\n\n" + tagLine
}

// modelHelp combines the command's short and long help into the help text a
// model sees. When both are set they are concatenated, short help first; if one
// already contains the other only the longer is used, so a long help that
// restates the summary does not repeat it.
func (t *Tool) modelHelp() string {
	short := t.Model.Help
	long := t.Model.HelpLong

	switch {
	case short == "":
		return long
	case long == "":
		return short
	case strings.Contains(long, short):
		return long
	case strings.Contains(short, long):
		return short
	default:
		return short + "\n\n" + long
	}
}

// Tags are the fisk command tags.
func (t *Tool) Tags() []string {
	return t.Model.Tags
}

// NeedsConfirm reports whether the command must be approved by the operator
// before it runs: it carries the always-on ai:confirm tag, or any of the extra
// confirm tags the operator configured (extraTags, from confirm_tags). The
// always-on ai:confirm tag is checked unconditionally, so omitting it from
// extraTags can never weaken the guarantee. In the agent loop the approval is
// enforced against a local operator; over MCP it is requested from the calling
// client through elicitation when the client supports it, and the command runs
// ungated otherwise (see the mcpserver package).
func (t *Tool) NeedsConfirm(extraTags []string) bool {
	for _, tag := range t.Tags() {
		if tag == confirmTag || slices.Contains(extraTags, tag) {
			return true
		}
	}

	return false
}

// ConfirmTrigger returns the tag that gates this command, named to the operator
// in the approval prompt. The always-on ai:confirm tag takes precedence when
// present, since it is the strongest, mode-independent signal; otherwise the
// first of the command's tags that appears in extraTags, in the command's own tag
// order, is returned so the message is deterministic. It returns an empty string
// when the command is not gated, which NeedsConfirm reports first, so callers
// consult it only for a gated command.
func (t *Tool) ConfirmTrigger(extraTags []string) string {
	if slices.Contains(t.Tags(), confirmTag) {
		return confirmTag
	}

	for _, tag := range t.Tags() {
		if slices.Contains(extraTags, tag) {
			return tag
		}
	}

	return ""
}

// InputSchema is the Anthropic-restricted JSON schema for the command, as
// precomputed by fisk during introspection. ApplicationTools guarantees it is
// present, so it is non-nil for any tool obtained that way.
func (t *Tool) InputSchema() map[string]any {
	return t.Model.RestrictedSchema
}

// MissingRequired returns the names of the command's required parameters that the
// model's input does not supply, in schema-declared order. A parameter counts as
// supplied when its key is present in the input object regardless of its value:
// fisk renders an explicit null or empty string as a value (see argValuesFromValue
// and scalarString in ArgsFromJSON), so only an absent key is genuinely missing,
// and a required boolean false, number zero or empty array all count as supplied.
// An empty or null input is treated as an empty object, mirroring argv, so every
// required parameter is reported missing. A non-object input (a JSON array or
// scalar) is left for Execute to reject with fisk's own error, so nothing is
// reported missing here.
//
// fisk forbids a required flag or argument that also carries a default, so a
// property in the required list genuinely must be supplied and this cannot reject
// a call the command would have run from a default.
func (t *Tool) MissingRequired(input json.RawMessage) []string {
	required := schemaRequired(t.InputSchema()["required"])
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

// MissingRequiredMessage is the tool_result text returned to the model when a call
// omitted required parameters. It names the missing parameters and lists the
// command's full parameter roster, split into required and optional, so the model
// can reconcile what it sent against what the command accepts and correct the call
// in one turn rather than re-guessing a parameter name it got wrong.
func (t *Tool) MissingRequiredMessage(missing []string) string {
	required, optional := t.parameters()

	msg := fmt.Sprintf("tool %q was called without required parameter(s): %s. required: %s",
		t.Name(), strings.Join(missing, ", "), strings.Join(required, ", "))
	if len(optional) > 0 {
		msg += "; optional: " + strings.Join(optional, ", ")
	}

	return msg
}

// parameters returns the command's parameter names split into required and
// optional. Required keeps the schema-declared order; optional is sorted, since
// the schema properties are an unordered map, so the roster is deterministic.
func (t *Tool) parameters() (required, optional []string) {
	schema := t.InputSchema()
	required = schemaRequired(schema["required"])

	props, ok := schema["properties"].(map[string]any)
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

// decodeInputObject decodes a model tool input into its object form. An empty or
// null input is an empty object, matching how argv normalizes it before building
// the command line. The second return is false when the input is present but not a
// JSON object (an array or a scalar), which the caller passes through to the
// command to reject rather than treating every property as absent.
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

// argv resolves the model's JSON arguments into the argument vector for the
// command: the command path followed by its arguments and flags. args is the raw
// tool_use input; an empty or null input is treated as an empty argument object.
//
// The arguments only ever reach the binary as argv, never a shell, so model
// input cannot be interpreted as a shell command; t.Model.ArgsFromJSON is the
// trust boundary that bounds it to the command's schema.
func (t *Tool) argv(args json.RawMessage) ([]string, error) {
	// The model may emit a null or empty input for a command that takes no
	// arguments; ArgsFromJSON expects an object, so normalize to an empty one.
	if len(args) == 0 || string(args) == "null" {
		args = json.RawMessage("{}")
	}

	tail, err := t.Model.ArgsFromJSON(args)
	if err != nil {
		return nil, fmt.Errorf("building command line for %q: %w", t.Command(), err)
	}

	return append(append([]string{}, t.Path...), tail...), nil
}

// CommandLine resolves the model's JSON arguments into the full command line a
// user would type: the command path plus its arguments and flags, without the
// binary path. It is what Execute runs and what tracing displays.
func (t *Tool) CommandLine(args json.RawMessage) (string, error) {
	argv, err := t.argv(args)
	if err != nil {
		return "", err
	}

	return strings.Join(argv, " "), nil
}

// TraceLine renders the trace line for a resolved tool call: the full command
// line a user would type, sanitized for terminal display. It falls back to the
// command path when the model's arguments cannot be resolved. This is the single
// source of truth for how a tool call is shown, whether during a live run or when
// a stored transcript is replayed.
func (t *Tool) TraceLine(args json.RawMessage) string {
	cmdline, err := t.CommandLine(args)
	if err != nil {
		cmdline = t.Command()
	}

	return SanitizeCommandLine(cmdline)
}

// traceArg bounds how much of a single resolved argument value the short trace
// line keeps before eliding its middle: the head and tail are kept and the middle
// is replaced by an ellipsis, so similar values (which often share a prefix, like
// two subjects or stream names) stay distinguishable while the line stays short.
const (
	traceArgHeadRunes = 10
	traceArgTailRunes = 6
	traceArgEllipsis  = "..."
)

// TraceLineShort renders the trace line like TraceLine but elides the middle of
// any long argument value. It is display-only, for the scrolling transcript where
// a long argument would otherwise swamp the line; the confirm gate and CommandLine
// keep the full value, so an approval decision or an executed command is never
// shortened. The command path and flag names are never elided, only argument
// values. It falls back to the command path when the arguments cannot be resolved.
func (t *Tool) TraceLineShort(args json.RawMessage) string {
	argv, err := t.argv(args)
	if err != nil {
		return SanitizeCommandLine(t.Command())
	}

	// The command path is the first len(t.Path) tokens; everything after a lone "--"
	// separator is a positional value. Between them are flags, which fisk renders as
	// a single "--name=value" token (or a bare "--name" for a boolean). The slice is
	// a fresh copy from argv, so eliding tokens here cannot reach Execute, which
	// resolves its own argv.
	positional := false
	for i := len(t.Path); i < len(argv); i++ {
		tok := argv[i]
		switch {
		case !positional && tok == "--":
			positional = true
		case !positional && strings.HasPrefix(tok, "-"):
			if name, value, ok := strings.Cut(tok, "="); ok {
				argv[i] = name + "=" + ElideMiddle(value)
			}
		default:
			argv[i] = ElideMiddle(tok)
		}
	}

	return SanitizeCommandLine(strings.Join(argv, " "))
}

// ElideMiddle shortens s to its head and tail joined by an ellipsis when it is
// longer than the head, tail and ellipsis together, counting runes so a multibyte
// character is never split. A short value is returned unchanged. It is the shared
// truncation used for displaying a tool call's argument values, whether resolved
// from a command line (TraceLineShort) or shown as raw JSON in a read-only view.
func ElideMiddle(s string) string {
	r := []rune(s)
	if len(r) <= traceArgHeadRunes+len(traceArgEllipsis)+traceArgTailRunes {
		return s
	}

	return string(r[:traceArgHeadRunes]) + traceArgEllipsis + string(r[len(r)-traceArgTailRunes:])
}

// Execute runs the command for a tool call. The model's JSON arguments are
// turned into the command line by the fisk model, the application binary is
// executed, and its output is returned as a CommandResult.
//
// A non-zero exit is reported in the result rather than as an error, so the model
// can read the failure output and react; only an inability to run the binary, or
// a canceled or timed-out context, is returned as an error.
func (t *Tool) Execute(ctx context.Context, args json.RawMessage) (*CommandResult, error) {
	if t.AppPath == "" {
		return nil, fmt.Errorf("command %q has no application path to execute", t.Command())
	}

	argv, err := t.argv(args)
	if err != nil {
		return nil, err
	}

	cmd := exec.CommandContext(ctx, t.AppPath, argv...)
	cmd.Stdin = nil
	cmd.WaitDelay = commandWaitDelay
	cmd.Env = commandEnv()

	// The command is non-interactive: stdin is closed and stdout and stderr are
	// captured together in the order they were produced.
	out, runErr := cmd.CombinedOutput()

	// A canceled or timed-out context is an execution failure, not a command
	// outcome the model should reason about, so surface it as an error.
	if ctx.Err() != nil {
		return nil, fmt.Errorf("running command %q: %w", t.Command(), ctx.Err())
	}

	result := &CommandResult{Command: strings.Join(argv, " ")}
	result.Output, result.Truncated = capOutput(string(out))

	var exitErr *exec.ExitError
	switch {
	case runErr == nil:
		result.ExitCode = 0
	case errors.As(runErr, &exitErr):
		result.ExitCode = exitErr.ExitCode()
	default:
		// The binary could not be started (missing, not executable, ...).
		return nil, fmt.Errorf("running command %q: %w", t.Command(), runErr)
	}

	return result, nil
}

// stdinIsTerminal reports whether the agent's own stdin is an interactive
// terminal, the condition the confirm gate and the ask_human_* builtins need to
// reach a human. It is a variable so a test can exercise those paths without a
// real terminal.
var stdinIsTerminal = func() bool { return term.IsTerminal(int(os.Stdin.Fd())) }

// StdoutIsTerminal reports whether stdout is an interactive terminal. The
// full-screen UI takes over the screen only when both this and StdinIsTerminal
// hold, so a piped or redirected stdout falls back to the line UI and stays clean.
func StdoutIsTerminal() bool { return term.IsTerminal(int(os.Stdout.Fd())) }

// sensitiveEnvVars are removed from a tool command's environment so a tool, whose
// command line is chosen by the model, can never read the agent's own credentials.
// These are the secret-bearing variables the anthropic-sdk-go default credential
// chain (anthropic.NewClient -> DefaultClientOptions) reads to authenticate the
// agent's API requests. Selector variables that merely point at on-disk
// credentials (ANTHROPIC_PROFILE, ANTHROPIC_CONFIG_DIR, XDG_CONFIG_HOME, ...) are
// deliberately not listed: they hold no secret, and the files they locate are
// guarded by filesystem permissions, not by stripping an env var a tool could
// rediscover anyway.
var sensitiveEnvVars = map[string]bool{
	"ANTHROPIC_API_KEY":             true, // API key
	"ANTHROPIC_AUTH_TOKEN":          true, // OAuth / bearer token
	"ANTHROPIC_IDENTITY_TOKEN":      true, // workload-identity-federation token (literal value)
	"ANTHROPIC_WEBHOOK_SIGNING_KEY": true, // webhook signing secret
	"ANTHROPIC_CUSTOM_HEADERS":      true, // may carry Authorization / x-api-key headers
}

// commandEnv builds the environment for a tool command: the current environment
// with sensitive variables removed and LLMFORMAT set. LLMFORMAT signals fisk
// applications that their output is read by an LLM, so they can render a form
// suited to that rather than a terminal.
func commandEnv() []string {
	current := os.Environ()
	out := make([]string, 0, len(current)+1)

	for _, kv := range current {
		name, _, found := strings.Cut(kv, "=")
		if found && sensitiveEnvVars[name] {
			continue
		}
		out = append(out, kv)
	}

	return append(out, "LLMFORMAT=1")
}

// capOutput truncates s to maxToolOutputBytes, keeping the head and tail with a
// marker between them, and reports whether it truncated.
func capOutput(s string) (string, bool) {
	if len(s) <= maxToolOutputBytes {
		return s, false
	}

	const marker = "\n...[output truncated]...\n"
	half := maxToolOutputBytes / 2
	return s[:half] + marker + s[len(s)-half:], true
}

// ApplicationTools turns every runnable command of a fisk application model into
// a Tool. The command tree is walked recursively and each leaf command (one with
// no subcommands) becomes a Tool named by its full command path; non-leaf
// (grouping) commands and hidden commands, with their subtrees, are skipped.
//
// The model must come from a fisk introspection recent enough to precompute the
// per-command schemas; if a leaf is missing its schema (an older fisk) it returns
// an error rather than producing tools with empty schemas.
func ApplicationTools(app *fisk.ApplicationModel) ([]*Tool, error) {
	if app == nil {
		return nil, fmt.Errorf("application model is nil")
	}

	var tools []*Tool
	if app.CmdGroupModel == nil {
		return tools, nil
	}

	for _, cmd := range app.Commands {
		tools = append(tools, commandTools(cmd, nil)...)
	}

	for _, t := range tools {
		if t.Model.RestrictedSchema == nil {
			return nil, fmt.Errorf("command %q has no precomputed schema; introspect the application with a current fisk", t.Command())
		}
	}

	return tools, nil
}

// commandTools recursively turns cmd and its subcommands into tools. prefix is
// the path of command names leading to cmd. A command with subcommands is a
// grouping node and is not itself turned into a Tool; only its leaves are.
func commandTools(cmd *fisk.CmdModel, prefix []string) []*Tool {
	if cmd.Hidden {
		return nil
	}

	path := append(append([]string{}, prefix...), cmd.Name)

	if cmd.CmdGroupModel != nil && len(cmd.Commands) > 0 {
		var tools []*Tool
		for _, sub := range cmd.Commands {
			tools = append(tools, commandTools(sub, path)...)
		}
		return tools
	}

	return []*Tool{{
		Path:  path,
		Model: cmd,
	}}
}

// FilterMode selects whether a ToolFilter keeps or removes the tools it matches.
type FilterMode int

const (
	// IncludeFilter keeps only the tools that match the filter.
	IncludeFilter FilterMode = iota
	// ExcludeFilter removes the tools that match the filter.
	ExcludeFilter
)

// FilterTools applies a ToolFilter to a list of tools as either an include or an
// exclude list. A tool matches the filter when any of the filter's Tools regular
// expressions matches its tool name (the underscore-joined command path, e.g.
// "auth_user_info"), or any of the filter's Tags matches one of its tags (an
// empty tag in the filter matches a tool with no tags).
//
// Tools tagged ai:deny are always removed, regardless of mode or filter, so this
// is the enforcement point for that policy and should always be applied. A nil
// filter imposes no include/exclude restriction and keeps everything else.
func FilterTools(tools []*Tool, filter *config.ToolFilter, mode FilterMode) ([]*Tool, error) {
	var patterns []*regexp.Regexp
	if filter != nil {
		for _, pattern := range filter.Tools {
			re, err := regexp.Compile(pattern)
			if err != nil {
				return nil, fmt.Errorf("invalid tool filter pattern %q: %w", pattern, err)
			}
			patterns = append(patterns, re)
		}
	}

	var out []*Tool
	for _, t := range tools {
		if slices.Contains(t.Tags(), denyTag) {
			continue
		}

		if filter == nil {
			out = append(out, t)
			continue
		}

		matched := matchesFilter(t, filter, patterns)
		switch mode {
		case IncludeFilter:
			if matched {
				out = append(out, t)
			}
		case ExcludeFilter:
			if !matched {
				out = append(out, t)
			}
		}
	}

	return out, nil
}

// matchesFilter reports whether the Tool matches the filter's name patterns or
// tags. patterns are the pre-compiled forms of filter.Tools, matched against the
// tool name (the underscore-joined command path).
func matchesFilter(t *Tool, filter *config.ToolFilter, patterns []*regexp.Regexp) bool {
	name := t.Name()
	for _, re := range patterns {
		if re.MatchString(name) {
			return true
		}
	}

	tags := t.Tags()
	for _, tag := range filter.Tags {
		if tag == "" {
			if len(tags) == 0 {
				return true
			}
			continue
		}
		if slices.Contains(tags, tag) {
			return true
		}
	}

	return false
}

// ToolsForApp introspects the application binary at appPath and returns its
// runnable commands as Tools, each bound to appPath so it can be executed. It is
// the entry point for turning a binary into runnable tools: the binary path
// travels with the tools, so a tool that does not know how to run cannot be
// produced. Use ApplicationTools directly only when an executable path is not
// needed (for example to inspect or filter the command tree).
func ToolsForApp(appPath string) ([]*Tool, error) {
	model, err := FetchFiskAppModel(appPath)
	if err != nil {
		return nil, fmt.Errorf("introspecting %q: %w", appPath, err)
	}

	tools, err := ApplicationTools(model)
	if err != nil {
		return nil, err
	}

	for _, t := range tools {
		t.AppPath = appPath
	}

	return tools, nil
}

func FetchFiskAppModel(appPath string) (*fisk.ApplicationModel, error) {
	cmd := exec.Command(appPath, "--fisk-introspect")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, err
	}

	var appDef fisk.ApplicationModel
	err = json.Unmarshal(out, &appDef)
	if err != nil {
		return nil, err
	}

	return &appDef, nil
}
