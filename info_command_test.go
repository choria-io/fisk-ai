//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/choria-io/fisk"
	"github.com/choria-io/ui/columns"
	"github.com/choria-io/ui/table"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/nats-io/jsm.go/natscontext"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/choria-io/fisk-ai/config"
	"github.com/choria-io/fisk-ai/internal/a2a"
	wire "github.com/choria-io/fisk-ai/internal/a2a/wire/v1"
	"github.com/choria-io/fisk-ai/internal/agent"
	"github.com/choria-io/fisk-ai/internal/agenttest"
	"github.com/choria-io/fisk-ai/internal/mcpclient"
	"github.com/choria-io/fisk-ai/internal/toolkit"
)

// mcpInfoServers stands a real mcp.Server up in this process for every server info
// dials, over an in-memory transport pair, so the specs drive genuine protocol traffic
// with no subprocess and no socket.
type mcpInfoServers struct {
	mu    sync.Mutex
	tools map[string][]*mcp.Tool
	fail  map[string]error
	dials int
}

func newMCPInfoServers() *mcpInfoServers {
	return &mcpInfoServers{tools: map[string][]*mcp.Tool{}, fail: map[string]error{}}
}

// dialer builds the mcpclient.Dialer discovery reaches these servers through.
func (f *mcpInfoServers) dialer() mcpclient.Dialer {
	return func(_ context.Context, server config.MCPServer) (mcp.Transport, error) {
		f.mu.Lock()
		f.dials++
		err := f.fail[server.Name]
		tools := f.tools[server.Name]
		f.mu.Unlock()

		if err != nil {
			return nil, err
		}

		clientSide, serverSide := mcp.NewInMemoryTransports()

		srv := mcp.NewServer(&mcp.Implementation{Name: server.Name, Version: "9.9.9"}, nil)
		for _, tool := range tools {
			srv.AddTool(tool, mcpInfoHandler)
		}

		// The server side is connected before the client, as in-memory transports
		// require, and under a context of its own: the caller's carries the connect
		// timeout, which has nothing to say about how long this server lives.
		_, err = srv.Connect(context.Background(), serverSide, nil)
		if err != nil {
			return nil, err
		}

		return clientSide, nil
	}
}

// dialed is how many servers discovery has asked for a transport for.
func (f *mcpInfoServers) dialed() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.dials
}

// mcpInfoTool is a descriptor a fake server advertises. An empty description is what a
// server that described a tool badly sends, which the import skips.
func mcpInfoTool(name string, description string) *mcp.Tool {
	return &mcp.Tool{Name: name, Description: description, InputSchema: json.RawMessage(`{"type":"object"}`)}
}

// mcpInfoHandler answers a call, which no spec here makes: info reads names and
// descriptions and calls nothing.
func mcpInfoHandler(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "handled by " + req.Params.Name}}}, nil
}

// infoApp is an application with a confirm-gated command and a plain one.
func infoApp() *fisk.Application {
	app := fisk.New("app", "an app")
	app.Command("backup", "back a thing up").Tag(toolkit.ConfirmTag)
	app.Command("status", "show the status")

	return app
}

// fakeRemoteClient is an a2a client over a fake transport that answers discovery for
// every host with the given tools, and fails it for the host named dead.
func fakeRemoteClient(dead string, tools ...string) *a2a.Client {
	GinkgoHelper()

	card := wire.AgentCard{Name: "peer", Version: "1.0.0"}
	for _, name := range tools {
		card.Tools = append(card.Tools, wire.ToolDescriptor{
			Name:        name,
			Description: name + " on the peer\n\nTags: impact:ro",
			InputSchema: json.RawMessage(`{"type":"object"}`),
		})
	}

	transport := agenttest.NewFakeTransport(GinkgoTB(), card)
	transport.SetFaults(agenttest.TransportFault{Agent: dead, Err: a2a.ErrNoResponders})

	client, err := a2a.NewClient(transport, "agent")
	Expect(err).ToNot(HaveOccurred())

	return client
}

