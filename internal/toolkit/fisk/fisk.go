//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package fisk

import (
	"bytes"
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
	"github.com/choria-io/fisk-ai/internal/llm"
	"github.com/choria-io/fisk-ai/internal/toolkit"
	"github.com/choria-io/fisk-ai/internal/util"

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

// maxIntrospectBytes bounds how much of a binary's --fisk-introspect output is read
// into memory before decoding. The whole document must be valid JSON, so output past
// this cannot be truncated the way tool output is; a binary that exceeds it is
// rejected, bounding the memory an untrusted binary can force before it is parsed.
const maxIntrospectBytes = 16 * 1024 * 1024

// commandWaitDelay bounds how long a command's I/O may linger after its context
// is canceled before the process and its pipes are forcibly torn down.
const commandWaitDelay = 10 * time.Second

// introspectTimeout bounds a --fisk-introspect call when the caller's context carries
// no deadline of its own. It is a constant, not a config field: introspection is a
// fast metadata read at the top of every run, and a hung binary must not block the run
// forever. A caller that needs a different bound passes a context with its own deadline.
const introspectTimeout = 30 * time.Second

// FiskCommandTool is our intermediate representation of a fisk command exposed as a FiskCommandTool.
// It is built from an application model, filtered with FilterTools, and finally
// turned into a neutral tool definition with Definition. The captured
// command Model is the source of truth for the FiskCommandTool's schema and tags (so
// tag-based filtering is possible, which a bare tool definition cannot express)
// and, later, for mapping a FiskCommandTool call's arguments back to the command's
// arguments and flags.
type FiskCommandTool struct {
	// Path is the command path components, e.g. {"auth", "user", "add"}.
	Path []string
	// Model is the leaf command this tool represents.
	Model *fisk.CmdModel
	// AppPath is the filesystem path of the application binary this command
	// belongs to and which is executed when the tool is called. It is set by
	// ToolsForApp; it is empty for tools built directly with ApplicationTools,
	// whose handlers therefore cannot run until it is populated.
	AppPath string
	// GlobalFlags are the application-level (global) flags exposed on this command.
	// They are resolved once from the configured allowlist (unioned with the
	// application's required globals) and filtered so none collides with the
	// command's own flags or arguments. They are merged into the tool's input schema
	// (InputSchema) and, when the model supplies them, rendered onto the command line
	// after the command path (argv). It is nil when no globals are exposed.
	GlobalFlags []*fisk.FlagModel
	// SensitiveEnvVars are operator-named credential environment variables (see
	// config.CredentialEnvNames) stripped from the command's environment in addition
	// to the provider-declared set (llm.CredentialEnvNames). Like AppPath it is set by
	// ToolsForApp, so a tool that can execute (AppPath populated) always carries the
	// scrub list.
	SensitiveEnvVars []string
}

// frameworkGlobalFlags are the application-level flags fisk adds automatically:
// the help variants, shell completion hooks, the introspection hook, and version.
// None is ever exposable to the model. Exposing --help or --version would make a
// tool print usage or a version string instead of running, and the completion
// hooks are noise. The list mirrors fisk's own internal ignore set, plus version.
var frameworkGlobalFlags = map[string]bool{
	"help":                   true,
	"help-long":              true,
	"help-man":               true,
	"help-llm":               true,
	"help-compact":           true,
	"completion-bash":        true,
	"completion-script-bash": true,
	"completion-script-zsh":  true,
	"fisk-introspect":        true,
	"version":                true,
}

// Name is the LLM tool name: the command path joined with "_", e.g. the command
// "auth user add" becomes "auth_user_add".
func (t *FiskCommandTool) Name() string {
	return strings.Join(t.Path, "_")
}

// Command is the full command path as a human would type it, space separated.
func (t *FiskCommandTool) Command() string {
	return strings.Join(t.Path, " ")
}

// Description is the command help, falling back to the long help. It is the
// plain, tag-free help used for human-facing listings; ModelDescription is what
// a model sees.
func (t *FiskCommandTool) Description() string {
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
func (t *FiskCommandTool) ModelDescription() string {
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
func (t *FiskCommandTool) modelHelp() string {
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
func (t *FiskCommandTool) Tags() []string {
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
func (t *FiskCommandTool) NeedsConfirm(extraTags []string) bool {
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
func (t *FiskCommandTool) ConfirmTrigger(extraTags []string) string {
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
// present, so it is non-nil for any tool obtained that way. When the tool exposes
// application global flags they are merged in as extra properties.
func (t *FiskCommandTool) InputSchema() map[string]any {
	if len(t.GlobalFlags) == 0 {
		return t.Model.RestrictedSchema
	}

	return mergeGlobalFlags(t.Model.RestrictedSchema, t.GlobalFlags)
}

// mergeGlobalFlags returns a copy of the command's restricted schema with the
// exposed global flags added as properties. It clones the schema, its properties
// map and its required list rather than mutating the shared model schema, which is
// reused on every request, so the injected properties cannot leak into the cached
// schema or accumulate across calls. A global that is required and carries no
// default is added to the required list; otherwise it is optional. A global whose
// name is already a property is left to the command's own definition (it is
// filtered by applicableGlobals before it reaches here; this is belt and braces).
func mergeGlobalFlags(schema map[string]any, globals []*fisk.FlagModel) map[string]any {
	out := make(map[string]any, len(schema)+1)
	for k, v := range schema {
		out[k] = v
	}

	props := map[string]any{}
	if existing, ok := schema["properties"].(map[string]any); ok {
		for k, v := range existing {
			props[k] = v
		}
	}

	required := append([]string{}, toolkit.SchemaRequired(schema["required"])...)

	for _, f := range globals {
		if _, exists := props[f.Name]; exists {
			continue
		}
		props[f.Name] = globalFlagSchema(f)
		if f.Required && len(f.Default) == 0 {
			required = append(required, f.Name)
		}
	}

	out["properties"] = props
	if len(required) > 0 {
		out["required"] = required
	}

	return out
}

// globalFlagSchema builds the JSON schema property for an exposed global flag. It
// reuses the flag's own restricted schema, which after the introspection JSON
// round-trip (a flag's Value does not survive it) resolves to a boolean for a
// boolean flag and a string otherwise, wrapped in an array for a cumulative flag;
// a richer scalar type such as int or duration degrades to string, which is
// harmless as the binary re-parses and validates the value. A flag advertising
// completions is given them as an enum, and the description is prefixed so the
// model understands the argument is an application-wide global present on every
// command rather than a quirk of this one.
func globalFlagSchema(f *fisk.FlagModel) map[string]any {
	schema := f.RestrictedSchema()

	if f.Help != "" {
		schema["description"] = "Global flag: " + f.Help
	} else {
		schema["description"] = "Global flag"
	}

	if len(f.Completions) > 0 {
		if typ, _ := schema["type"].(string); typ == "string" {
			opts := make([]any, len(f.Completions))
			for i, c := range f.Completions {
				opts[i] = c
			}
			schema["enum"] = opts
		}
	}

	return schema
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
func (t *FiskCommandTool) MissingRequired(input json.RawMessage) []string {
	required := toolkit.SchemaRequired(t.InputSchema()["required"])
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
func (t *FiskCommandTool) MissingRequiredMessage(missing []string) string {
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
func (t *FiskCommandTool) parameters() (required, optional []string) {
	schema := t.InputSchema()
	required = toolkit.SchemaRequired(schema["required"])

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
func (t *FiskCommandTool) argv(args json.RawMessage) ([]string, error) {
	// The model may emit a null or empty input for a command that takes no
	// arguments; ArgsFromJSON expects an object, so normalize to an empty one.
	if len(args) == 0 || string(args) == "null" {
		args = json.RawMessage("{}")
	}

	globalTail, localArgs, err := t.splitGlobalArgs(args)
	if err != nil {
		return nil, err
	}

	tail, err := t.Model.ArgsFromJSON(localArgs)
	if err != nil {
		return nil, fmt.Errorf("building command line for %q: %w", t.Command(), err)
	}

	// Global flags are placed after the command path and ahead of the command's own
	// flags and the "--" positional separator that ArgsFromJSON emits. fisk parses
	// application-level flags at any position, and keeping them before "--" means
	// they are always read as flags, never as positional values.
	argv := append([]string{}, t.Path...)
	argv = append(argv, globalTail...)
	argv = append(argv, tail...)

	return argv, nil
}

// splitGlobalArgs separates the model's argument object into the exposed global
// flags and the command's own arguments. It renders the globals into command-line
// tokens and returns the remaining arguments as a JSON object for the command's
// own ArgsFromJSON. The globals must be rendered separately: the command's schema
// does not know them, so ArgsFromJSON would reject them as unknown properties.
//
// The globals are rendered by a synthetic command carrying only the exposed global
// flags, so fisk's own renderer decides the boolean, negatable and cumulative
// forms rather than this package re-deriving them. A non-object input is passed
// through unchanged for the command's ArgsFromJSON to reject with fisk's own error.
func (t *FiskCommandTool) splitGlobalArgs(args json.RawMessage) (globalTail []string, localArgs json.RawMessage, err error) {
	if len(t.GlobalFlags) == 0 {
		return nil, args, nil
	}

	var obj map[string]json.RawMessage
	if err := json.Unmarshal(args, &obj); err != nil {
		return nil, args, nil
	}

	globalNames := make(map[string]bool, len(t.GlobalFlags))
	for _, f := range t.GlobalFlags {
		globalNames[f.Name] = true
	}

	globalObj := map[string]json.RawMessage{}
	localObj := map[string]json.RawMessage{}
	for k, v := range obj {
		if globalNames[k] {
			globalObj[k] = v
			continue
		}
		localObj[k] = v
	}

	if len(globalObj) > 0 {
		globalJSON, err := json.Marshal(globalObj)
		if err != nil {
			return nil, nil, fmt.Errorf("building global flags for %q: %w", t.Command(), err)
		}
		synthetic := &fisk.CmdModel{FlagGroupModel: &fisk.FlagGroupModel{Flags: t.GlobalFlags}}
		globalTail, err = synthetic.ArgsFromJSON(globalJSON)
		if err != nil {
			return nil, nil, fmt.Errorf("building global flags for %q: %w", t.Command(), err)
		}
	}

	localJSON, err := json.Marshal(localObj)
	if err != nil {
		return nil, nil, fmt.Errorf("building command line for %q: %w", t.Command(), err)
	}

	return globalTail, localJSON, nil
}

// CommandLine resolves the model's JSON arguments into the full command line a
// user would type: the command path plus its arguments and flags, without the
// binary path. It is what Execute runs and what tracing displays.
func (t *FiskCommandTool) CommandLine(args json.RawMessage) (string, error) {
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
func (t *FiskCommandTool) TraceLine(args json.RawMessage) string {
	cmdline, err := t.CommandLine(args)
	if err != nil {
		cmdline = t.Command()
	}

	return util.SanitizeCommandLine(cmdline)
}

// TraceLineShort renders the trace line like TraceLine but elides the middle of
// any long argument value. It is display-only, for the scrolling transcript where
// a long argument would otherwise swamp the line; the confirm gate and CommandLine
// keep the full value, so an approval decision or an executed command is never
// shortened. The command path and flag names are never elided, only argument
// values. It falls back to the command path when the arguments cannot be resolved.
func (t *FiskCommandTool) TraceLineShort(args json.RawMessage) string {
	argv, err := t.argv(args)
	if err != nil {
		return util.SanitizeCommandLine(t.Command())
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
				argv[i] = name + "=" + util.ElideMiddle(value)
			}
		default:
			argv[i] = util.ElideMiddle(tok)
		}
	}

	return util.SanitizeCommandLine(strings.Join(argv, " "))
}

// Execute runs the command for a tool call. The model's JSON arguments are
// turned into the command line by the fisk model, the application binary is
// executed, and its output is returned as a CommandResult.
//
// A non-zero exit is reported in the result rather than as an error, so the model
// can read the failure output and react; only an inability to run the binary, or
// a canceled or timed-out context, is returned as an error.
//
// workDir is the directory the command runs in: with many runs sharing one process,
// each run passes its own so a tool writing a relative path does not collide with a
// sibling run's. It sets cmd.Dir only; it is not a sandbox, and the command can still
// write anywhere the process uid can (an absolute path, $HOME, $TMPDIR). Empty inherits
// the process working directory, today's behavior.
func (t *FiskCommandTool) Execute(ctx context.Context, args json.RawMessage, workDir string) (*toolkit.CommandResult, error) {
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
	cmd.Dir = workDir
	cmd.Env = commandEnv(t.SensitiveEnvVars, workDir)
	// Put the command in its own process group so a cancel kills its forked descendants
	// too, not just the direct child, which would otherwise leak in a long-lived server.
	configureProcessGroup(cmd)

	// The command is non-interactive: stdin is closed and stdout and stderr are
	// captured together in the order they were produced. The capturing writer keeps
	// only the head and tail of the output within a fixed memory ceiling, so a command
	// emitting far more than the cap (a runaway loop, gigabytes of logs) cannot grow
	// the process to exhaustion the way buffering the whole stream first would. Setting
	// the same writer on both streams makes os/exec serialize their writes into it, so
	// the interleaving order is preserved, as with CombinedOutput.
	capture := newCapWriter()
	cmd.Stdout = capture
	cmd.Stderr = capture
	runErr := cmd.Run()

	// A canceled or timed-out context is an execution failure, not a command
	// outcome the model should reason about, so surface it as an error.
	if ctx.Err() != nil {
		return nil, fmt.Errorf("running command %q: %w", t.Command(), ctx.Err())
	}

	result := &toolkit.CommandResult{Command: strings.Join(argv, " ")}
	result.Output, result.Truncated = capture.result()

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

// commandEnv builds the environment for a tool command: the current environment
// with credential variables removed and LLMFORMAT set. LLMFORMAT signals fisk
// applications that their output is read by an LLM, so they can render a form
// suited to that rather than a terminal.
//
// The stripped set is the union of two name sources, so a tool whose command line
// the model chooses can never read a named secret from its environment:
//   - llm.CredentialEnvNames(): the secret-bearing variables every llm provider
//     linked into this build declared at registration (see internal/llm). This
//     covers all compiled providers, not just the active one, and replaces what was
//     once a static anthropic list maintained here.
//   - extra: operator-configured credential variables (a tool's SensitiveEnvVars,
//     from config.CredentialEnvNames).
//
// The guarantee is name-based: it removes the variables identified as secrets, so a
// tool cannot read them from its environment. It cannot catch the same secret value
// an operator also exported under a second, unnamed variable. Empty names are
// ignored.
//
// workDir, when set, becomes the child's PWD. cmd.Dir changes the actual working
// directory but leaves PWD inherited from the parent, so a shell wrapper or anything
// reading $PWD would otherwise disagree with os.Getwd. The inherited PWD is dropped
// and the canonical one appended, so the child never sees two. TMPDIR and HOME are
// deliberately left as inherited: repointing TMPDIR into workDir would make a tool's
// temp files collide with its own output there and would hand a tool that cleans
// TMPDIR the power to delete that output, and repointing HOME breaks tools that read
// their config or credentials from it. So $HOME-, $TMPDIR- and absolute-path writes
// stay shared across runs; this is collision avoidance for relative writes, not a
// sandbox.
func commandEnv(extra []string, workDir string) []string {
	current := os.Environ()
	out := make([]string, 0, len(current)+1)

	provider := llm.CredentialEnvNames()

	for _, kv := range current {
		name, _, found := strings.Cut(kv, "=")
		if !found {
			out = append(out, kv)
			continue
		}
		if name != "" && (slices.Contains(provider, name) || slices.Contains(extra, name)) {
			continue
		}
		// The canonical PWD is appended below when a workDir is set, so drop any
		// inherited one rather than leave the child with two conflicting entries.
		if workDir != "" && name == "PWD" {
			continue
		}
		out = append(out, kv)
	}

	if workDir != "" {
		out = append(out, "PWD="+workDir)
	}

	return append(out, "LLMFORMAT=1")
}

// capWriter is an io.Writer that retains at most the head and tail of everything
// written to it, so a subprocess emitting far more than maxToolOutputBytes cannot
// grow the process memory. It buffers the first maxToolOutputBytes in head and the
// most recent half in a fixed-size tail ring, discarding the middle. result then
// reproduces the same head/marker/tail truncation the model saw before, whether or
// not the whole stream would have fit in memory. It is written by a single goroutine
// at a time (os/exec guarantees this when it backs both Stdout and Stderr), so it
// needs no lock.
type capWriter struct {
	head    []byte // first up to maxToolOutputBytes, in arrival order
	tail    []byte // ring buffer holding the most recent up to len(tail) bytes
	tailLen int    // number of valid bytes in the ring
	tailPos int    // next write index in the ring
	total   int64  // total bytes written, to decide whether truncation happened
}

func newCapWriter() *capWriter {
	return &capWriter{
		head: make([]byte, 0, maxToolOutputBytes),
		tail: make([]byte, maxToolOutputBytes/2),
	}
}

func (w *capWriter) Write(p []byte) (int, error) {
	w.total += int64(len(p))

	if len(w.head) < maxToolOutputBytes {
		room := maxToolOutputBytes - len(w.head)
		if room > len(p) {
			room = len(p)
		}
		w.head = append(w.head, p[:room]...)
	}

	w.writeTail(p)

	return len(p), nil
}

// writeTail keeps only the most recent len(w.tail) bytes across all writes. A write
// at least the ring's size supersedes the whole ring, so it is copied in bulk rather
// than byte by byte, keeping a gigabyte-scale stream cheap.
func (w *capWriter) writeTail(p []byte) {
	tl := len(w.tail)
	if len(p) >= tl {
		copy(w.tail, p[len(p)-tl:])
		w.tailPos = 0
		w.tailLen = tl
		return
	}

	for _, b := range p {
		w.tail[w.tailPos] = b
		w.tailPos = (w.tailPos + 1) % tl
		if w.tailLen < tl {
			w.tailLen++
		}
	}
}

// result returns the captured output and whether it was truncated. Below the cap the
// head holds the whole stream and is returned verbatim; above it, the first half of
// head and the tail ring are joined with a marker, matching the old capOutput form.
func (w *capWriter) result() (string, bool) {
	if w.total <= maxToolOutputBytes {
		return string(w.head), false
	}

	const marker = "\n...[output truncated]...\n"
	half := maxToolOutputBytes / 2

	start := (w.tailPos - w.tailLen + len(w.tail)) % len(w.tail)
	tail := make([]byte, w.tailLen)
	for i := 0; i < w.tailLen; i++ {
		tail[i] = w.tail[(start+i)%len(w.tail)]
	}

	return string(w.head[:half]) + marker + string(tail), true
}

// ApplicationTools turns every runnable command of a fisk application model into
// a FiskCommandTool. The command tree is walked recursively and each leaf command (one with
// no subcommands) becomes a FiskCommandTool named by its full command path; non-leaf
// (grouping) commands and hidden commands, with their subtrees, are skipped.
//
// The model must come from a fisk introspection recent enough to precompute the
// per-command schemas; if a leaf is missing its schema (an older fisk) it returns
// an error rather than producing tools with empty schemas.
//
// globalFlags is the operator's allowlist of application global flags to expose to
// the model (see config.Config.GlobalFlags). Every leaf tool carries the exposed
// globals applicable to it; a name that matches no exposable global is an error.
func ApplicationTools(app *fisk.ApplicationModel, globalFlags ...string) ([]*FiskCommandTool, error) {
	if app == nil {
		return nil, fmt.Errorf("application model is nil")
	}

	exposed, err := resolveExposedGlobals(app, globalFlags)
	if err != nil {
		return nil, err
	}

	var tools []*FiskCommandTool
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
		t.GlobalFlags = applicableGlobals(exposed, t.Model)
	}

	return tools, nil
}

// resolveExposedGlobals selects the application global flags to expose to the model
// from the model's application-level flags. A flag is exposed when it is named in
// the allowlist or when the application marks it required: a required global is
// always exposed, listed or not, because the command cannot run without it. A name
// that matches no global at all, or one that resolves to a framework flag (help,
// version, ...) or a hidden flag, is an error, so a typo or an attempt to surface a
// sensitive-by-omission flag fails loudly at load rather than silently exposing
// nothing. The two error cases are distinguished so the operator is not sent
// hunting for a typo that is not there.
func resolveExposedGlobals(app *fisk.ApplicationModel, allowlist []string) ([]*fisk.FlagModel, error) {
	byName := map[string]*fisk.FlagModel{}
	if app.FlagGroupModel != nil {
		for _, f := range app.Flags {
			byName[f.Name] = f
		}
	}

	var out []*fisk.FlagModel
	seen := map[string]bool{}

	for _, name := range allowlist {
		f, ok := byName[name]
		if !ok {
			return nil, fmt.Errorf("global_flags entry %q matches no global flag of application %q; run `fisk-ai info` to list the exposable global flags", name, app.Name)
		}
		if frameworkGlobalFlags[f.Name] || f.Hidden {
			return nil, fmt.Errorf("global_flags entry %q is a hidden or framework flag and cannot be exposed to the model; only application-defined global flags can be exposed", name)
		}
		if seen[f.Name] {
			continue
		}
		seen[f.Name] = true
		out = append(out, f)
	}

	// A required global is always exposed, listed or not: the command cannot run
	// without it, so the model must be able to supply it.
	if app.FlagGroupModel != nil {
		for _, f := range app.Flags {
			if !f.Required || seen[f.Name] || frameworkGlobalFlags[f.Name] {
				continue
			}
			seen[f.Name] = true
			out = append(out, f)
		}
	}

	return out, nil
}

// applicableGlobals returns the exposed globals that do not collide with the
// command's own flags or positional arguments. A command that already defines a
// flag or argument of the same name keeps its own: the local one wins and the
// global is skipped for that command, so a call can never be ambiguous.
func applicableGlobals(exposed []*fisk.FlagModel, cmd *fisk.CmdModel) []*fisk.FlagModel {
	if len(exposed) == 0 {
		return nil
	}

	local := map[string]bool{}
	if cmd.FlagGroupModel != nil {
		for _, f := range cmd.Flags {
			local[f.Name] = true
		}
	}
	if cmd.ArgGroupModel != nil {
		for _, a := range cmd.Args {
			local[a.Name] = true
		}
	}

	var out []*fisk.FlagModel
	for _, f := range exposed {
		if local[f.Name] {
			continue
		}
		out = append(out, f)
	}

	return out
}

// commandTools recursively turns cmd and its subcommands into tools. prefix is
// the path of command names leading to cmd. A command with subcommands is a
// grouping node and is not itself turned into a FiskCommandTool; only its leaves are.
func commandTools(cmd *fisk.CmdModel, prefix []string) []*FiskCommandTool {
	if cmd.Hidden {
		return nil
	}

	path := append(append([]string{}, prefix...), cmd.Name)

	if cmd.CmdGroupModel != nil && len(cmd.Commands) > 0 {
		var tools []*FiskCommandTool
		for _, sub := range cmd.Commands {
			tools = append(tools, commandTools(sub, path)...)
		}
		return tools
	}

	return []*FiskCommandTool{{
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
func FilterTools(tools []*FiskCommandTool, filter *config.ToolFilter, mode FilterMode) ([]*FiskCommandTool, error) {
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

	var out []*FiskCommandTool
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

// matchesFilter reports whether the FiskCommandTool matches the filter's name patterns or
// tags. patterns are the pre-compiled forms of filter.Tools, matched against the
// tool name (the underscore-joined command path).
func matchesFilter(t *FiskCommandTool, filter *config.ToolFilter, patterns []*regexp.Regexp) bool {
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
//
// credentialEnvNames (see config.CredentialEnvNames) is stored on every returned
// tool as SensitiveEnvVars and used to scrub the introspection subprocess, so both
// executions of the operator's binary run without the agent's named credentials.
func ToolsForApp(ctx context.Context, appPath string, credentialEnvNames []string, globalFlags ...string) ([]*FiskCommandTool, error) {
	// FetchFiskAppModel already names the binary in its errors, so it is returned as-is
	// rather than wrapped again with a second "introspecting %q".
	model, err := FetchFiskAppModel(ctx, appPath, credentialEnvNames)
	if err != nil {
		return nil, err
	}

	tools, err := ApplicationTools(model, globalFlags...)
	if err != nil {
		return nil, err
	}

	for _, t := range tools {
		t.AppPath = appPath
		t.SensitiveEnvVars = credentialEnvNames
	}

	return tools, nil
}

// FetchFiskAppModel runs the application's --fisk-introspect hook and decodes its
// command model. credentialEnvNames are stripped from the subprocess environment
// (alongside the static sensitive set) so the operator's binary never sees the
// agent's named credentials, even at introspection time.
//
// It runs on the LoadTools path at the top of every run, so a hung binary would
// otherwise block the run forever. ctx governs cancellation; when ctx carries no
// deadline of its own the default introspectTimeout is applied, so a run whose caller
// did not bound it is still protected. WaitDelay bounds the I/O teardown after a
// cancel, matching Execute, so a canceled introspection does not leave a lingering
// pipe reader.
func FetchFiskAppModel(ctx context.Context, appPath string, credentialEnvNames []string) (*fisk.ApplicationModel, error) {
	// Apply the default bound only when the caller supplied none, so a caller that
	// already bounded (or deliberately left unbounded and then bounded elsewhere) its
	// context is not silently re-clamped, and so the timeout error can name the window.
	defaultTimeout := false
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, introspectTimeout)
		defer cancel()
		defaultTimeout = true
	}

	cmd := exec.CommandContext(ctx, appPath, "--fisk-introspect")
	cmd.WaitDelay = commandWaitDelay
	// Introspection deliberately runs in the process working directory, not a per-run
	// ToolWorkDir: it is a one-time read at run start, so it never collides, and a
	// binary that reads a relative config at introspect time must see the same one the
	// operator would, or the exposed tool set could differ per run.
	cmd.Env = commandEnv(credentialEnvNames, "")
	// A slow introspection binary that forks leaves no orphan when the timeout fires.
	configureProcessGroup(cmd)

	// Both streams feed one capped buffer, matching CombinedOutput's ordering while
	// bounding the memory the binary can force: output past the ceiling is discarded
	// and flagged rather than buffered, and the over-large document is then rejected.
	buf := &cappedBuffer{limit: maxIntrospectBytes}
	cmd.Stdout = buf
	cmd.Stderr = buf
	err := cmd.Run()

	// A canceled or timed-out context is an introspection failure with a specific
	// cause, not a decode problem. When it is the default bound, name the window and the
	// expectation so an operator knows the binary must answer within it.
	if ctx.Err() != nil {
		if defaultTimeout && errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("introspecting %q timed out after %s; the binary must respond within this window", appPath, introspectTimeout)
		}
		return nil, fmt.Errorf("introspecting %q: %w", appPath, ctx.Err())
	}
	if err != nil {
		return nil, fmt.Errorf("introspecting %q: %w", appPath, err)
	}
	if buf.exceeded {
		return nil, fmt.Errorf("introspecting %q produced more than %d bytes; the binary must emit a bounded introspection document", appPath, maxIntrospectBytes)
	}

	var appDef fisk.ApplicationModel
	err = json.Unmarshal(buf.buf.Bytes(), &appDef)
	if err != nil {
		return nil, fmt.Errorf("introspecting %q: decoding the introspection document: %w", appPath, err)
	}

	return &appDef, nil
}

// cappedBuffer accumulates up to limit bytes and records whether more arrived. It
// backs a subprocess read whose output must be decoded whole and so cannot be
// truncated: past the limit the excess is counted and discarded rather than buffered,
// bounding memory, and exceeded lets the caller reject an over-large document. Write
// always reports full consumption so the os/exec copier keeps draining the pipe.
type cappedBuffer struct {
	buf      bytes.Buffer
	limit    int
	exceeded bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	room := c.limit - c.buf.Len()
	switch {
	case room >= len(p):
		c.buf.Write(p)
	case room > 0:
		c.buf.Write(p[:room])
		c.exceeded = true
	case len(p) > 0:
		c.exceeded = true
	}

	return len(p), nil
}

// GlobalFlagInfo describes an application global flag for display by the info
// command: its name and help, whether the current configuration exposes it to the
// model, and whether the application marks it required (which exposes it
// regardless of the allowlist).
type GlobalFlagInfo struct {
	Name     string
	Help     string
	Exposed  bool
	Required bool
}

// AppGlobalFlags introspects the application at cfg.ApplicationPath and returns its
// exposable global flags: every application-level flag that is not hidden or a
// framework flag (help, version, ...). Each is marked with whether cfg exposes it,
// either by naming it under global_flags or because the application marks it
// required. It backs the info command's listing of which globals exist and which
// the operator has allowlisted.
func AppGlobalFlags(ctx context.Context, cfg *config.Config) ([]GlobalFlagInfo, error) {
	// With no wrapped application there is nothing to introspect and so no global
	// flags to report.
	if cfg.ApplicationPath == "" {
		return nil, nil
	}

	// FetchFiskAppModel already names the binary in its errors, so it is returned as-is.
	model, err := FetchFiskAppModel(ctx, cfg.ApplicationPath, cfg.CredentialEnvNames())
	if err != nil {
		return nil, err
	}

	allow := map[string]bool{}
	for _, name := range cfg.GlobalFlagNames() {
		allow[name] = true
	}

	var out []GlobalFlagInfo
	if model.FlagGroupModel == nil {
		return out, nil
	}
	for _, f := range model.Flags {
		if f.Hidden || frameworkGlobalFlags[f.Name] {
			continue
		}
		out = append(out, GlobalFlagInfo{
			Name:     f.Name,
			Help:     f.Help,
			Exposed:  allow[f.Name] || f.Required,
			Required: f.Required,
		})
	}

	return out, nil
}
