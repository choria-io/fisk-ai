//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/goccy/go-yaml"
)

// identityPattern constrains the agent identity to characters that are also legal
// in a single NATS subject token and queue group, since the identity is used as
// the discovery queue group. It rejects whitespace, '.', '*', '>', and anything
// else that would form an invalid or wildcard-bearing subject.
var identityPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// defaultIdentity is the identity used when neither an explicit identity nor an
// application_path (whose basename would otherwise supply one) is set. It keeps
// the identity a legal NATS token and the memory/knowledge store paths stable for
// an application-less agent.
const defaultIdentity = "fisk-ai"

// Default budget values applied wherever a config leaves a value unset.
const (
	defaultLLMMaxTokens     = 200000
	defaultLLMMaxIterations = 50
	defaultLLMCallTimeout   = 120 * time.Second
)

// Config is the top-level agent configuration.
type Config struct {
	// Identity is the name used in discovery; it doubles as a queue group so
	// multiple agents sharing an identity share the work. Optional if MCP.
	Identity string `json:"identity" yaml:"identity"`
	// ApplicationPath is the app to run and introspect for tools.
	ApplicationPath string `json:"application_path" yaml:"application_path"`
	// NatsContext is the name of a NATS context (as managed by `nats context`
	// and resolved by jsm.go/natscontext) used to connect to NATS for importing
	// remote tools and for the a2a server. Required when RemoteTools is set or in
	// server mode.
	NatsContext string `json:"nats_context,omitempty" yaml:"nats_context,omitempty"`
	// SystemPrompt describes what we are doing and may be long; think of it as a
	// single-skill agent where this is the skill. Optional if MCP.
	SystemPrompt string `json:"system_prompt" yaml:"system_prompt"`

	// Exclude filters tools out. By default the entire command becomes tools
	// (regex matching); this lets you take `nats` and only expose `nats auth`.
	Exclude *ToolFilter `json:"exclude,omitempty" yaml:"exclude,omitempty"`
	// Include restricts tools to a specific set; it can never include `ai:deny`.
	Include *ToolFilter `json:"include,omitempty" yaml:"include,omitempty"`
	// GlobalFlags is an allowlist of the wrapped application's global (application-
	// level) flag names to expose to the model as an argument on every leaf command
	// tool. It is how an operator surfaces a safe global such as nats's --context,
	// which selects a stored connection profile, while keeping sensitive globals such
	// as --user and --password hidden from the model. A name is the long flag name,
	// with or without the leading dashes; each is validated against the application's
	// real global flags when tools are loaded, and a name matching no exposable global
	// is an error. Hidden and framework flags (help, version, ...) cannot be exposed. A
	// name that collides with a command's own flag or argument is skipped for that
	// command. A global the application marks required is always exposed, listed here
	// or not, since the command cannot run without it.
	GlobalFlags []string `json:"global_flags,omitempty" yaml:"global_flags,omitempty"`
	// RemoteAgents are remote agents we can talk to using a2a-like behaviors.
	RemoteAgents []RemoteAgent `json:"remote_agents,omitempty" yaml:"remote_agents,omitempty"`
	// RemoteTools are remote agents we pull in all the tools of.
	RemoteTools []RemoteToolHost `json:"remote_tools,omitempty" yaml:"remote_tools,omitempty"`
	// Expose makes this agent discoverable to other agents and/or over MCP.
	Expose *ExposeConfig `json:"expose,omitempty" yaml:"expose,omitempty"`
	// Harness groups the settings that govern how the agent harness itself behaves
	// during a run: the human-in-the-loop tools, the confirmation gate tags, and the
	// terminal UI switches. It is optional; its zero value leaves every setting at its
	// default (human-in-the-loop off, no extra confirm tags, TUI on, bell on).
	Harness HarnessConfig `json:"harness,omitempty" yaml:"harness,omitempty"`

	// LLM is the model to use and general LLM setup. Always required.
	LLM LLMConfig `json:"llm" yaml:"llm"`
}

// HarnessConfig groups the settings that govern how the agent harness behaves
// during a run, as distinct from the model (llm) or the tool selection. Every
// field is optional and its zero value is the default behavior.
type HarnessConfig struct {
	// HumanInTheLoop, when enabled, gives the model a built-in in-process tool to
	// put a question to the operator at the terminal. Agent mode only; it is never
	// exposed over MCP and needs an interactive terminal.
	HumanInTheLoop *HumanInTheLoopConfig `json:"human_in_the_loop,omitempty" yaml:"human_in_the_loop,omitempty"`
	// ConfirmTags lists command tags that, in addition to the always-on ai:confirm
	// tag, require the operator's explicit approval before a tagged command runs.
	// Matching is exact (not a regex) and additive to ai:confirm. In the agent loop
	// it gates against the operator; over MCP it gates through client elicitation as
	// governed by expose.agent.mcp.confirm_over_mcp.
	ConfirmTags []string `json:"confirm_tags,omitempty" yaml:"confirm_tags,omitempty"`
	// NoTUI disables the full-screen terminal UI for this agent, always using the
	// line-by-line output even on an interactive terminal. It is a hard off switch
	// that the command line cannot re-enable, for agents whose operators rely on the
	// line UI (piping, screen readers). The TUI is otherwise the default on a terminal.
	NoTUI bool `json:"no_tui,omitempty" yaml:"no_tui,omitempty"`
	// NoBell silences the terminal bell the full-screen UI rings when a run blocks
	// waiting on an operator decision (an approval gate or an ask_human_* prompt). The
	// bell is on by default so an operator who looked away is alerted; set this for an
	// agent that prompts often, or where an audible bell is unwelcome. Like no_tui it
	// is a negative switch, and it has no effect in the line UI.
	NoBell bool `json:"no_bell,omitempty" yaml:"no_bell,omitempty"`
	// Memory, when enabled, gives the model built-in tools to keep durable notes in
	// a key/value store that survives across runs. Agent mode only; like the
	// human-in-the-loop tools it is not exposed over MCP.
	Memory *MemoryConfig `json:"memory,omitempty" yaml:"memory,omitempty"`
	// RAG, when enabled, gives the model a built-in knowledge_search tool over a
	// locally-built index of the operator's markdown/text documents. The user-facing
	// name is "knowledge" (the config block, the CLI command, and the tool); the Go
	// identifiers keep the rag prefix since RAG is the technique. It has two tiers: a
	// lexical FTS5 baseline that is always on when enabled, and an opt-in vector tier
	// active only when the embeddings sub-block is present.
	RAG *RAGConfig `json:"knowledge,omitempty" yaml:"knowledge,omitempty"`
	// Sessions selects and configures the store that holds checkpointed run
	// journals. It is deliberately NOT parsed from the config file yet (yaml:"-"):
	// it is synthesized at boot from the --state-dir flag and defaults (see
	// SessionConfigFromStateDir), so the construction path is already the one a
	// future YAML block will use. Exposing it to the file is a designed change (it
	// needs strict options decoding, unknown-backend validation, and the
	// --state-dir precedence rule), not a one-line tag flip; when that lands, change
	// the tag to yaml:"sessions,omitempty" and route it through the same canonical
	// JSON path memory uses for its options block.
	Sessions *SessionConfig `json:"-" yaml:"-"`
}