var _ = Describe("info tools", func() {
	var (
		ctx     context.Context
		cancel  context.CancelFunc
		servers *mcpInfoServers
		cfg     *config.Config
	)

	BeforeEach(func() {
		ctx, cancel = context.WithTimeout(context.Background(), 30*time.Second)
		DeferCleanup(cancel)

		servers = newMCPInfoServers()
		cfg = agenttest.Config(GinkgoTB(), agenttest.NewFakeApp(GinkgoTB(), infoApp()))
	})

	// assemble assembles src as info does, leniently.
	assemble := func(src agent.Sources) *agent.Assembly {
		GinkgoHelper()

		asm, err := agent.Assemble(ctx, cfg, src, agent.SurfaceRun, agent.Lenient)
		Expect(err).ToNot(HaveOccurred())

		return asm
	}

	// sources opens what info opens, with the fake MCP servers behind the dialer.
	sources := func() agent.Sources {
		GinkgoHelper()

		src, release := infoSources(ctx, cfg, servers.dialer())
		DeferCleanup(release)

		return src
	}

	// The table is rendered as Markdown on purpose: String picks Markdown or a
	// box-drawn text table from the environment, and the rows are pinned as
	// pipe-separated cells.
	renderTable := func(asm *agent.Assembly) string {
		GinkgoHelper()

		tbl := table.NewTableWriter("")
		tbl.AddHeaders("Tool", "Source", "Confirm", "Description", "Tags")
		addToolRows(tbl, cfg, asm)

		out, err := tbl.Markdown()
		Expect(err).ToNot(HaveOccurred())

		return string(out)
	}

	renderStatus := func(asm *agent.Assembly) string {
		c := columns.New()
		printRemoteToolStatus(c, cfg, asm.Remote)
		printMCPServerStatus(c, asm.MCP)

		return c.String()
	}

	renderProblems := func(asm *agent.Assembly) string {
		var buf bytes.Buffer
		printSourceProblems(&buf, cfg, asm.Problems)

		return buf.String()
	}

	It("Should list the commands, the built-ins and a live host's tools, with the dead host unavailable", func() {
		cfg.Harness.HumanInTheLoop = &config.HumanInTheLoopConfig{Enabled: true}
		cfg.Harness.Memory = &config.MemoryConfig{Enabled: true}
		cfg.Harness.RAG = &config.RAGConfig{Enabled: true}
		cfg.Harness.Tools = []config.HarnessToolConfig{
			{Name: config.ReadFileToolName, Confirm: true},
			{Name: config.Base64EncodeToolName},
		}
		cfg.NatsContext = "lab"
		cfg.RemoteTools = []config.RemoteToolHost{{Name: "live", Alias: "lv"}, {Name: "dead"}}

		asm := assemble(agent.Sources{Remote: fakeRemoteClient("dead", "forecast", "status")})

		out := renderTable(asm)
		Expect(out).To(ContainSubstring("| backup | local | Yes | back a thing up | ai:confirm |"))
		Expect(out).To(ContainSubstring("| status | local |  | show the status |  |"))
		Expect(out).To(ContainSubstring("| ask_human_confirm | local |  |"))
		Expect(out).To(ContainSubstring("| memory_list | local |  |"))
		Expect(out).To(ContainSubstring("| knowledge_search | local |  |"))
		Expect(out).To(ContainSubstring("| read_file | local | Yes |"))
		Expect(out).To(ContainSubstring("| base64_encode | local |  |"))
		Expect(out).To(ContainSubstring("| forecast | lv |  | forecast on the peer | impact:ro |"))
		Expect(out).To(ContainSubstring("| lv_status | lv |  | status on the peer | impact:ro |"))

		status := renderStatus(asm)
		Expect(status).To(ContainSubstring("Remote tool hosts"))
		Expect(status).To(ContainSubstring(`live (1.0.0): reachable in`))
		Expect(status).To(ContainSubstring(`advertised 2 tool(s), imported 2 as "lv"`))
		Expect(status).To(ContainSubstring(`dead: UNAVAILABLE via context "lab" after`))
		Expect(status).To(ContainSubstring(a2a.ErrNoResponders.Error()))

		Expect(asm.Problems).To(HaveLen(1))
		Expect(asm.Problems[0].Alias).To(Equal("dead"))
		Expect(renderProblems(asm)).To(BeEmpty())
	})

	It("Should warn with the dial error when the NATS context cannot be dialed and still list the local tools", func() {
		GinkgoT().Setenv("XDG_CONFIG_HOME", GinkgoT().TempDir())
		cfg.NatsContext = "nowhere"
		cfg.RemoteTools = []config.RemoteToolHost{{Name: "peer"}}

		src := sources()
		Expect(src.Remote).To(BeNil())
		Expect(src.Unbound).To(HaveLen(1))
		Expect(src.Unbound[0].Source).To(Equal(agent.SourceRemoteTools))

		asm := assemble(src)
		problems := renderProblems(asm)
		Expect(problems).To(HavePrefix(`warning: cannot connect to NATS context "nowhere" to discover remote tools: connecting to NATS context "nowhere": `))
		Expect(problems).To(ContainSubstring(natscontext.ErrNotFound.Error()))
		Expect(renderTable(asm)).To(ContainSubstring("| backup | local | Yes |"))
		Expect(renderStatus(asm)).ToNot(ContainSubstring("Remote tool hosts"))
	})

	It("Should list a live server's tools beside a server that refused the connection", func() {
		servers.fail["down"] = errors.New("the server did not start")
		servers.tools["docs"] = []*mcp.Tool{
			mcpInfoTool("search", "Searches the pages"),
			mcpInfoTool("read", "Reads a page"),
		}
		cfg.MCPClients = []config.MCPServer{
			{Name: "down", Command: "unused"},
			{Name: "docs", Alias: "dx", Command: "npx", Args: []string{"-y", "docs-server"}, Exclude: &config.ToolFilter{Tools: []string{"^read$"}}},
		}

		src := sources()
		Expect(src.MCP).To(HaveLen(1))
		Expect(src.Unbound).To(HaveLen(1))
		Expect(src.Unbound[0].Alias).To(Equal("down"))

		asm := assemble(src)
		Expect(asm.MCP).To(HaveLen(2))

		Expect(renderTable(asm)).To(ContainSubstring("| dx_search | dx |  | Searches the pages |  |"))

		status := renderStatus(asm)
		Expect(status).To(ContainSubstring("MCP clients"))
		Expect(status).To(ContainSubstring("down (stdio unused): UNAVAILABLE: "))
		Expect(status).To(ContainSubstring("the server did not start"))
		Expect(status).To(ContainSubstring("docs (stdio npx -y docs-server): reachable in"))
		Expect(status).To(ContainSubstring(`advertised 2 tool(s), kept 1 after filtering, imported 1 as "dx"`))
		Expect(status).To(ContainSubstring("tools: dx_search"))

		Expect(renderProblems(asm)).To(BeEmpty())
	})

	It("Should show a skipped tool with the reason it was left out", func() {
		servers.tools["docs"] = []*mcp.Tool{
			mcpInfoTool("search", "Searches the pages"),
			mcpInfoTool("read", ""),
		}
		cfg.MCPClients = []config.MCPServer{{Name: "docs", Command: "unused"}}

		out := renderStatus(assemble(sources()))
		Expect(out).To(ContainSubstring("tools: docs_search"))
		Expect(out).To(ContainSubstring(`tool "read" was not imported: tool "read" from mcp server "docs" advertises no description`))
	})

	It("Should print the configured endpoint with its credentials redacted", func() {
		servers.tools["docs"] = []*mcp.Tool{mcpInfoTool("search", "Searches the pages")}
		cfg.MCPClients = []config.MCPServer{{Name: "docs", URL: "https://mcp.example.net/mcp/?apiKey=a-very-secret-token"}}

		out := renderStatus(assemble(sources()))
		Expect(out).To(ContainSubstring("docs (http https://mcp.example.net/mcp/?apiKey=REDACTED)"))
		Expect(out).ToNot(ContainSubstring("a-very-secret-token"))
	})

	It("Should redact an endpoint a stdio bridge carries in an argument", func() {
		servers.tools["docs"] = []*mcp.Tool{mcpInfoTool("search", "Searches the pages")}
		cfg.MCPClients = []config.MCPServer{{
			Name:    "docs",
			Command: "npx",
			Args:    []string{"-y", "mcp-remote", "https://mcp.example.net/sse?key=a-very-secret-token"},
		}}

		out := renderStatus(assemble(sources()))
		Expect(out).To(ContainSubstring("docs (stdio npx -y mcp-remote https://mcp.example.net/sse?key=REDACTED)"))
		Expect(out).ToNot(ContainSubstring("a-very-secret-token"))
	})

	It("Should connect to nothing when no servers are configured", func() {
		src := sources()
		Expect(src.MCP).To(BeEmpty())
		Expect(servers.dialed()).To(Equal(0))

		asm := assemble(src)
		Expect(asm.MCP).To(BeEmpty())
		Expect(renderStatus(asm)).ToNot(ContainSubstring("MCP clients"))
	})
})

