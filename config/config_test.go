// Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestConfig(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Config")
}

var _ = Describe("Config", func() {
	Describe("NewConfig", func() {
		It("Should apply LLM budget defaults", func() {
			cfg := NewConfig()
			Expect(cfg).NotTo(BeNil())
			Expect(cfg.LLM.Budget.MaxTokens).To(Equal(int64(defaultLLMMaxTokens)))
			Expect(cfg.LLM.Budget.MaxIterations).To(Equal(int64(defaultLLMMaxIterations)))
			Expect(cfg.LLM.Budget.CallTimeoutParsed).To(Equal(defaultLLMCallTimeout))
		})
	})

	Describe("ParseConfigFile", func() {
		It("Should return an error when the file does not exist", func() {
			cfg, err := ParseConfigFile(filepath.Join(GinkgoT().TempDir(), "missing.yaml"))
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("reading config"))
			Expect(cfg).To(BeNil())
		})

		It("Should read and parse a config from disk", func() {
			path := filepath.Join(GinkgoT().TempDir(), "config.yaml")
			Expect(os.WriteFile(path, []byte(`
identity: agent1
application_path: /usr/bin/nats
system_prompt: do the thing
llm:
  model: claude-sonnet-4-6
`), 0o600)).To(Succeed())

			cfg, err := ParseConfigFile(path)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.Identity).To(Equal("agent1"))
		})
	})

	Describe("ParseConfig", func() {
		It("Should return an error for invalid YAML", func() {
			cfg, err := ParseConfig([]byte("identity: [unterminated"))
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("parsing config"))
			Expect(cfg).To(BeNil())
		})

		It("Should parse a minimal config and apply defaults", func() {
			cfg, err := ParseConfig([]byte(`
identity: agent1
application_path: /usr/bin/nats
system_prompt: do the thing
llm:
  model: claude-sonnet-4-6
`))
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.Identity).To(Equal("agent1"))
			Expect(cfg.ApplicationPath).To(Equal("/usr/bin/nats"))
			Expect(cfg.SystemPrompt).To(Equal("do the thing"))
			Expect(cfg.LLM.Model).To(Equal("claude-sonnet-4-6"))

			Expect(cfg.LLM.Budget.MaxTokens).To(Equal(int64(defaultLLMMaxTokens)))
			Expect(cfg.LLM.Budget.MaxIterations).To(Equal(int64(defaultLLMMaxIterations)))
			Expect(cfg.LLM.Budget.CallTimeoutParsed).To(Equal(defaultLLMCallTimeout))
		})

		It("Should parse all fields and durations", func() {
			cfg, err := ParseConfig([]byte(`
identity: agent1
application_path: /usr/bin/nats
system_prompt: do the thing
llm:
  model: claude-sonnet-4-6
  budget:
    max_tokens: 1000
    max_iterations: 5
    call_timeout: 90s
exclude:
  tools:
    - "nats auth.*"
  tags:
    - admin
include:
  tags:
    - ""
nats_context: ngs
remote_agents:
  - name: remote1
    alias: r1
remote_tools:
  - name: host1
    alias: h1
    exclude:
      tools:
        - secret
expose:
  agent:
    agent_to_agent: true
    mcp:
      port: 8080
    tools:
      include:
        tags:
          - public
`))
			Expect(err).NotTo(HaveOccurred())

			Expect(cfg.LLM.Budget.MaxTokens).To(Equal(int64(1000)))
			Expect(cfg.LLM.Budget.MaxIterations).To(Equal(int64(5)))
			Expect(cfg.LLM.Budget.CallTimeoutParsed).To(Equal(90 * time.Second))

			Expect(cfg.Exclude.Tools).To(Equal([]string{"nats auth.*"}))
			Expect(cfg.Exclude.Tags).To(Equal([]string{"admin"}))

			Expect(cfg.Include.Tags).To(Equal([]string{""}))

			Expect(cfg.RemoteAgents).To(HaveLen(1))
			Expect(cfg.RemoteAgents[0].Name).To(Equal("remote1"))
			Expect(cfg.RemoteAgents[0].Alias).To(Equal("r1"))

			Expect(cfg.RemoteTools).To(HaveLen(1))
			Expect(cfg.NatsContext).To(Equal("ngs"))
			Expect(cfg.RemoteTools[0].Name).To(Equal("host1"))
			Expect(cfg.RemoteTools[0].Exclude.Tools).To(Equal([]string{"secret"}))

			Expect(cfg.Expose.Agent.AgentToAgent).To(BeTrue())
			Expect(cfg.Expose.Agent.MCP.Port).To(Equal(8080))
			Expect(cfg.Expose.Agent.Tools.Include.Tags).To(Equal([]string{"public"}))
		})

		It("Should reject unknown keys, including harness settings left at the top level", func() {
			_, err := ParseConfig([]byte(`
identity: agent1
application_path: /usr/bin/nats
system_prompt: do the thing
human_in_the_loop:
  enabled: true
llm:
  model: claude-sonnet-4-6
`))
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("human_in_the_loop"))

			_, err = ParseConfig([]byte(`
identity: agent1
application_path: /usr/bin/nats
system_prompt: do the thing
no_such_key: true
llm:
  model: claude-sonnet-4-6
`))
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("no_such_key"))
		})

		It("Should normalize confirm_tags by trimming, dropping empties and deduping", func() {
			cfg, err := ParseConfig([]byte(`
identity: agent1
application_path: /usr/bin/nats
system_prompt: do the thing
llm:
  model: claude-sonnet-4-6
harness:
  confirm_tags:
    - "impact:rw"
    - " impact:rw "
    - ""
    - "   "
    - "admin"
`))
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.ConfirmTags()).To(Equal([]string{"impact:rw", "admin"}))
		})

		It("Should normalize global_flags, stripping dashes and de-duplicating", func() {
			cfg, err := ParseConfig([]byte(`
identity: agent1
application_path: /usr/bin/nats
system_prompt: do the thing
llm:
  model: claude-sonnet-4-6
global_flags:
  - "--context"
  - "context"
  - " server "
  - ""
`))
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.GlobalFlagNames()).To(Equal([]string{"context", "server"}))
		})

		It("Should normalize confirm_over_mcp case and whitespace", func() {
			cfg, err := ParseConfig([]byte(`
identity: agent1
application_path: /usr/bin/nats
system_prompt: do the thing
llm:
  model: claude-sonnet-4-6
expose:
  agent:
    mcp:
      port: 8080
      confirm_over_mcp: "  Always  "
`))
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.ConfirmOverMCPMode()).To(Equal("always"))
		})

		It("Should default confirm_over_mcp to auto when unset", func() {
			cfg, err := ParseConfig([]byte(`
identity: agent1
application_path: /usr/bin/nats
system_prompt: do the thing
llm:
  model: claude-sonnet-4-6
expose:
  agent:
    mcp:
      port: 8080
`))
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.ConfirmOverMCPMode()).To(Equal("auto"))
		})

		It("Should reject an invalid confirm_over_mcp value", func() {
			_, err := ParseConfig([]byte(`
identity: agent1
application_path: /usr/bin/nats
system_prompt: do the thing
llm:
  model: claude-sonnet-4-6
expose:
  agent:
    mcp:
      port: 8080
      confirm_over_mcp: sometimes
`))
			Expect(err).To(MatchError(ContainSubstring("invalid confirm_over_mcp")))
		})

		It("Should return an error for an invalid llm call_timeout", func() {
			cfg, err := ParseConfig([]byte(`
identity: agent1
application_path: /usr/bin/nats
system_prompt: do the thing
llm:
  model: claude-sonnet-4-6
  budget:
    call_timeout: not-a-duration
`))
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("invalid llm call_timeout"))
			Expect(cfg).To(BeNil())
		})

		It("Should derive identity from the application_path base name when unset", func() {
			cfg, err := ParseConfig([]byte(`
application_path: /usr/bin/nats
system_prompt: do the thing
llm:
  model: claude-sonnet-4-6
`))
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.Identity).To(Equal("nats"))
		})

		It("Should keep an explicit identity over the derived one", func() {
			cfg, err := ParseConfig([]byte(`
identity: agent1
application_path: /usr/bin/nats
system_prompt: do the thing
llm:
  model: claude-sonnet-4-6
`))
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.Identity).To(Equal("agent1"))
		})

		It("Should reject a derived identity whose basename carries illegal characters", func() {
			_, err := ParseConfig([]byte(`
application_path: /usr/bin/my.agent
system_prompt: do the thing
llm:
  model: claude-sonnet-4-6
`))
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("is invalid"))
		})

		It("Should accept an agent config without application_path and default the identity", func() {
			cfg, err := ParseConfig([]byte(`
system_prompt: do the thing
llm:
  model: claude-sonnet-4-6
`))
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.ApplicationPath).To(BeEmpty())
			Expect(cfg.Identity).To(Equal("fisk-ai"))
		})

		It("Should keep an explicit identity when application_path is unset", func() {
			cfg, err := ParseConfig([]byte(`
identity: agent1
system_prompt: do the thing
llm:
  model: claude-sonnet-4-6
`))
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.Identity).To(Equal("agent1"))
		})

		It("Should return a validation error when llm.model is missing", func() {
			cfg, err := ParseConfig([]byte(`
identity: agent1
system_prompt: do the thing
`))
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("llm.model is required"))
			Expect(cfg).To(BeNil())
		})

		It("Should reject global_flags without an application_path", func() {
			cfg, err := ParseConfig([]byte(`
system_prompt: do the thing
llm:
  model: claude-sonnet-4-6
global_flags:
  - context
`))
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("global_flags is set but application_path is not"))
			Expect(cfg).To(BeNil())
		})

		It("Should require application_path for the a2a server mode", func() {
			cfg, err := ParseConfigForMode([]byte(`
identity: agent1
nats_context: ctx
expose:
  agent:
    agent_to_agent: true
`), ModeServer)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("application_path is required for the a2a server"))
			Expect(cfg).To(BeNil())
		})

		It("Should parse human_in_the_loop and report it enabled", func() {
			cfg, err := ParseConfig([]byte(`
identity: agent1
application_path: /usr/bin/nats
system_prompt: do the thing
harness:
  human_in_the_loop:
    enabled: true
llm:
  model: claude-sonnet-4-6
`))
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.HumanInTheLoopEnabled()).To(BeTrue())
		})

		It("Should report human_in_the_loop disabled when absent or set to false", func() {
			cfg, err := ParseConfig([]byte(`
identity: agent1
application_path: /usr/bin/nats
system_prompt: do the thing
llm:
  model: claude-sonnet-4-6
`))
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.HumanInTheLoopEnabled()).To(BeFalse())

			cfg, err = ParseConfig([]byte(`
identity: agent1
application_path: /usr/bin/nats
system_prompt: do the thing
harness:
  human_in_the_loop:
    enabled: false
llm:
  model: claude-sonnet-4-6
`))
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.HumanInTheLoopEnabled()).To(BeFalse())
		})
	})

	Describe("TUIDisabled", func() {
		It("Should report the TUI disabled when no_tui is true", func() {
			cfg, err := ParseConfig([]byte(`
identity: agent1
application_path: /usr/bin/nats
system_prompt: do the thing
harness:
  no_tui: true
llm:
  model: claude-sonnet-4-6
`))
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.TUIDisabled()).To(BeTrue())
		})

		It("Should report the TUI enabled when no_tui is absent or false", func() {
			cfg, err := ParseConfig([]byte(`
identity: agent1
application_path: /usr/bin/nats
system_prompt: do the thing
llm:
  model: claude-sonnet-4-6
`))
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.TUIDisabled()).To(BeFalse())
		})
	})

	Describe("BellEnabled", func() {
		It("Should ring the bell by default when no_bell is absent", func() {
			cfg, err := ParseConfig([]byte(`
identity: agent1
application_path: /usr/bin/nats
system_prompt: do the thing
llm:
  model: claude-sonnet-4-6
`))
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.BellEnabled()).To(BeTrue())
		})

		It("Should silence the bell when no_bell is true", func() {
			cfg, err := ParseConfig([]byte(`
identity: agent1
application_path: /usr/bin/nats
system_prompt: do the thing
harness:
  no_bell: true
llm:
  model: claude-sonnet-4-6
`))
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.BellEnabled()).To(BeFalse())
		})
	})

	Describe("Expose helpers", func() {
		It("Should report a2a enabled only when expose.agent.agent_to_agent is true", func() {
			cfg, err := ParseConfigForMode([]byte(`
identity: agent1
application_path: /usr/bin/nats
nats_context: ngs
expose:
  agent:
    agent_to_agent: true
`), ModeServer)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.A2AEnabled()).To(BeTrue())
			Expect(cfg.MCPEnabled()).To(BeFalse())
		})

		It("Should report a2a disabled when the expose block is absent or the flag is false", func() {
			cfg, err := ParseConfigForMode([]byte(`
application_path: /usr/bin/nats
`), ModeMCP)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.A2AEnabled()).To(BeFalse())

			cfg, err = ParseConfigForMode([]byte(`
application_path: /usr/bin/nats
expose:
  agent:
    agent_to_agent: false
`), ModeMCP)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.A2AEnabled()).To(BeFalse())
		})

		It("Should report MCP enabled when the expose.agent.mcp block is present", func() {
			cfg, err := ParseConfigForMode([]byte(`
application_path: /usr/bin/nats
expose:
  agent:
    mcp:
      port: 9000
`), ModeMCP)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.MCPEnabled()).To(BeTrue())
			Expect(cfg.MCPPort()).To(Equal(9000))
			Expect(cfg.A2AEnabled()).To(BeFalse())
		})

		It("Should report MCP enabled for a portless mcp block, leaving the default to the caller", func() {
			cfg, err := ParseConfigForMode([]byte(`
application_path: /usr/bin/nats
expose:
  agent:
    mcp: {}
`), ModeMCP)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.MCPEnabled()).To(BeTrue())
			Expect(cfg.MCPPort()).To(Equal(0))
		})

		It("Should report the configured MCP bind address, defaulting to empty", func() {
			cfg, err := ParseConfigForMode([]byte(`
application_path: /usr/bin/nats
expose:
  agent:
    mcp:
      port: 9000
      address: 127.0.0.1
`), ModeMCP)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.MCPAddress()).To(Equal("127.0.0.1"))

			cfg, err = ParseConfigForMode([]byte(`
application_path: /usr/bin/nats
expose:
  agent:
    mcp:
      port: 9000
`), ModeMCP)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.MCPAddress()).To(Equal(""))
		})

		It("Should report MCP disabled when the expose block is absent", func() {
			cfg, err := ParseConfigForMode([]byte(`
application_path: /usr/bin/nats
`), ModeMCP)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.MCPEnabled()).To(BeFalse())
		})
	})

	Describe("Validate", func() {
		var cfg *Config

		BeforeEach(func() {
			cfg = &Config{
				Identity:        "agent1",
				ApplicationPath: "/usr/bin/nats",
				SystemPrompt:    "do the thing",
				LLM:             LLMConfig{Model: "claude-sonnet-4-6"},
			}
		})

		It("Should pass with a complete config", func() {
			Expect(Validate(cfg)).To(Succeed())
		})

		It("Should fail when the config is nil", func() {
			err := Validate(nil)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("config is nil"))
		})

		It("Should pass in agent mode when application_path is missing", func() {
			cfg.ApplicationPath = ""
			Expect(Validate(cfg)).To(Succeed())
		})

		It("Should fail in a2a server mode when application_path is missing", func() {
			cfg.ApplicationPath = ""
			cfg.NatsContext = "ctx"
			err := ValidateForMode(cfg, ModeServer)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("application_path is required for the a2a server"))
		})

		It("Should fail when global_flags is set without application_path", func() {
			cfg.ApplicationPath = ""
			cfg.GlobalFlags = []string{"context"}
			err := Validate(cfg)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("global_flags is set but application_path is not"))
		})

		It("Should fail when identity is missing and not exposed over MCP", func() {
			cfg.Identity = ""
			err := Validate(cfg)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("identity is required unless exposed over MCP"))
		})

		It("Should fail when prompt is missing and not exposed over MCP", func() {
			cfg.SystemPrompt = ""
			err := Validate(cfg)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("prompt is required unless exposed over MCP"))
		})

		It("Should accept an identity of letters, digits, '-' and '_'", func() {
			cfg.Identity = "agent_1-prod"
			Expect(Validate(cfg)).To(Succeed())
		})

		It("Should reject an identity with characters illegal in a NATS queue group", func() {
			for _, bad := range []string{"agent 1", "agent.1", "agent*", "agent>", "ag/ent"} {
				cfg.Identity = bad
				err := Validate(cfg)
				Expect(err).To(HaveOccurred(), "identity %q should be rejected", bad)
				Expect(err.Error()).To(ContainSubstring("is invalid"))
			}
		})

		It("Should reject an illegal identity even when exposed over MCP", func() {
			cfg.Identity = "agent.1"
			err := ValidateForMode(cfg, ModeMCP)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("is invalid"))
		})

		It("Should fail when llm.model is missing", func() {
			cfg.LLM.Model = ""
			err := Validate(cfg)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("llm.model is required"))
		})

		It("Should not require identity or prompt when exposed over MCP", func() {
			cfg.Identity = ""
			cfg.SystemPrompt = ""
			cfg.Expose = &ExposeConfig{Agent: &AgentExpose{MCP: &ExposedMCPConfig{Port: 8080}}}
			Expect(Validate(cfg)).To(Succeed())
		})

		It("Should still require identity and prompt when exposed only as an agent without MCP", func() {
			cfg.Identity = ""
			cfg.Expose = &ExposeConfig{Agent: &AgentExpose{AgentToAgent: true}}
			err := Validate(cfg)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("identity is required unless exposed over MCP"))
		})
	})

	Describe("LLMBudget prepare", func() {
		It("Should keep explicit values and parse the call timeout", func() {
			b := &LLMBudget{
				MaxTokens:         10,
				MaxIterations:     30,
				CallTimeoutString: "45s",
			}
			Expect(b.prepare()).To(Succeed())
			Expect(b.MaxTokens).To(Equal(int64(10)))
			Expect(b.MaxIterations).To(Equal(int64(30)))
			Expect(b.CallTimeoutParsed).To(Equal(45 * time.Second))
		})

		It("Should apply defaults when values are unset", func() {
			b := &LLMBudget{}
			Expect(b.prepare()).To(Succeed())
			Expect(b.MaxTokens).To(Equal(int64(defaultLLMMaxTokens)))
			Expect(b.MaxIterations).To(Equal(int64(defaultLLMMaxIterations)))
			Expect(b.CallTimeoutParsed).To(Equal(defaultLLMCallTimeout))
		})

		It("Should error on an invalid call timeout", func() {
			b := &LLMBudget{CallTimeoutString: "soon"}
			err := b.prepare()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("invalid llm call_timeout"))
		})

		It("Should error on a negative max_tokens", func() {
			b := &LLMBudget{MaxTokens: -1}
			err := b.prepare()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("invalid llm max_tokens"))
		})

		It("Should error on a negative max_iterations", func() {
			b := &LLMBudget{MaxIterations: -1}
			err := b.prepare()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("invalid llm max_iterations"))
		})
	})

	Describe("Memory", func() {
		parseWithMemory := func(memory string) (*Config, error) {
			return ParseConfig([]byte(`
identity: agent1
application_path: /usr/bin/nats
system_prompt: do the thing
llm:
  model: claude-sonnet-4-6
harness:
  memory:
` + memory))
		}

		It("Should leave memory off when the block is absent", func() {
			cfg, err := parseWithMemory("    enabled: false")
			Expect(err).ToNot(HaveOccurred())
			Expect(cfg.MemoryEnabled()).To(BeFalse())
			Expect(cfg.MemoryBackend()).To(BeEmpty())
			Expect(cfg.MemoryRawOptions()).To(BeNil())
		})

		It("Should default the backend to file and enable the index", func() {
			cfg, err := parseWithMemory("    enabled: true")
			Expect(err).ToNot(HaveOccurred())
			Expect(cfg.MemoryEnabled()).To(BeTrue())
			Expect(cfg.MemoryBackend()).To(Equal("file"))
			Expect(cfg.MemoryIndexEnabled()).To(BeTrue())
		})

		It("Should honor no_index as an opt-out", func() {
			cfg, err := parseWithMemory("    enabled: true\n    no_index: true")
			Expect(err).ToNot(HaveOccurred())
			Expect(cfg.MemoryEnabled()).To(BeTrue())
			Expect(cfg.MemoryIndexEnabled()).To(BeFalse())
		})

		It("Should capture the options block as canonical JSON for a per-backend decode", func() {
			cfg, err := parseWithMemory("    enabled: true\n    options:\n      directory: /tmp/mem")
			Expect(err).ToNot(HaveOccurred())
			Expect(string(cfg.MemoryRawOptions())).To(MatchJSON(`{"directory":"/tmp/mem"}`))
		})

		It("Should reject an unknown key inside the memory block", func() {
			_, err := parseWithMemory("    enabled: true\n    bogus: 1")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("bogus"))
		})
	})
})
