//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/choria-io/fisk"
	tools2 "github.com/choria-io/fisk-ai/internal/toolkit"
	fisk2 "github.com/choria-io/fisk-ai/internal/toolkit/fisk"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/choria-io/fisk-ai/config"
)

func TestMCPServer(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Internal/MCPServer")
}

// introspect drives an application's real --fisk-introspect handler in-process
// and returns the parsed model, whose per-command schemas are populated.
func introspect(app *fisk.Application) *fisk.ApplicationModel {
	GinkgoHelper()

	app.Terminate(func(int) {})

	r, w, err := os.Pipe()
	Expect(err).NotTo(HaveOccurred())

	stdout := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = stdout }()

	captured := make(chan []byte, 1)
	go func() {
		data, _ := io.ReadAll(r)
		captured <- data
	}()

	_, err = app.Parse([]string{"--fisk-introspect"})
	Expect(err).NotTo(HaveOccurred())
	Expect(w.Close()).To(Succeed())

	var model fisk.ApplicationModel
	Expect(json.Unmarshal(<-captured, &model)).To(Succeed())

	return &model
}

// toolsFor introspects app and returns its tools.
func toolsFor(app *fisk.Application) []*fisk2.FiskCommandTool {
	GinkgoHelper()

	tools, err := fisk2.ApplicationTools(introspect(app))
	Expect(err).NotTo(HaveOccurred())

	return tools
}

// writeExecutable writes body to an executable file and returns its path, for
// use as a stand-in application binary.
func writeExecutable(body string) string {
	GinkgoHelper()

	path := filepath.Join(GinkgoT().TempDir(), "app")
	Expect(os.WriteFile(path, []byte(body), 0o700)).To(Succeed())

	return path
}

// connect wires an in-memory MCP client to the server and returns the session.
func connect(ctx context.Context, srv *mcp.Server) *mcp.ClientSession {
	GinkgoHelper()

	serverT, clientT := mcp.NewInMemoryTransports()
	_, err := srv.Connect(ctx, serverT, nil)
	Expect(err).NotTo(HaveOccurred())

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "v1"}, nil)
	cs, err := client.Connect(ctx, clientT, nil)
	Expect(err).NotTo(HaveOccurred())

	return cs
}

// callText runs a tool and returns its first text content plus the error flag.
func callText(ctx context.Context, cs *mcp.ClientSession, name string, args map[string]any) (string, bool) {
	GinkgoHelper()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	Expect(err).NotTo(HaveOccurred())
	Expect(res.Content).NotTo(BeEmpty())

	text, ok := res.Content[0].(*mcp.TextContent)
	Expect(ok).To(BeTrue())

	return text.Text, res.IsError
}

// connectElicit connects a client that answers elicitation with handler, so the
// server sees a client that negotiated the elicitation capability (the SDK
// advertises it automatically when a handler is set).
func connectElicit(ctx context.Context, srv *mcp.Server, handler func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error)) *mcp.ClientSession {
	GinkgoHelper()

	serverT, clientT := mcp.NewInMemoryTransports()
	_, err := srv.Connect(ctx, serverT, nil)
	Expect(err).NotTo(HaveOccurred())

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "v1"}, &mcp.ClientOptions{ElicitationHandler: handler})
	cs, err := client.Connect(ctx, clientT, nil)
	Expect(err).NotTo(HaveOccurred())

	return cs
}

// taggedExecutable builds one command carrying tag, backed by an executable that
// prints marker, ready to serve.
func taggedExecutable(name, tag, marker string) []*fisk2.FiskCommandTool {
	GinkgoHelper()

	app := fisk.New("app", "an app")
	app.Command(name, "a command").Tag(tag)

	tools := toolsFor(app)
	Expect(tools).To(HaveLen(1))
	tools[0].AppPath = writeExecutable("#!/bin/sh\necho " + marker + "\n")

	return tools
}

// safeBuffer is an io.Writer safe for the concurrent writes a running server makes
// from its handler and initialized-notification goroutines.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.String()
}