// SessionConfig selects and configures the session store backend. Its shape
// mirrors MemoryConfig: a backend name and a raw per-backend options block decoded
// against a typed schema at store construction, so an unknown option key fails as
// loudly as an unknown top-level key. For the file backend the options accept
// {directory: <path>}, defaulting to the absolute XDG state directory.
type SessionConfig struct {
	// Backend selects the store implementation. It defaults to "file", the only
	// backend today, which keeps each run in a JSON-lines journal under a directory.
	Backend string `json:"backend,omitempty" yaml:"backend,omitempty"`
	// Options carries backend-specific settings as a raw block, decoded against a
	// typed per-backend schema at store construction. For the file backend it
	// accepts {directory: <path>}.
	Options json.RawMessage `json:"options,omitempty" yaml:"options,omitempty"`
}

// BackendName returns the configured backend, defaulting to "file". It is
// nil-safe: sessions are always available (checkpointing is not a feature that can
// be disabled), so a nil config resolves to the file backend rather than to an
// empty name that would fail lookup.
func (s *SessionConfig) BackendName() string {
	if s == nil || s.Backend == "" {
		return "file"
	}

	return s.Backend
}

// RawOptions returns the raw backend options block, decoded per backend at store
// construction. It is nil-safe and nil when no options are set.
func (s *SessionConfig) RawOptions() json.RawMessage {
	if s == nil {
		return nil
	}

	return s.Options
}

// SessionConfigFromStateDir synthesizes the session config from the --state-dir
// flag. An empty dir yields the file backend with no options, so the file backend
// applies its default (the absolute XDG state directory); a set dir populates the
// file backend's directory option. The flag is the sole source today and always
// wins: when a YAML block is later added, this override is applied last so an
// explicit --state-dir still takes precedence over a configured directory.
func SessionConfigFromStateDir(dir string) *SessionConfig {
	if dir == "" {
		return &SessionConfig{Backend: "file"}
	}

	return &SessionConfig{
		Backend: "file",
		Options: json.RawMessage(fmt.Sprintf(`{"directory":%q}`, dir)),
	}
}

// RAGConfig configures the built-in knowledge_search tool and the backing SQLite
// index. It is written by the operator as harness.knowledge. The lexical tier is
// always available when enabled; the vector tier turns on only when Embeddings is
// present. The index stores verbatim document text UNENCRYPTED on disk (file mode
// 0600), the same posture as the memory feature, so do not index secrets.
type RAGConfig struct {
	// Enabled turns the knowledge_search tool on. The block being absent, or present
	// with enabled false, leaves it off.
	Enabled bool `json:"enabled" yaml:"enabled"`
	// Paths are the default index roots used by knowledge index when no explicit path
	// is given. It is not an error for this to be empty, but then knowledge index
	// requires an explicit path argument.
	Paths []string `json:"paths,omitempty" yaml:"paths,omitempty"`
	// Directory is where the SQLite index lives. It is resolved relative to the
	// working directory when not absolute, and defaults to knowledge/<identity>,
	// mirroring harness.memory's directory. It is project-local and excluded from its
	// own index walk.
	Directory string `json:"directory,omitempty" yaml:"directory,omitempty"`
	// TopK is the default number of chunks knowledge_search returns when the model
	// does not request a specific count. It defaults to 5 and is clamped to a hard
	// ceiling of 20.
	TopK int `json:"top_k,omitempty" yaml:"top_k,omitempty"`
	// MaxInjectedTokens caps the total retrieved text fed to the model in one search
	// result. It defaults to 6000.
	MaxInjectedTokens int `json:"max_injected_tokens,omitempty" yaml:"max_injected_tokens,omitempty"`
	// Embeddings, when present, turns on the hybrid vector tier. Its absence leaves
	// the feature lexical-only, needing no model and no external service.
	Embeddings *RAGEmbeddingsConfig `json:"embeddings,omitempty" yaml:"embeddings,omitempty"`
}

// RAGEmbeddingsConfig configures the optional vector tier: a local
// OpenAI-compatible embeddings server contacted only when this block is present.
// The model is user-chosen; we make no assumptions about its dimension or prefix
// needs and pin whatever it emits in the index manifest.
type RAGEmbeddingsConfig struct {
	// BaseURL is the OpenAI-compatible base; embeddings are POSTed to
	// <base_url>/embeddings. A non-loopback base_url must use https.
	BaseURL string `json:"base_url" yaml:"base_url"`
	// Model is the embedding model name passed to the server.
	Model string `json:"model" yaml:"model"`
	// APIKeyEnv is the NAME of an environment variable holding a bearer token, never
	// the secret itself. When set the token is sent as Authorization: Bearer.
	APIKeyEnv string `json:"api_key_env,omitempty" yaml:"api_key_env,omitempty"`
	// TimeoutString is the per-request timeout as a duration string, e.g. 30s. It
	// defaults to 30s.
	TimeoutString string `json:"timeout,omitempty" yaml:"timeout,omitempty"`
	// TimeoutParsed is the parsed form of TimeoutString.
	TimeoutParsed time.Duration `json:"-" yaml:"-"`
	// QueryPrefix is prepended to a query before embedding. Default empty, since the
	// model is user-chosen and a wrong prefix is worse than none.
	QueryPrefix string `json:"query_prefix,omitempty" yaml:"query_prefix,omitempty"`
	// DocumentPrefix is prepended to each chunk before embedding; it supports a
	// {title} placeholder filled from the chunk's heading. Default empty.
	DocumentPrefix string `json:"document_prefix,omitempty" yaml:"document_prefix,omitempty"`
}