var _ = Describe("mcpServerTarget", func() {
	It("Should redact the endpoint a stdio argument carries", func() {
		target := mcpServerTarget(config.MCPServer{
			Name:    "docs",
			Command: "npx",
			Args:    []string{"-y", "mcp-remote", "https://mcp.example.net/sse?key=a-very-secret-token"},
		})

		Expect(target).To(Equal("stdio npx -y mcp-remote https://mcp.example.net/sse?key=REDACTED"))
	})

	It("Should redact the userinfo and the fragment of an endpoint in an argument", func() {
		target := mcpServerTarget(config.MCPServer{
			Name:    "docs",
			Command: "npx",
			Args:    []string{"mcp-remote", "https://operator:hunter2@mcp.example.net/sse#a-very-secret-token"},
		})

		Expect(target).To(Equal("stdio npx mcp-remote https://REDACTED@mcp.example.net/sse#REDACTED"))
	})

	// Most entries carry no url in their arguments at all, and an operator reads the
	// command line to check that it is the one they meant to run, so an ordinary
	// argument has to survive the redaction as it was written.
	It("Should print an ordinary command and its arguments unchanged", func() {
		target := mcpServerTarget(config.MCPServer{
			Name:    "docs",
			Command: "/usr/local/bin/server",
			Args:    []string{"-y", "mcp-remote", "--port=8080", "--config", "/etc/fisk/docs.yaml", ""},
		})

		Expect(target).To(Equal("stdio /usr/local/bin/server -y mcp-remote --port=8080 --config /etc/fisk/docs.yaml"))
	})

	It("Should print the configured endpoint of an http entry redacted", func() {
		target := mcpServerTarget(config.MCPServer{
			Name: "docs",
			URL:  "https://mcp.example.net/mcp/?apiKey=a-very-secret-token",
		})

		Expect(target).To(Equal("http https://mcp.example.net/mcp/?apiKey=REDACTED"))
	})
})