var _ = Describe("BuildServer", func() {
	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

	It("Should expose each tool with its raw, unannotated schema", func() {
		app := fisk.New("app", "an app")
		deploy := app.Command("deploy", "deploy things")
		deploy.Arg("target", "where to deploy").Required().String()
		deploy.Flag("force", "force the deploy").Bool()

		srv, registered := BuildServer(toolsFor(app), Options{Name: "app", Version: "v1", LogOutput: io.Discard})
		Expect(registered).To(ConsistOf("deploy"))

		cs := connect(ctx, srv)
		defer cs.Close()

		res, err := cs.ListTools(ctx, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Tools).To(HaveLen(1))

		tool := res.Tools[0]
		Expect(tool.Name).To(Equal("deploy"))
		Expect(tool.Description).To(Equal("deploy things"))

		schema, ok := tool.InputSchema.(map[string]any)
		Expect(ok).To(BeTrue())
		Expect(schema["required"]).To(ConsistOf("target"))

		props, ok := schema["properties"].(map[string]any)
		Expect(ok).To(BeTrue())

		// The optional flag keeps its description verbatim: unlike the Anthropic
		// path, no "(optional)" suffix is added for MCP clients.
		force, ok := props["force"].(map[string]any)
		Expect(ok).To(BeTrue())
		Expect(force["description"]).To(Equal("force the deploy"))
	})

	It("Should send configured instructions to the connecting client", func() {
		app := fisk.New("app", "an app")
		app.Command("deploy", "deploy things")

		srv, _ := BuildServer(toolsFor(app), Options{Name: "app", Version: "v1", Instructions: "use deploy carefully", LogOutput: io.Discard})

		cs := connect(ctx, srv)
		defer cs.Close()

		Expect(cs.InitializeResult().Instructions).To(Equal("use deploy carefully"))
	})

	It("Should send no instructions when none are configured", func() {
		app := fisk.New("app", "an app")
		app.Command("deploy", "deploy things")

		srv, _ := BuildServer(toolsFor(app), Options{Name: "app", Version: "v1", LogOutput: io.Discard})

		cs := connect(ctx, srv)
		defer cs.Close()

		Expect(cs.InitializeResult().Instructions).To(BeEmpty())
	})

	It("Should append the command's tags to the tool description for the connecting model", func() {
		app := fisk.New("app", "an app")
		app.Command("deploy", "deploy things").Tag("impact:rw")

		srv, _ := BuildServer(toolsFor(app), Options{Name: "app", Version: "v1", LogOutput: io.Discard})

		cs := connect(ctx, srv)
		defer cs.Close()

		res, err := cs.ListTools(ctx, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Tools).To(HaveLen(1))
		Expect(res.Tools[0].Description).To(Equal("deploy things\n\nTags: impact:rw"))
	})

	It("Should give a tool a space-joined command path as its annotation title", func() {
		app := fisk.New("app", "an app")
		stream := app.Command("stream", "stream commands")
		stream.Command("info", "show stream info")

		srv, _ := BuildServer(toolsFor(app), Options{Name: "app", Version: "v1", LogOutput: io.Discard})

		cs := connect(ctx, srv)
		defer cs.Close()

		res, err := cs.ListTools(ctx, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Tools).To(HaveLen(1))
		Expect(res.Tools[0].Name).To(Equal("stream_info"))
		Expect(res.Tools[0].Annotations).NotTo(BeNil())
		Expect(res.Tools[0].Annotations.Title).To(Equal("stream info"))
	})

	It("Should not expose tools removed by the ai:deny filter", func() {
		app := fisk.New("app", "an app")
		app.Command("keep", "kept tool")
		app.Command("secret", "denied tool").Tag("ai:deny")

		// The deny strip is the same first FilterTools pass the agent uses.
		filtered, err := fisk2.FilterTools(toolsFor(app), nil, fisk2.IncludeFilter)
		Expect(err).NotTo(HaveOccurred())

		srv, registered := BuildServer(filtered, Options{Name: "app", Version: "v1", LogOutput: io.Discard})
		Expect(registered).To(ConsistOf("keep"))

		cs := connect(ctx, srv)
		defer cs.Close()

		res, err := cs.ListTools(ctx, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Tools).To(HaveLen(1))
		Expect(res.Tools[0].Name).To(Equal("keep"))
	})

	It("Should expose confirm-tagged tools, leaving the gate to the calling client", func() {
		app := fisk.New("app", "an app")
		app.Command("keep", "kept tool")
		app.Command("gated", "needs confirmation").Tag("ai:confirm")
		app.Command("risky", "mutates state").Tag("impact:rw")

		_, registered := BuildServer(toolsFor(app), Options{Name: "app", Version: "v1", LogOutput: io.Discard})
		Expect(registered).To(ConsistOf("keep", "gated", "risky"))
	})

	It("Should skip tools whose names are not valid MCP tool names", func() {
		valid := &fisk2.FiskCommandTool{Path: []string{"ok"}, Model: &fisk.CmdModel{RestrictedSchema: map[string]any{"type": "object"}}}
		invalid := &fisk2.FiskCommandTool{Path: []string{"bad.name"}, Model: &fisk.CmdModel{RestrictedSchema: map[string]any{"type": "object"}}}

		_, registered := BuildServer([]*fisk2.FiskCommandTool{valid, invalid}, Options{Name: "app", Version: "v1", LogOutput: io.Discard})
		Expect(registered).To(ConsistOf("ok"))
	})
})