// MemoryConfig configures the built-in memory tools and the backing store. The
// options block is decoded per backend, so its legal keys depend on backend; for
// the file backend it accepts a single directory.
type MemoryConfig struct {
	// Enabled turns the memory tools on. The block being absent, or present with
	// enabled false, leaves them off.
	Enabled bool `json:"enabled" yaml:"enabled"`
	// Backend selects the store implementation. It defaults to "file", the only
	// backend today, which keeps each memory in a markdown file under a directory.
	Backend string `json:"backend,omitempty" yaml:"backend,omitempty"`
	// NoIndex opts out of injecting the list of stored memories (key and
	// description) into the system prompt at run start. The index is on by default
	// so the model knows what it has saved without having to call memory_list; set
	// this to keep the store's contents out of the prompt. Like no_tui it is a
	// negative switch.
	NoIndex bool `json:"no_index,omitempty" yaml:"no_index,omitempty"`
	// Options carries backend-specific settings as a raw block, decoded against a
	// typed per-backend schema at store construction so an unknown option key fails
	// as loudly as an unknown top-level key. For the file backend it accepts
	// {directory: <path>}, defaulting to memory/<identity>.
	Options json.RawMessage `json:"options,omitempty" yaml:"options,omitempty"`
}

// Confirm-over-MCP policies for ExposedMCPConfig.ConfirmOverMCP.
const (
	// ConfirmOverMCPAuto asks a client that supports elicitation to approve each
	// confirm-tagged command and runs it ungated for a client that does not. It is
	// the default when confirm_over_mcp is unset.
	ConfirmOverMCPAuto = "auto"
	// ConfirmOverMCPAlways requires approval for every confirm-tagged command: a
	// client that cannot elicit is refused rather than allowed to run it ungated.
	ConfirmOverMCPAlways = "always"
	// ConfirmOverMCPNever never asks over MCP; confirm-tagged commands run ungated
	// regardless of client support, for operators who rely on the client's own
	// approval UI and want to avoid a second prompt.
	ConfirmOverMCPNever = "never"
)

// KnowledgeSearchToolName is the name of the read-only knowledge_search built-in
// tool. It is the only built-in that may be served over MCP. It is defined here,
// the lowest layer, so config can validate the expose.agent.mcp.builtins allowlist
// without importing the util package that implements the tool; util references this
// same constant so the two never drift.
const KnowledgeSearchToolName = "knowledge_search"

// HumanInTheLoopConfig configures the built-in human-in-the-loop tools, which let
// the model ask the operator a question at the terminal during an agent run.
type HumanInTheLoopConfig struct {
	// Enabled turns the human-in-the-loop tools on. The block being absent, or
	// present with enabled false, leaves them off.
	Enabled bool `json:"enabled" yaml:"enabled"`
}

// Anthropic model identifiers usable as an LLMConfig.Model value.
const (
	// ModelClaudeFable5 is the Claude Fable 5 model, the most capable widely
	// released model for the most demanding reasoning and long-horizon agentic
	// work; the slowest and most expensive tier.
	ModelClaudeFable5 = "claude-fable-5"
	// ModelClaudeOpus48 is the Claude Opus 4.8 model, the most capable Opus tier;
	// slowest and most expensive Opus, best for hard reasoning and agentic work.
	ModelClaudeOpus48 = "claude-opus-4-8"
	// ModelClaudeOpus47 is the Claude Opus 4.7 model, the prior Opus release.
	ModelClaudeOpus47 = "claude-opus-4-7"
	// ModelClaudeOpus46 is the Claude Opus 4.6 model, an earlier Opus release.
	ModelClaudeOpus46 = "claude-opus-4-6"
	// ModelClaudeOpus45 is the Claude Opus 4.5 model, an earlier Opus release.
	ModelClaudeOpus45 = "claude-opus-4-5-20251101"
	// ModelClaudeSonnet5 is the Claude Sonnet 5 model, the mid tier balancing
	// capability, speed, and cost; a good general-purpose default.
	ModelClaudeSonnet5 = "claude-sonnet-5"
	// ModelClaudeSonnet46 is the Claude Sonnet 4.6 model, the prior Sonnet release.
	ModelClaudeSonnet46 = "claude-sonnet-4-6"
	// ModelClaudeSonnet45 is the Claude Sonnet 4.5 model, an earlier Sonnet release.
	ModelClaudeSonnet45 = "claude-sonnet-4-5-20250929"
	// ModelClaudeHaiku45 is the Claude Haiku 4.5 model, the fastest and cheapest
	// tier; best for high-throughput, latency-sensitive, or simpler tasks.
	ModelClaudeHaiku45 = "claude-haiku-4-5-20251001"
)