var _ = Describe("toolSearchStatus", func() {
	It("Should report tool search enabled for the default provider", func() {
		cfg := &config.Config{}
		cfg.LLM.Model = "claude-sonnet-5"
		Expect(toolSearchStatus(cfg)).To(Equal("enabled"))
	})

	It("Should report disabled when no_tool_search is set", func() {
		cfg := &config.Config{}
		cfg.LLM.Model = "claude-sonnet-5"
		cfg.LLM.NoToolSearch = true
		Expect(toolSearchStatus(cfg)).To(Equal("disabled"))
	})

	It("Should report unknown for a provider that is not linked into the build", func() {
		cfg := &config.Config{}
		cfg.LLM.Model = "gpt-5"
		cfg.LLM.Provider = "openai"
		Expect(toolSearchStatus(cfg)).To(Equal("unknown"))
	})
})

var _ = Describe("printSessionsSection", func() {
	render := func(cfg *config.Config) string {
		c := columns.New()
		printSessionsSection(c, cfg)

		return c.String()
	}

	It("Should omit the section for an MCP-only config with no model", func() {
		cfg := &config.Config{}
		cfg.Harness.Sessions = &config.SessionConfig{Backend: "jetstream"}
		Expect(render(cfg)).ToNot(ContainSubstring("Sessions"))
	})

	It("Should show the file backend and its configured directory", func() {
		cfg := &config.Config{}
		cfg.LLM.Model = "claude-sonnet-5"
		cfg.Harness.Sessions = config.SessionConfigFromStateDir("/tmp/runs")

		out := render(cfg)
		Expect(out).To(ContainSubstring("Sessions"))
		Expect(out).To(ContainSubstring("file"))
		Expect(out).To(ContainSubstring("/tmp/runs"))
	})

	It("Should show the XDG default when the file backend has no directory", func() {
		cfg := &config.Config{}
		cfg.LLM.Model = "claude-sonnet-5"

		Expect(render(cfg)).To(ContainSubstring("(XDG default)"))
	})

	It("Should show the jetstream stream, context, and the derived-prefix note", func() {
		cfg := &config.Config{}
		cfg.LLM.Model = "claude-sonnet-5"
		cfg.NatsContext = "prod"
		cfg.Harness.Sessions = &config.SessionConfig{
			Backend: "jetstream",
			Options: json.RawMessage(`{"stream":"FISKSESSIONS"}`),
		}

		out := render(cfg)
		Expect(out).To(ContainSubstring("jetstream"))
		Expect(out).To(ContainSubstring("FISKSESSIONS"))
		Expect(out).To(ContainSubstring("prod"))
		Expect(out).To(ContainSubstring("derived from the stream"))
	})

	It("Should show (default) as the jetstream context when none is configured", func() {
		cfg := &config.Config{}
		cfg.LLM.Model = "claude-sonnet-5"
		cfg.Harness.Sessions = &config.SessionConfig{
			Backend: "jetstream",
			Options: json.RawMessage(`{"stream":"FISKSESSIONS"}`),
		}

		Expect(render(cfg)).To(ContainSubstring("(default)"))
	})
})

var _ = Describe("printSamplePromptsSection", func() {
	render := func(cfg *config.Config) string {
		c := columns.New()
		printSamplePromptsSection(c, cfg)

		return c.String()
	}

	It("Should list every prompt the operator published on the card", func() {
		cfg := &config.Config{Prompts: []string{"who can publish to ORDERS?", "what changed in the auth config today?"}}

		out := render(cfg)
		Expect(out).To(ContainSubstring("Sample prompts"))
		Expect(out).To(ContainSubstring("who can publish to ORDERS?"))
		Expect(out).To(ContainSubstring("what changed in the auth config today?"))
	})

	It("Should omit the section for a configuration that names none", func() {
		Expect(render(&config.Config{})).ToNot(ContainSubstring("Sample prompts"))
	})
})