var _ = Describe("tool calls", func() {
	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

	// appWithExecutables builds an app whose named commands are each bound to a
	// stand-in executable body, returning tools ready to call.
	appWithExecutables := func(bodies map[string]string) []*fisk2.FiskCommandTool {
		GinkgoHelper()

		app := fisk.New("app", "an app")
		for name := range bodies {
			app.Command(name, "a command")
		}

		tools := toolsFor(app)
		byName := map[string]*fisk2.FiskCommandTool{}
		for _, t := range tools {
			byName[t.Name()] = t
		}
		for name, body := range bodies {
			Expect(byName[name]).NotTo(BeNil())
			byName[name].AppPath = writeExecutable(body)
		}

		return tools
	}

	It("Should return command output as a successful result", func() {
		tools := appWithExecutables(map[string]string{"ping": "#!/bin/sh\necho pong\n"})
		srv, _ := BuildServer(tools, Options{Name: "app", Version: "v1", LogOutput: io.Discard})

		cs := connect(ctx, srv)
		defer cs.Close()

		text, isError := callText(ctx, cs, "ping", nil)
		Expect(isError).To(BeFalse())

		var result tools2.CommandResult
		Expect(json.Unmarshal([]byte(text), &result)).To(Succeed())
		Expect(result.ExitCode).To(Equal(0))
		Expect(result.Output).To(Equal("pong\n"))
	})

	It("Should deliver a non-zero exit as a successful result, not an error", func() {
		tools := appWithExecutables(map[string]string{"fail": "#!/bin/sh\nexit 4\n"})
		srv, _ := BuildServer(tools, Options{Name: "app", Version: "v1", LogOutput: io.Discard})

		cs := connect(ctx, srv)
		defer cs.Close()

		text, isError := callText(ctx, cs, "fail", nil)
		Expect(isError).To(BeFalse())

		var result tools2.CommandResult
		Expect(json.Unmarshal([]byte(text), &result)).To(Succeed())
		Expect(result.ExitCode).To(Equal(4))
	})

	It("Should report an execution failure as an error result", func() {
		app := fisk.New("app", "an app")
		app.Command("broken", "a command")
		tools := toolsFor(app)
		tools[0].AppPath = "/nonexistent/binary"

		srv, _ := BuildServer(tools, Options{Name: "app", Version: "v1", LogOutput: io.Discard})

		cs := connect(ctx, srv)
		defer cs.Close()

		_, isError := callText(ctx, cs, "broken", nil)
		Expect(isError).To(BeTrue())
	})

	It("Should fail a call that exceeds the per-call timeout", func() {
		tools := appWithExecutables(map[string]string{"slow": "#!/bin/sh\nsleep 2\n"})
		srv, _ := BuildServer(tools, Options{Name: "app", Version: "v1", CallTimeout: 100 * time.Millisecond, LogOutput: io.Discard})

		cs := connect(ctx, srv)
		defer cs.Close()

		_, isError := callText(ctx, cs, "slow", nil)
		Expect(isError).To(BeTrue())
	})

	It("Should log the command line being run without its output", func() {
		tools := appWithExecutables(map[string]string{"ping": "#!/bin/sh\necho pong\n"})

		var logbuf bytes.Buffer
		srv, _ := BuildServer(tools, Options{Name: "app", Version: "v1", LogOutput: &logbuf})

		cs := connect(ctx, srv)
		defer cs.Close()

		_, isError := callText(ctx, cs, "ping", nil)
		Expect(isError).To(BeFalse())

		// The log names the command that ran, but not its output.
		Expect(logbuf.String()).To(ContainSubstring("Running ping"))
		Expect(logbuf.String()).NotTo(ContainSubstring("pong"))
	})

	It("Should serialize calls beyond the concurrency limit", func() {
		tools := appWithExecutables(map[string]string{"work": "#!/bin/sh\nsleep 0.4\n"})
		srv, _ := BuildServer(tools, Options{Name: "app", Version: "v1", Concurrency: 1, CallTimeout: 5 * time.Second, LogOutput: io.Discard})

		cs := connect(ctx, srv)
		defer cs.Close()

		start := time.Now()
		done := make(chan struct{}, 2)
		for i := 0; i < 2; i++ {
			go func() {
				defer GinkgoRecover()
				_, isError := callText(ctx, cs, "work", nil)
				Expect(isError).To(BeFalse())
				done <- struct{}{}
			}()
		}
		<-done
		<-done

		// With concurrency 1, two 0.4s calls cannot overlap, so the total must
		// exceed a single call's duration by a clear margin. Only a lower bound is
		// asserted, so a slow machine cannot make this flaky.
		Expect(time.Since(start)).To(BeNumerically(">", 700*time.Millisecond))
	})
})