// LLMConfig holds the model to use and general LLM setup.
type LLMConfig struct {
	// Model is the LLM model to use, e.g. ModelClaudeSonnet5 ("claude-sonnet-5").
	Model string `json:"model" yaml:"model"`
	// Budget bounds LLM usage; optional but recommended for long running agents.
	Budget LLMBudget `json:"budget" yaml:"budget"`
	// Thinking configures whether the model exposes its reasoning. Off by default:
	// when off, no thinking is requested and the model uses its default behavior.
	Thinking ThinkingConfig `json:"thinking" yaml:"thinking"`
	// NoPromptCache disables Anthropic prompt caching for this agent. Caching is on by
	// default (the zero value), mirroring no_tui / no_bell; set it only for a non-Anthropic
	// endpoint (ANTHROPIC_BASE_URL) whose proxy rejects or ignores cache_control. Disabling
	// only raises cost, it never changes output.
	NoPromptCache bool `json:"no_prompt_cache,omitempty" yaml:"no_prompt_cache,omitempty"`
}

// ThinkingConfig configures whether the model exposes its reasoning. It is a
// struct rather than a bare bool so further controls (e.g. effort) can be added
// later without changing the configuration shape. The setting is provider
// neutral: the active backend maps it to its own mechanism. Older Anthropic
// models that predate adaptive thinking (e.g. Sonnet 4.5, Haiku 4.5) reject it,
// so it is left off by default and opted into per agent.
type ThinkingConfig struct {
	// Enabled turns model thinking on. Off by default.
	Enabled bool `json:"enabled" yaml:"enabled"`
}

// LLMBudget bounds how much an agent may spend on the LLM.
//
// MaxTokens is a tokens-processed cap, not a dollar cap: it sums the full input
// throughput (uncached input plus cache reads and cache writes) and the output,
// so its magnitude matches the pre-cache world and a resume stays consistent.
// Prompt caching makes a run far cheaper in dollars than the token count implies
// (a cache read costs roughly a tenth of an uncached input token), so MaxTokens
// intentionally over-counts real spend; a cost-weighted budget is a separate
// future feature.
type LLMBudget struct {
	// MaxTokens is the maximum number of tokens to spend. It is a soft cap: the
	// running total is checked after each call, so a single call can overshoot it
	// by up to that call's input plus its max output tokens before the run stops.
	MaxTokens int64 `json:"max_tokens" yaml:"max_tokens"`
	// MaxIterations is the maximum number of LLM iterations to perform.
	MaxIterations int64 `json:"max_iterations" yaml:"max_iterations"`
	// CallTimeoutString is the per-call timeout as a duration string, e.g. 60s.
	CallTimeoutString string `json:"call_timeout" yaml:"call_timeout"`
	// CallTimeoutParsed is the parsed form of CallTimeoutString.
	CallTimeoutParsed time.Duration `json:"-" yaml:"-"`
}

// ExposeConfig controls how this agent is exposed to others.
type ExposeConfig struct {
	// Agent listens on a subject for a prompt, tools etc, making it discoverable.
	Agent *AgentExpose `json:"agent,omitempty" yaml:"agent,omitempty"`
}

// AgentExpose configures the agent-facing exposure of this agent.
type AgentExpose struct {
	// AgentToAgent opts this agent in to serving its tools to other agents over
	// a2a. It is the switch for the `fisk-ai a2a` command, which refuses to start
	// unless it is true; an agent that says nothing serves nothing.
	AgentToAgent bool `json:"agent_to_agent" yaml:"agent_to_agent"`
	// MCP opts this agent in to serving its tools over MCP and carries the listen
	// port. Its presence is the switch for the `fisk-ai mcp` command, which refuses
	// to start unless it is set.
	MCP *ExposedMCPConfig `json:"mcp,omitempty" yaml:"mcp,omitempty"`
	// Tools optionally narrows the served set, applied on top of the top-level
	// include/exclude; it can only remove tools, never add them. When absent the
	// whole top-level-selected set is served.
	Tools *ExposedToolSelection `json:"tools,omitempty" yaml:"tools,omitempty"`
}

// ExposedToolSelection narrows the served tool set, on top of the top-level
// include/exclude. Each filter is honored only when it carries patterns or tags.
type ExposedToolSelection struct {
	// Exclude drops tools from the served set. By default the entire command
	// becomes tools (regex matching); this lets you take `nats` and only serve
	// `nats auth`.
	Exclude *ToolFilter `json:"exclude,omitempty" yaml:"exclude,omitempty"`
	// Include restricts the served set to a specific subset; it can never re-add an
	// `ai:deny` tool or one the top-level filters already removed.
	Include *ToolFilter `json:"include,omitempty" yaml:"include,omitempty"`
}

// ExposedMCPConfig configures the MCP server.
type ExposedMCPConfig struct {
	// Port is the TCP port the MCP server listens on.
	Port int `json:"port" yaml:"port"`
	// Address is the host or IP the MCP server binds to. It defaults to loopback
	// (127.0.0.1) so the server is not reachable off the host unless an address is
	// set explicitly; use "0.0.0.0" to listen on all interfaces. It combines with
	// Port to form the listen address.
	Address string `json:"address,omitempty" yaml:"address,omitempty"`
	// Instructions is optional free text sent to clients at connection time to
	// describe how to use the server and its tools. Clients may pass it to the
	// LLM as a hint, so it is a place to add orientation that the individual tool
	// descriptions are too terse to carry. When empty nothing is sent.
	Instructions string `json:"instructions,omitempty" yaml:"instructions,omitempty"`
	// ConfirmOverMCP selects how confirm-tagged commands (ai:confirm and the
	// harness confirm_tags) are gated when served over MCP: "auto" (the default)
	// asks clients that support elicitation and runs the command ungated for clients
	// that do not, "always" refuses a confirm-tagged command when the client cannot
	// be asked, and "never" never asks and runs it ungated, delegating approval to
	// the client's own UI.
	ConfirmOverMCP string `json:"confirm_over_mcp,omitempty" yaml:"confirm_over_mcp,omitempty"`
	// Builtins additionally exposes the agent's built-in tools (currently only
	// knowledge_search) over MCP. The agent's wrapped CLI tools are always exposed;
	// the built-ins are off by default and must be listed here because they are
	// otherwise agent-run-only. Only the read-only knowledge_search is exposable;
	// the memory and human_in_the_loop built-ins are never exposable and listing one
	// is a config error. Any client that can reach the port can then query the
	// knowledge base, so this is an explicit, security-relevant opt-in.
	Builtins []string `json:"builtins,omitempty" yaml:"builtins,omitempty"`
}