var _ = Describe("Serve", func() {
	It("Should shut down promptly when a client holds the SSE stream open", func() {
		// A streamable HTTP client opens a standalone, long-lived SSE GET stream
		// after initialization. http.Server.Shutdown waits for in-flight requests
		// to go idle, which that stream never does, so unless the serving code
		// cancels request contexts a single interrupt blocks for the full
		// shutdownTimeout. This guards that the shutdown returns well within it.
		app := fisk.New("app", "an app")
		app.Command("ping", "a command")
		tools := toolsFor(app)
		tools[0].AppPath = writeExecutable("#!/bin/sh\necho pong\n")

		srv, registered := BuildServer(tools, Options{Name: "app", Version: "v1", LogOutput: io.Discard})
		Expect(registered).To(ConsistOf("ping"))

		ln, err := net.Listen("tcp", "127.0.0.1:0")
		Expect(err).NotTo(HaveOccurred())

		serveCtx, cancelServe := context.WithCancel(context.Background())
		defer cancelServe()

		errCh := make(chan error, 1)
		go func() {
			defer GinkgoRecover()
			errCh <- serveListener(serveCtx, ln, srv, registered, Options{Name: "app", Version: "v1", LogOutput: io.Discard})
		}()

		clientCtx, cancelClient := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancelClient()

		transport := &mcp.StreamableClientTransport{Endpoint: "http://" + ln.Addr().String()}
		client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "v1"}, nil)
		cs, err := client.Connect(clientCtx, transport, nil)
		Expect(err).NotTo(HaveOccurred())
		defer cs.Close()

		// Confirm the session is live (and thus the standalone SSE stream is open)
		// before triggering shutdown, so the prompt-return assertion is meaningful.
		_, err = cs.ListTools(clientCtx, nil)
		Expect(err).NotTo(HaveOccurred())

		cancelServe()

		// With the fix the hanging stream is unblocked and Shutdown drains at once,
		// returning nil; without it the call blocks for the whole shutdownTimeout,
		// so a window comfortably under that timeout is the regression assertion.
		Eventually(errCh, shutdownTimeout-time.Second).Should(Receive(BeNil()))
	})
})

var _ = Describe("config mode", func() {
	It("Should validate an MCP config that has no prompt or model", func() {
		cfg, err := config.ParseConfigForMode([]byte("application_path: /bin/echo\n"), config.ModeMCP)
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg.ApplicationPath).To(Equal("/bin/echo"))
		// Identity defaults to the binary basename, used as the server name.
		Expect(cfg.Identity).To(Equal("echo"))
	})

	It("Should still require a prompt and model in agent mode", func() {
		_, err := config.ParseConfigForMode([]byte("application_path: /bin/echo\n"), config.ModeAgent)
		Expect(err).To(HaveOccurred())
	})
})

var _ = Describe("claudeAddHint", func() {
	It("Should suggest an http transport command on localhost for an unspecified bind", func() {
		hint := claudeAddHint("myagent", &net.TCPAddr{Port: 8080})
		Expect(hint).To(Equal("claude mcp add --transport http myagent http://localhost:8080"))
	})

	It("Should rewrite an unspecified IPv6 bind host to localhost", func() {
		hint := claudeAddHint("myagent", &net.TCPAddr{IP: net.IPv6unspecified, Port: 8080})
		Expect(hint).To(Equal("claude mcp add --transport http myagent http://localhost:8080"))
	})

	It("Should preserve a concrete bind host", func() {
		hint := claudeAddHint("myagent", &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 9000})
		Expect(hint).To(Equal("claude mcp add --transport http myagent http://127.0.0.1:9000"))
	})

	It("Should fall back to a default name when the identity is empty", func() {
		hint := claudeAddHint("", &net.TCPAddr{Port: 8080})
		Expect(hint).To(Equal("claude mcp add --transport http fisk-ai http://localhost:8080"))
	})
})

var _ = Describe("Confirm gating over MCP", func() {
	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

	approve := func(_ context.Context, _ *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"approve": true}}, nil
	}

	// denies asserts that an ai:confirm command handled by the given elicitation
	// handler is refused and never runs: the result is an error carrying wantText
	// and not the command's output marker.
	denies := func(handler func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error), wantText string) {
		GinkgoHelper()

		tools := taggedExecutable("deploy", "ai:confirm", "deployed")
		srv, _ := BuildServer(tools, Options{Name: "app", Version: "v1", LogOutput: io.Discard})

		cs := connectElicit(ctx, srv, handler)
		defer cs.Close()

		text, isError := callText(ctx, cs, "deploy", nil)
		Expect(isError).To(BeTrue())
		Expect(text).To(ContainSubstring(wantText))
		Expect(text).NotTo(ContainSubstring("deployed"))
	}

	It("Should run an ai:confirm tool when the client approves", func() {
		tools := taggedExecutable("deploy", "ai:confirm", "deployed")
		srv, _ := BuildServer(tools, Options{Name: "app", Version: "v1", LogOutput: io.Discard})

		cs := connectElicit(ctx, srv, approve)
		defer cs.Close()

		text, isError := callText(ctx, cs, "deploy", nil)
		Expect(isError).To(BeFalse())

		var result tools2.CommandResult
		Expect(json.Unmarshal([]byte(text), &result)).To(Succeed())
		Expect(result.Output).To(Equal("deployed\n"))
	})

	It("Should deny when the user declines", func() {
		denies(func(_ context.Context, _ *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
			return &mcp.ElicitResult{Action: "decline"}, nil
		}, "declined")
	})

	It("Should deny when the user dismisses the prompt", func() {
		denies(func(_ context.Context, _ *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
			return &mcp.ElicitResult{Action: "cancel"}, nil
		}, "dismissed")
	})

	It("Should deny when the user accepts without approving", func() {
		denies(func(_ context.Context, _ *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
			return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"approve": false}}, nil
		}, "chose not to run")
	})

	It("Should deny when the answer violates the boolean schema", func() {
		// The SDK validates the client's answer against the requested schema, so a
		// non-boolean approve is rejected before it reaches the handler; it surfaces
		// as an elicitation error and denies. The handler's own checked type
		// assertion remains as defense in depth should a client ever bypass this.
		denies(func(_ context.Context, _ *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
			return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"approve": "yes"}}, nil
		}, "failed")
	})

	It("Should deny when the elicitation itself errors", func() {
		denies(func(_ context.Context, _ *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
			return nil, io.EOF
		}, "failed")
	})

	It("Should run an ai:confirm tool ungated when the client cannot elicit", func() {
		tools := taggedExecutable("deploy", "ai:confirm", "deployed")
		logs := &safeBuffer{}
		srv, _ := BuildServer(tools, Options{Name: "app", Version: "v1", LogOutput: logs})

		cs := connect(ctx, srv)
		defer cs.Close()

		text, isError := callText(ctx, cs, "deploy", nil)
		Expect(isError).To(BeFalse())

		var result tools2.CommandResult
		Expect(json.Unmarshal([]byte(text), &result)).To(Succeed())
		Expect(result.Output).To(Equal("deployed\n"))
		Expect(logs.String()).To(ContainSubstring("ungated"))
	})

	It("Should not elicit for a tool that carries no confirm tag", func() {
		tools := taggedExecutable("list", "impact:ro", "listed")
		srv, _ := BuildServer(tools, Options{Name: "app", Version: "v1", LogOutput: io.Discard})

		// A decline handler would deny the call if it were gated; a successful run
		// with output proves the tool was never gated.
		cs := connectElicit(ctx, srv, func(_ context.Context, _ *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
			return &mcp.ElicitResult{Action: "decline"}, nil
		})
		defer cs.Close()

		text, isError := callText(ctx, cs, "list", nil)
		Expect(isError).To(BeFalse())

		var result tools2.CommandResult
		Expect(json.Unmarshal([]byte(text), &result)).To(Succeed())
		Expect(result.Output).To(Equal("listed\n"))
	})

	It("Should gate a configured confirm tag the same as ai:confirm", func() {
		tools := taggedExecutable("write", "impact:rw", "wrote")
		srv, _ := BuildServer(tools, Options{Name: "app", Version: "v1", ConfirmTags: []string{"impact:rw"}, LogOutput: io.Discard})

		cs := connectElicit(ctx, srv, func(_ context.Context, _ *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
			return &mcp.ElicitResult{Action: "decline"}, nil
		})
		defer cs.Close()

		text, isError := callText(ctx, cs, "write", nil)
		Expect(isError).To(BeTrue())
		Expect(text).NotTo(ContainSubstring("wrote"))
	})

	It("Should not gate a tag that is not configured as a confirm tag", func() {
		tools := taggedExecutable("write", "impact:rw", "wrote")
		srv, _ := BuildServer(tools, Options{Name: "app", Version: "v1", LogOutput: io.Discard})

		cs := connectElicit(ctx, srv, func(_ context.Context, _ *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
			return &mcp.ElicitResult{Action: "decline"}, nil
		})
		defer cs.Close()

		text, isError := callText(ctx, cs, "write", nil)
		Expect(isError).To(BeFalse())

		var result tools2.CommandResult
		Expect(json.Unmarshal([]byte(text), &result)).To(Succeed())
		Expect(result.Output).To(Equal("wrote\n"))
	})

	It("Should refuse an ai:confirm tool in always mode when the client cannot elicit", func() {
		tools := taggedExecutable("deploy", "ai:confirm", "deployed")
		srv, _ := BuildServer(tools, Options{Name: "app", Version: "v1", ConfirmMode: ConfirmAlways, LogOutput: io.Discard})

		cs := connect(ctx, srv)
		defer cs.Close()

		text, isError := callText(ctx, cs, "deploy", nil)
		Expect(isError).To(BeTrue())
		Expect(text).To(ContainSubstring("requires approval"))
		Expect(text).NotTo(ContainSubstring("deployed"))
	})

	It("Should run an ai:confirm tool in always mode when the client approves", func() {
		tools := taggedExecutable("deploy", "ai:confirm", "deployed")
		srv, _ := BuildServer(tools, Options{Name: "app", Version: "v1", ConfirmMode: ConfirmAlways, LogOutput: io.Discard})

		cs := connectElicit(ctx, srv, approve)
		defer cs.Close()

		text, isError := callText(ctx, cs, "deploy", nil)
		Expect(isError).To(BeFalse())

		var result tools2.CommandResult
		Expect(json.Unmarshal([]byte(text), &result)).To(Succeed())
		Expect(result.Output).To(Equal("deployed\n"))
	})

	It("Should run an ai:confirm tool ungated in never mode without eliciting", func() {
		tools := taggedExecutable("deploy", "ai:confirm", "deployed")
		srv, _ := BuildServer(tools, Options{Name: "app", Version: "v1", ConfirmMode: ConfirmNever, LogOutput: io.Discard})

		// A decline handler would deny the call if it were gated; a successful run on
		// an elicitation-capable client proves never mode skips the gate entirely.
		cs := connectElicit(ctx, srv, func(_ context.Context, _ *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
			return &mcp.ElicitResult{Action: "decline"}, nil
		})
		defer cs.Close()

		text, isError := callText(ctx, cs, "deploy", nil)
		Expect(isError).To(BeFalse())

		var result tools2.CommandResult
		Expect(json.Unmarshal([]byte(text), &result)).To(Succeed())
		Expect(result.Output).To(Equal("deployed\n"))
	})
})