// RemoteAgent is a remote agent we can talk to using a2a-like behaviors.
type RemoteAgent struct {
	// Name is the remote agent's identity.
	Name string `yaml:"name" json:"name"`
	// Alias is a short local name for the remote agent.
	Alias string `yaml:"alias,omitempty" json:"alias,omitempty"`
}

// RemoteToolHost is a remote agent we pull in all the tools of.
type RemoteToolHost struct {
	// Name is the remote agent's identity.
	Name string `yaml:"name" json:"name"`
	// Alias is a short local name for the remote tool host.
	Alias string `yaml:"alias,omitempty" json:"alias,omitempty"`
	// Exclude filters out tools from this host (same semantics as the top level).
	Exclude *ToolFilter `json:"exclude,omitempty" yaml:"exclude,omitempty"`
	// Include restricts tools from this host (same semantics as the top level).
	Include *ToolFilter `json:"include,omitempty" yaml:"include,omitempty"`
}

// EffectiveAlias is the prefix applied to tools imported from this host: the
// configured Alias when set, otherwise the host's identity. Imported tools are
// named "<alias>_<remote tool name>" so they carry their provenance and stay
// distinct from local tools and from other hosts' tools.
func (h RemoteToolHost) EffectiveAlias() string {
	if h.Alias != "" {
		return h.Alias
	}

	return h.Name
}

// ToolFilter is a generic filter selecting tools by name or tag. It is used at
// several levels: top-level include/exclude, per remote tool host, and when
// exposing tools.
type ToolFilter struct {
	// Tools is an explicit list of tool names, regex matched.
	Tools []string `json:"tools,omitempty" yaml:"tools,omitempty"`
	// Tags matches tools by tag. `ai:deny` is always active and can never be
	// included; "" matches untagged commands.
	Tags []string `json:"tags,omitempty" yaml:"tags,omitempty"`
}

// Mode selects which set of required fields a config is validated against. The
// same file drives both the agent (run) and the MCP server, but each needs a
// different subset of fields.
type Mode int

const (
	// ModeAgent validates a config for running the LLM agent: it needs a prompt
	// and a model in addition to the application.
	ModeAgent Mode = iota
	// ModeMCP validates a config for serving tools over MCP: only the application
	// is needed, since there is no prompt or model in that mode.
	ModeMCP
	// ModeServer validates a config for the a2a server: it serves the application's
	// tools to remote agents over NATS, so it needs the application, a valid
	// identity (it keys the discovery and tool subjects and the queue group), and a
	// NATS context. Like MCP it uses neither a prompt nor a model.
	ModeServer
)

// NewConfig returns a Config with default budgets applied.
func NewConfig() *Config {
	cfg := &Config{}

	cfg.prepare()

	return cfg
}

// ParseConfigFile reads the YAML config at path and parses it for agent mode.
func ParseConfigFile(path string) (*Config, error) {
	return ParseConfigFileForMode(path, ModeAgent)
}

// ParseConfigFileForMode reads the YAML config at path and parses it for the
// given mode.
func ParseConfigFileForMode(path string, mode Mode) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config %q: %w", path, err)
	}

	return ParseConfigForMode(data, mode)
}

// ParseConfig parses the YAML config in data for agent mode.
func ParseConfig(data []byte) (*Config, error) {
	return ParseConfigForMode(data, ModeAgent)
}

// ParseConfigForMode parses the YAML config in data, applies default budgets,
// parses duration strings, and validates the result against the given mode.
func ParseConfigForMode(data []byte, mode Mode) (*Config, error) {
	cfg := &Config{}
	// UseJSONUnmarshaler makes goccy populate json.RawMessage fields (such as a
	// memory backend's options block) with canonical JSON, so a raw sub-block can
	// be decoded later against a typed per-backend schema regardless of whether the
	// config was YAML or, in future, JSON.
	if err := yaml.UnmarshalWithOptions(data, cfg, yaml.DisallowUnknownField(), yaml.UseJSONUnmarshaler()); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	if err := cfg.prepare(); err != nil {
		return nil, err
	}

	if err := ValidateForMode(cfg, mode); err != nil {
		return nil, err
	}

	return cfg, nil
}

// Validate checks that required fields are set for agent mode.
func Validate(cfg *Config) error {
	return ValidateForMode(cfg, ModeAgent)
}

// ValidateForMode checks that the fields required by mode are set. application_path
// is optional for ModeAgent and ModeMCP, which can run on built-in and remote tools
// alone; ModeServer still requires it, since a2a serves only the wrapped
// application's tools and never the built-ins. ModeMCP needs nothing more, since it
// serves tools and uses neither a prompt nor a model. ModeServer needs a valid
// identity and a NATS context but, like MCP, no prompt or model. ModeAgent
// additionally needs a model, and a prompt and identity unless the agent is also
// exposed over MCP.
func ValidateForMode(cfg *Config, mode Mode) error {
	if cfg == nil {
		return fmt.Errorf("config is nil")
	}

	// global_flags names flags on the wrapped application, so it has nothing to
	// attach to without one. GlobalFlags is normalized by prepare, so an empty
	// slice here means no usable flag was configured.
	if len(cfg.GlobalFlags) > 0 && cfg.ApplicationPath == "" {
		return fmt.Errorf("global_flags is set but application_path is not: global flags are the wrapped application's own globals and have nothing to attach to without an application; remove global_flags or set application_path")
	}

	// The identity doubles as the discovery queue group, so when set it must be a
	// legal NATS subject token. It defaults to the application binary's basename,
	// which can carry illegal characters (a dot, a space), so check it in every
	// mode whenever it is non-empty rather than only where it is required.
	if cfg.Identity != "" && !identityPattern.MatchString(cfg.Identity) {
		return fmt.Errorf("identity %q is invalid: it must contain only letters, digits, '-' or '_' (it doubles as a NATS queue group); set an explicit identity if the application binary name contains other characters", cfg.Identity)
	}

	// Remote tool hosts are consulted whenever the agent runs or is inspected, so
	// validate them in every mode rather than only where they are imported.
	if err := validateRemoteToolHosts(cfg.RemoteTools); err != nil {
		return err
	}

	if mode == ModeServer {
		// a2a serves only the wrapped application's tools, never the built-ins, so an
		// application-less a2a server could never have anything to serve.
		if cfg.ApplicationPath == "" {
			return fmt.Errorf("application_path is required for the a2a server: a2a serves the wrapped application's tools and cannot serve the built-in tools")
		}
		if cfg.NatsContext == "" {
			return fmt.Errorf("nats_context is required for the a2a server")
		}
		return nil
	}

	if mode == ModeMCP {
		return nil
	}

	// ModeAgent: a config that imports remote tools must say how to reach NATS.
	if len(cfg.RemoteTools) > 0 && cfg.NatsContext == "" {
		return fmt.Errorf("nats_context is required when remote_tools is set")
	}

	mcpOnly := cfg.Expose != nil && cfg.Expose.Agent != nil && cfg.Expose.Agent.MCP != nil
	if !mcpOnly {
		if cfg.Identity == "" {
			return fmt.Errorf("identity is required unless exposed over MCP")
		}
		if cfg.SystemPrompt == "" {
			return fmt.Errorf("prompt is required unless exposed over MCP")
		}
	}

	if cfg.LLM.Model == "" {
		return fmt.Errorf("llm.model is required")
	}

	return nil
}

// validateRemoteToolHosts checks each remote tool host's identity, alias, and
// filters. The host name keys the NATS subjects, so it must be a legal subject
// token; the alias, when set, prefixes imported tool names, so it must be a legal
// tool-name token. A tag-based exclude is rejected outright: discovery does not
// carry tags (a ToolDescriptor has only a name, description and input schema), so
// an exclude-by-tag could never be honored and would silently leave a tool the
// operator meant to remove imported anyway. An include-by-tag is not an error
// here; the importing command warns and ignores it.
func validateRemoteToolHosts(hosts []RemoteToolHost) error {
	for _, host := range hosts {
		if host.Name == "" {
			return fmt.Errorf("remote_tools host is missing a name")
		}
		if !identityPattern.MatchString(host.Name) {
			return fmt.Errorf("remote_tools host name %q is invalid: it must contain only letters, digits, '-' or '_' (it keys the NATS subjects)", host.Name)
		}
		if host.Alias != "" && !identityPattern.MatchString(host.Alias) {
			return fmt.Errorf("remote_tools host %q has an invalid alias %q: it must contain only letters, digits, '-' or '_' (it prefixes imported tool names)", host.Name, host.Alias)
		}
		if host.Exclude != nil && len(host.Exclude.Tags) > 0 {
			return fmt.Errorf("remote_tools host %q has an exclude.tags filter, which cannot be honored: discovery does not carry tags, so a tool excluded by tag would be imported anyway; exclude by tool name instead", host.Name)
		}
	}

	return nil
}

// HumanInTheLoopEnabled reports whether the built-in human-in-the-loop tools are
// enabled. They are only ever active in agent mode.
func (c *Config) HumanInTheLoopEnabled() bool {
	return c.Harness.HumanInTheLoop != nil && c.Harness.HumanInTheLoop.Enabled
}

// MemoryEnabled reports whether the built-in memory tools are enabled. Like the
// human-in-the-loop tools they are only ever active in agent mode.
func (c *Config) MemoryEnabled() bool {
	return c.Harness.Memory != nil && c.Harness.Memory.Enabled
}

// MemoryIndexEnabled reports whether the list of stored memories should be
// injected into the system prompt at run start. It requires memory to be enabled
// and no_index to be unset.
func (c *Config) MemoryIndexEnabled() bool {
	return c.MemoryEnabled() && !c.Harness.Memory.NoIndex
}

// MemoryBackend returns the configured memory backend, defaulting to "file" when
// memory is enabled but no backend is named. It returns "" when memory is off.
func (c *Config) MemoryBackend() string {
	if !c.MemoryEnabled() {
		return ""
	}
	if c.Harness.Memory.Backend == "" {
		return "file"
	}

	return c.Harness.Memory.Backend
}

// MemoryRawOptions returns the raw backend options block, decoded per backend at
// store construction. It is nil when memory is off or no options are set.
func (c *Config) MemoryRawOptions() json.RawMessage {
	if !c.MemoryEnabled() {
		return nil
	}

	return c.Harness.Memory.Options
}

// SessionBackend returns the configured session store backend, defaulting to
// "file". Unlike MemoryBackend it never returns "": sessions are not a feature
// that can be disabled, so an unset config still resolves to the file backend.
func (c *Config) SessionBackend() string {
	return c.Harness.Sessions.BackendName()
}

// SessionRawOptions returns the raw session backend options block, decoded per
// backend at store construction. It is nil when no options are set.
func (c *Config) SessionRawOptions() json.RawMessage {
	return c.Harness.Sessions.RawOptions()
}

// RAGEnabled reports whether the built-in knowledge_search tool is enabled. Like
// the other harness tools it is only ever active in agent mode. It is the
// block-only gate for the lexical baseline; the vector tier has its own gate.
func (c *Config) RAGEnabled() bool {
	return c.Harness.RAG != nil && c.Harness.RAG.Enabled
}

// RAGVectorEnabled reports whether the opt-in vector tier is on: RAG enabled and
// an embeddings sub-block present. It is the second, independent gate; a lexical
// index needs neither a model nor a server.
func (c *Config) RAGVectorEnabled() bool {
	return c.RAGEnabled() && c.Harness.RAG.Embeddings != nil
}

// ConfirmTags returns the extra confirmation gate tags configured under the
// harness block, additive to the always-on ai:confirm tag. It is nil when none
// are set; prepare normalizes the stored slice (trim, de-duplicate, drop empties).
func (c *Config) ConfirmTags() []string {
	return c.Harness.ConfirmTags
}

// TUIDisabled reports whether the agent config turns off the full-screen terminal
// UI. When true the run uses the line UI even on an interactive terminal, and no
// command-line flag can re-enable it.
func (c *Config) TUIDisabled() bool {
	return c.Harness.NoTUI
}

// BellEnabled reports whether the full-screen UI should ring the terminal bell when a
// run blocks on an operator decision. It is on unless the agent config sets no_bell.
func (c *Config) BellEnabled() bool {
	return !c.Harness.NoBell
}

// PromptCacheEnabled reports whether Anthropic prompt caching should be applied to
// this agent's requests. It is on unless the agent config sets no_prompt_cache, which
// is the escape hatch for a non-Anthropic endpoint whose proxy rejects cache_control.
func (c *Config) PromptCacheEnabled() bool {
	return !c.LLM.NoPromptCache
}

// ThinkingEnabled reports whether the model should be asked to expose its
// reasoning. Off unless explicitly enabled under llm.thinking.
func (c *Config) ThinkingEnabled() bool {
	return c.LLM.Thinking.Enabled
}

// MCPPort returns the MCP server port configured under expose.agent.mcp, or 0 if
// none is set. Callers layer their own default and flag override on top.
func (c *Config) MCPPort() int {
	if c.Expose == nil || c.Expose.Agent == nil || c.Expose.Agent.MCP == nil {
		return 0
	}

	return c.Expose.Agent.MCP.Port
}

// MCPAddress returns the host or IP the MCP server binds to as configured under
// expose.agent.mcp, or "" if none is set. Callers layer their own flag override
// and loopback default on top.
func (c *Config) MCPAddress() string {
	if c.Expose == nil || c.Expose.Agent == nil || c.Expose.Agent.MCP == nil {
		return ""
	}

	return c.Expose.Agent.MCP.Address
}

// MCPInstructions returns the optional instructions configured under
// expose.agent.mcp, or "" if none is set. The MCP server sends them to clients
// at connection time only when non-empty.
func (c *Config) MCPInstructions() string {
	if c.Expose == nil || c.Expose.Agent == nil || c.Expose.Agent.MCP == nil {
		return ""
	}

	return c.Expose.Agent.MCP.Instructions
}

// ConfirmOverMCPMode returns the configured confirm-over-MCP policy from
// expose.agent.mcp, defaulting to auto when no MCP block or value is set. prepare
// normalizes and validates the stored value, so this returns one of the three
// known policies.
func (c *Config) ConfirmOverMCPMode() string {
	if c.Expose == nil || c.Expose.Agent == nil || c.Expose.Agent.MCP == nil || c.Expose.Agent.MCP.ConfirmOverMCP == "" {
		return ConfirmOverMCPAuto
	}

	return c.Expose.Agent.MCP.ConfirmOverMCP
}

// A2AEnabled reports whether this agent opts in to serving its tools to other
// agents over a2a, set under expose.agent.agent_to_agent. Serving is off unless
// explicitly enabled, so a config that says nothing exposes nothing.
func (c *Config) A2AEnabled() bool {
	return c.Expose != nil && c.Expose.Agent != nil && c.Expose.Agent.AgentToAgent
}

// A2ATransportName is the a2a transport binding in use. It is fixed to NATS until
// a second transport lands, at which point it becomes a configurable field; see
// A2ATransport.
const A2ATransportName = "nats"

// A2ATransport returns the name of the a2a transport binding to use, looked up in
// the a2a transport registry. It is fixed to NATS for now; a transport: config
// field is deferred until a second transport exists.
func (c *Config) A2ATransport() string {
	return A2ATransportName
}

// MCPEnabled reports whether this agent opts in to serving its tools over MCP.
// Presence of the expose.agent.mcp block is the switch; it also carries the
// listen port. Like a2a, a config that says nothing exposes nothing.
func (c *Config) MCPEnabled() bool {
	return c.Expose != nil && c.Expose.Agent != nil && c.Expose.Agent.MCP != nil
}

// MCPBuiltins returns the built-in tools opted in to MCP exposure via
// expose.agent.mcp.builtins, normalized and validated by prepare. It is nil when
// none are set.
func (c *Config) MCPBuiltins() []string {
	if c.Expose == nil || c.Expose.Agent == nil || c.Expose.Agent.MCP == nil {
		return nil
	}

	return c.Expose.Agent.MCP.Builtins
}

// MCPExposesKnowledgeSearch reports whether the read-only knowledge_search
// built-in is allowlisted for MCP exposure. It is the gate mcp_command uses to
// decide whether to open the knowledge store and serve the tool.
func (c *Config) MCPExposesKnowledgeSearch() bool {
	return slices.Contains(c.MCPBuiltins(), KnowledgeSearchToolName)
}

// prepare fills in default budgets and parses all duration strings.
func (c *Config) prepare() error {
	if c.Identity == "" {
		if c.ApplicationPath != "" {
			c.Identity = filepath.Base(c.ApplicationPath)
		} else {
			c.Identity = defaultIdentity
		}
	}

	c.Harness.ConfirmTags = normalizeTags(c.Harness.ConfirmTags)
	c.GlobalFlags = normalizeGlobalFlags(c.GlobalFlags)

	if c.Expose != nil && c.Expose.Agent != nil && c.Expose.Agent.MCP != nil {
		mode, err := normalizeConfirmOverMCP(c.Expose.Agent.MCP.ConfirmOverMCP)
		if err != nil {
			return err
		}
		c.Expose.Agent.MCP.ConfirmOverMCP = mode

		builtins, err := c.normalizeMCPBuiltins(c.Expose.Agent.MCP.Builtins)
		if err != nil {
			return err
		}
		c.Expose.Agent.MCP.Builtins = builtins
	}

	if err := c.LLM.Budget.prepare(); err != nil {
		return err
	}

	if c.Harness.RAG != nil && c.Harness.RAG.Embeddings != nil {
		if err := c.Harness.RAG.Embeddings.prepare(); err != nil {
			return err
		}
	}

	return nil
}

// defaultRAGEmbedTimeout is the per-request embeddings timeout applied when
// harness.knowledge.embeddings.timeout is unset.
const defaultRAGEmbedTimeout = 30 * time.Second

// prepare parses the embeddings request timeout, defaulting it when unset. A
// malformed duration fails loudly at parse time rather than on the first embed.
// The base_url and model are validated later at rag.Open, before the agent loop.
func (e *RAGEmbeddingsConfig) prepare() error {
	if e.TimeoutString == "" {
		e.TimeoutParsed = defaultRAGEmbedTimeout
		return nil
	}

	d, err := time.ParseDuration(e.TimeoutString)
	if err != nil {
		return fmt.Errorf("invalid knowledge.embeddings.timeout %q: %w", e.TimeoutString, err)
	}
	if d <= 0 {
		return fmt.Errorf("invalid knowledge.embeddings.timeout %q: must be positive", e.TimeoutString)
	}
	e.TimeoutParsed = d

	return nil
}

// normalizeConfirmOverMCP lower-cases and trims the confirm_over_mcp value,
// defaulting an empty value to auto and rejecting anything that is not one of the
// three known policies, so a typo fails loudly at parse time rather than silently
// selecting a weaker or stronger gate than the operator intended.
func normalizeConfirmOverMCP(v string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", ConfirmOverMCPAuto:
		return ConfirmOverMCPAuto, nil
	case ConfirmOverMCPAlways:
		return ConfirmOverMCPAlways, nil
	case ConfirmOverMCPNever:
		return ConfirmOverMCPNever, nil
	default:
		return "", fmt.Errorf("invalid confirm_over_mcp %q: must be auto, always or never", v)
	}
}

// normalizeMCPBuiltins trims and de-duplicates the expose.agent.mcp.builtins
// allowlist and validates every entry. Only the read-only knowledge_search may be
// exposed over MCP: any other name (a typo, or a real but unexposable built-in
// such as a memory or human_in_the_loop tool) is rejected with a message that
// names the exposable set and why the others are excluded. A non-empty allowlist
// with knowledge disabled is also rejected, since there would be nothing to serve.
func (c *Config) normalizeMCPBuiltins(names []string) ([]string, error) {
	if len(names) == 0 {
		return nil, nil
	}

	seen := make(map[string]bool, len(names))
	out := make([]string, 0, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			continue
		}
		if name != KnowledgeSearchToolName {
			return nil, fmt.Errorf("expose.agent.mcp.builtins: %q cannot be exposed over MCP; the only exposable built-in is the read-only %s (the memory and human_in_the_loop built-ins are never exposable because they need operator state or interaction)", name, KnowledgeSearchToolName)
		}
		seen[name] = true
		out = append(out, name)
	}

	if len(out) > 0 && !c.RAGEnabled() {
		return nil, fmt.Errorf("expose.agent.mcp.builtins lists %s but knowledge is not enabled; add a harness.knowledge block with 'enabled: true' or remove %s from builtins", KnowledgeSearchToolName, KnowledgeSearchToolName)
	}

	return out, nil
}

// AppToolFiltersConfigured reports whether the top-level include or exclude tool
// filters carry any patterns or tags. They only ever narrow the wrapped
// application's tools, so with no application_path they match nothing; callers use
// this to warn rather than silently ignore an operator's filter.
func (c *Config) AppToolFiltersConfigured() bool {
	if c.Include != nil && (len(c.Include.Tools) > 0 || len(c.Include.Tags) > 0) {
		return true
	}
	if c.Exclude != nil && (len(c.Exclude.Tools) > 0 || len(c.Exclude.Tags) > 0) {
		return true
	}

	return false
}

// GlobalFlagNames returns the configured allowlist of application global flag
// names to expose to the model. prepare normalizes the stored slice (trim, strip
// leading dashes, de-duplicate, drop empties). It is nil when none are set.
func (c *Config) GlobalFlagNames() []string {
	return c.GlobalFlags
}

// normalizeGlobalFlags trims each global flag name, strips its leading dashes so
// an operator can write the name as they type it on the command line (--context
// or context), drops empties, and removes duplicates while preserving first-seen
// order.
func normalizeGlobalFlags(names []string) []string {
	if len(names) == 0 {
		return names
	}

	seen := make(map[string]bool, len(names))
	out := make([]string, 0, len(names))
	for _, name := range names {
		name = strings.TrimLeft(strings.TrimSpace(name), "-")
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}

	return out
}

// normalizeTags trims surrounding whitespace from each tag, drops empties, and
// removes duplicates while preserving first-seen order. Trimming matters for
// confirm tags: a trailing space would make a tag silently fail to match a real
// command tag, leaving a command the operator believes is gated able to run
// without confirmation.
func normalizeTags(tags []string) []string {
	if len(tags) == 0 {
		return tags
	}

	seen := make(map[string]bool, len(tags))
	out := make([]string, 0, len(tags))
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "" || seen[tag] {
			continue
		}
		seen[tag] = true
		out = append(out, tag)
	}

	return out
}

// prepare applies LLM budget defaults and parses the call timeout.
func (b *LLMBudget) prepare() error {
	if b.MaxTokens < 0 {
		return fmt.Errorf("invalid llm max_tokens %d: must not be negative", b.MaxTokens)
	}
	if b.MaxIterations < 0 {
		return fmt.Errorf("invalid llm max_iterations %d: must not be negative", b.MaxIterations)
	}

	if b.MaxTokens == 0 {
		b.MaxTokens = defaultLLMMaxTokens
	}
	if b.MaxIterations == 0 {
		b.MaxIterations = defaultLLMMaxIterations
	}

	if b.CallTimeoutString == "" {
		b.CallTimeoutParsed = defaultLLMCallTimeout
		return nil
	}

	d, err := time.ParseDuration(b.CallTimeoutString)
	if err != nil {
		return fmt.Errorf("invalid llm call_timeout %q: %w", b.CallTimeoutString, err)
	}
	b.CallTimeoutParsed = d

	return nil
}
