//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package fisk

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/choria-io/fisk"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/choria-io/fisk-ai/config"
	"github.com/choria-io/fisk-ai/internal/toolkit"
)

func TestFisk(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Toolkit/Fisk")
}

// writeExecutable writes body to an executable file in a temp dir and returns its
// path, for use as a stand-in application binary.
func writeExecutable(body string) string {
	GinkgoHelper()

	path := filepath.Join(GinkgoT().TempDir(), "app")
	Expect(os.WriteFile(path, []byte(body), 0o700)).To(Succeed())
	return path
}

// toolsByName indexes tools by their name for order-independent assertions.
func toolsByName(tools []*FiskCommandTool) map[string]*FiskCommandTool {
	out := make(map[string]*FiskCommandTool, len(tools))
	for _, t := range tools {
		out[t.Name()] = t
	}
	return out
}

// names returns the FiskCommandTool names in order.
func names(tools []*FiskCommandTool) []string {
	out := make([]string, 0, len(tools))
	for _, t := range tools {
		out = append(out, t.Name())
	}
	return out
}

// introspect mirrors the production path: it drives the application's real
// --fisk-introspect handler in-process, capturing the JSON it writes to stdout
// and unmarshaling it, yielding a model whose schemas are populated but whose
// Values are gone (as they are over the --fisk-introspect process boundary).
func introspect(app *fisk.Application) *fisk.ApplicationModel {
	GinkgoHelper()

	// --fisk-introspect calls terminate(0); make it a no-op so the test survives.
	app.Terminate(func(int) {})

	r, w, err := os.Pipe()
	Expect(err).NotTo(HaveOccurred())

	stdout := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = stdout }()

	// read concurrently so a large model can't fill the pipe and block the write.
	captured := make(chan []byte, 1)
	go func() {
		data, _ := io.ReadAll(r)
		captured <- data
	}()

	_, err = app.Parse([]string{"--fisk-introspect"})
	Expect(err).NotTo(HaveOccurred())
	Expect(w.Close()).To(Succeed())

	var m fisk.ApplicationModel
	Expect(json.Unmarshal(<-captured, &m)).To(Succeed())
	return &m
}

var _ = Describe("ApplicationTools", func() {
	It("Should return an error for a nil model", func() {
		tools, err := ApplicationTools(nil)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("application model is nil"))
		Expect(tools).To(BeNil())
	})

	It("Should return no tools when the model has no command group", func() {
		tools, err := ApplicationTools(&fisk.ApplicationModel{})
		Expect(err).NotTo(HaveOccurred())
		Expect(tools).To(BeEmpty())
	})

	It("Should return no tools when the application has no commands", func() {
		tools, err := ApplicationTools(fisk.New("app", "an app").Model())
		Expect(err).NotTo(HaveOccurred())
		Expect(tools).To(BeEmpty())
	})

	It("Should error when a leaf command has no precomputed schema (older fisk)", func() {
		app := fisk.New("app", "an app")
		app.Command("one", "first command")

		// app.Model() carries no precomputed schemas, as an older fisk's
		// introspect output would not.
		tools, err := ApplicationTools(app.Model())
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("no precomputed schema"))
		Expect(tools).To(BeNil())
	})

	It("Should create a tool for each top level command", func() {
		app := fisk.New("app", "an app")
		app.Command("one", "first command")
		app.Command("two", "second command")

		tools, err := ApplicationTools(introspect(app))
		Expect(err).NotTo(HaveOccurred())
		Expect(tools).To(HaveLen(2))

		byName := toolsByName(tools)
		Expect(byName).To(HaveKey("one"))
		Expect(byName).To(HaveKey("two"))
		Expect(byName["one"].Description()).To(Equal("first command"))
		Expect(byName["one"].Command()).To(Equal("one"))
	})

	It("Should build the restricted input schema from arguments and flags", func() {
		app := fisk.New("app", "an app")
		cmd := app.Command("deploy", "deploy things")
		cmd.Arg("target", "where to deploy").Required().String()
		cmd.Flag("force", "force the deploy").Bool()
		cmd.Flag("count", "how many").Int()

		tools, err := ApplicationTools(introspect(app))
		Expect(err).NotTo(HaveOccurred())
		Expect(tools).To(HaveLen(1))

		schema := tools[0].InputSchema()
		Expect(schema["type"]).To(Equal("object"))
		Expect(schema["additionalProperties"]).To(Equal(false))

		props, ok := schema["properties"].(map[string]any)
		Expect(ok).To(BeTrue())
		Expect(props).To(HaveKey("target"))
		Expect(props).To(HaveKey("force"))

		// type fidelity survives the introspect round-trip (it would be "string"
		// if the schema were recomputed from the Value-less deserialized model)
		Expect(props["count"].(map[string]any)["type"]).To(Equal("integer"))

		// the restricted schema keeps only genuinely required fields as required
		Expect(schema["required"]).To(ConsistOf("target"))
	})

	It("Should capture the command model for later argument and flag mapping", func() {
		app := fisk.New("app", "an app")
		cmd := app.Command("deploy", "deploy things")
		cmd.Arg("target", "where to deploy").Required().String()
		cmd.Flag("force", "force the deploy").Bool()

		tools, err := ApplicationTools(introspect(app))
		Expect(err).NotTo(HaveOccurred())
		Expect(tools).To(HaveLen(1))

		model := tools[0].Model
		Expect(model).NotTo(BeNil())

		argNames := []string{}
		for _, a := range model.Args {
			argNames = append(argNames, a.Name)
		}
		Expect(argNames).To(ContainElement("target"))

		var force *fisk.FlagModel
		for _, f := range model.Flags {
			if f.Name == "force" {
				force = f
			}
		}
		Expect(force).NotTo(BeNil())
		Expect(force.Boolean).To(BeTrue())
	})

	It("Should carry the command tags", func() {
		app := fisk.New("app", "an app")
		app.Command("one", "first command").Tag("admin", "write")

		tools, err := ApplicationTools(introspect(app))
		Expect(err).NotTo(HaveOccurred())
		Expect(tools).To(HaveLen(1))
		Expect(tools[0].Tags()).To(ConsistOf("admin", "write"))
	})

	It("Should create a tool per leaf command using the full command path as the name", func() {
		app := fisk.New("app", "an app")
		auth := app.Command("auth", "auth commands")
		user := auth.Command("user", "user commands")
		user.Command("add", "add a user")
		user.Command("rm", "remove a user")
		auth.Command("login", "log in")

		tools, err := ApplicationTools(introspect(app))
		Expect(err).NotTo(HaveOccurred())

		byName := toolsByName(tools)
		Expect(byName).To(HaveLen(3))
		Expect(byName).To(HaveKey("auth_user_add"))
		Expect(byName).To(HaveKey("auth_user_rm"))
		Expect(byName).To(HaveKey("auth_login"))
		Expect(byName["auth_user_add"].Command()).To(Equal("auth user add"))
		Expect(byName["auth_user_add"].Description()).To(Equal("add a user"))
	})

	It("Should not create tools for grouping commands that only hold subcommands", func() {
		app := fisk.New("app", "an app")
		auth := app.Command("auth", "auth commands")
		auth.Command("login", "log in")

		tools, err := ApplicationTools(introspect(app))
		Expect(err).NotTo(HaveOccurred())

		byName := toolsByName(tools)
		Expect(byName).To(HaveLen(1))
		Expect(byName).NotTo(HaveKey("auth"))
		Expect(byName).To(HaveKey("auth_login"))
	})

	It("Should keep same-named leaves under different parents distinct", func() {
		app := fisk.New("app", "an app")
		app.Command("auth", "auth commands").Command("add", "add auth")
		app.Command("account", "account commands").Command("add", "add account")

		tools, err := ApplicationTools(introspect(app))
		Expect(err).NotTo(HaveOccurred())

		byName := toolsByName(tools)
		Expect(byName).To(HaveLen(2))
		Expect(byName).To(HaveKey("auth_add"))
		Expect(byName).To(HaveKey("account_add"))
	})

	It("Should skip hidden commands and the subtrees below them", func() {
		app := fisk.New("app", "an app")
		secret := app.Command("secret", "hidden commands").Hidden()
		secret.Command("leak", "should never appear")
		app.Command("visible", "a visible command")

		tools, err := ApplicationTools(introspect(app))
		Expect(err).NotTo(HaveOccurred())
		Expect(tools).To(HaveLen(1))
		Expect(tools[0].Name()).To(Equal("visible"))
	})

	It("Should fall back to the long help when the help is empty", func() {
		app := fisk.New("app", "an app")
		app.Command("x", "").HelpLong("the long form help")

		tools, err := ApplicationTools(introspect(app))
		Expect(err).NotTo(HaveOccurred())
		Expect(tools).To(HaveLen(1))
		Expect(tools[0].Description()).To(Equal("the long form help"))
	})

	Describe("ModelDescription", func() {
		It("Should return the plain help when the command has no tags", func() {
			tool := &FiskCommandTool{Path: []string{"x"}, Model: &fisk.CmdModel{Help: "do a thing"}}
			Expect(tool.ModelDescription()).To(Equal("do a thing"))
		})

		It("Should append the command's tags, including reserved ai: tags, so a prompt can key off them", func() {
			tool := &FiskCommandTool{Path: []string{"stream", "rm"}, Model: &fisk.CmdModel{Help: "remove a stream", Tags: []string{"impact:rw", confirmTag}}}
			Expect(tool.ModelDescription()).To(Equal("remove a stream\n\nTags: impact:rw, ai:confirm"))
		})

		It("Should emit only the tag line when the command has no help", func() {
			tool := &FiskCommandTool{Path: []string{"x"}, Model: &fisk.CmdModel{Tags: []string{"impact:ro"}}}
			Expect(tool.ModelDescription()).To(Equal("Tags: impact:ro"))
		})

		It("Should combine the short and long help so the model gets the richer guidance", func() {
			tool := &FiskCommandTool{Path: []string{"stream", "add"}, Model: &fisk.CmdModel{Help: "create a stream", HelpLong: "Creates a stream capturing messages on the given subjects."}}
			Expect(tool.ModelDescription()).To(Equal("create a stream\n\nCreates a stream capturing messages on the given subjects."))
		})

		It("Should not repeat the short help when the long help already contains it", func() {
			tool := &FiskCommandTool{Path: []string{"stream", "add"}, Model: &fisk.CmdModel{Help: "create a stream", HelpLong: "create a stream capturing messages on the given subjects"}}
			Expect(tool.ModelDescription()).To(Equal("create a stream capturing messages on the given subjects"))
		})

		It("Should use the long help with tags when only the long help is set", func() {
			tool := &FiskCommandTool{Path: []string{"stream", "add"}, Model: &fisk.CmdModel{HelpLong: "Creates a stream.", Tags: []string{"impact:rw"}}}
			Expect(tool.ModelDescription()).To(Equal("Creates a stream.\n\nTags: impact:rw"))
		})
	})

	It("Should preserve names, tags and schema on a model round-tripped through introspect JSON", func() {
		app := fisk.New("app", "an app")
		auth := app.Command("auth", "auth commands")
		add := auth.Command("add", "add a thing").Tag("admin")
		add.Arg("target", "where to add").Required().String()

		tools, err := ApplicationTools(introspect(app))
		Expect(err).NotTo(HaveOccurred())
		Expect(tools).To(HaveLen(1))
		Expect(tools[0].Name()).To(Equal("auth_add"))
		Expect(tools[0].Description()).To(Equal("add a thing"))
		Expect(tools[0].Tags()).To(ConsistOf("admin"))

		props, ok := tools[0].InputSchema()["properties"].(map[string]any)
		Expect(ok).To(BeTrue())
		Expect(props).To(HaveKey("target"))
	})
})

var _ = Describe("FilterTools", func() {
	// fixture returns a fresh tool list covering tagged, untagged and ai:deny tools.
	fixture := func() []*FiskCommandTool {
		return []*FiskCommandTool{
			{Path: []string{"auth", "user", "add"}, Model: &fisk.CmdModel{Tags: []string{"admin"}}},
			{Path: []string{"auth", "login"}, Model: &fisk.CmdModel{}},
			{Path: []string{"server", "run"}, Model: &fisk.CmdModel{Tags: []string{denyTag}}},
			{Path: []string{"server", "info"}, Model: &fisk.CmdModel{}},
		}
	}

	It("Should always remove ai:deny tools regardless of mode", func() {
		inc, err := FilterTools(fixture(), nil, IncludeFilter)
		Expect(err).NotTo(HaveOccurred())
		Expect(names(inc)).NotTo(ContainElement("server_run"))

		exc, err := FilterTools(fixture(), nil, ExcludeFilter)
		Expect(err).NotTo(HaveOccurred())
		Expect(names(exc)).NotTo(ContainElement("server_run"))
	})

	It("Should keep everything but ai:deny when the filter is nil", func() {
		tools, err := FilterTools(fixture(), nil, IncludeFilter)
		Expect(err).NotTo(HaveOccurred())
		Expect(names(tools)).To(ConsistOf("auth_user_add", "auth_login", "server_info"))
	})

	It("Should include only tools whose name matches a name pattern", func() {
		tools, err := FilterTools(fixture(), &config.ToolFilter{Tools: []string{"^auth"}}, IncludeFilter)
		Expect(err).NotTo(HaveOccurred())
		Expect(names(tools)).To(ConsistOf("auth_user_add", "auth_login"))
	})

	It("Should exclude tools whose name matches a name pattern", func() {
		tools, err := FilterTools(fixture(), &config.ToolFilter{Tools: []string{"^auth"}}, ExcludeFilter)
		Expect(err).NotTo(HaveOccurred())
		Expect(names(tools)).To(ConsistOf("server_info"))
	})

	It("Should match the underscore-joined tool name, not the space-joined command", func() {
		tools, err := FilterTools(fixture(), &config.ToolFilter{Tools: []string{"auth_user"}}, IncludeFilter)
		Expect(err).NotTo(HaveOccurred())
		Expect(names(tools)).To(ConsistOf("auth_user_add"))

		// the space-joined command form must not match
		none, err := FilterTools(fixture(), &config.ToolFilter{Tools: []string{"auth user"}}, IncludeFilter)
		Expect(err).NotTo(HaveOccurred())
		Expect(none).To(BeEmpty())
	})

	It("Should include tools matching a tag", func() {
		tools, err := FilterTools(fixture(), &config.ToolFilter{Tags: []string{"admin"}}, IncludeFilter)
		Expect(err).NotTo(HaveOccurred())
		Expect(names(tools)).To(ConsistOf("auth_user_add"))
	})

	It("Should exclude tools matching a tag", func() {
		tools, err := FilterTools(fixture(), &config.ToolFilter{Tags: []string{"admin"}}, ExcludeFilter)
		Expect(err).NotTo(HaveOccurred())
		Expect(names(tools)).To(ConsistOf("auth_login", "server_info"))
	})

	It("Should treat an empty tag as matching untagged tools", func() {
		tools, err := FilterTools(fixture(), &config.ToolFilter{Tags: []string{""}}, IncludeFilter)
		Expect(err).NotTo(HaveOccurred())
		Expect(names(tools)).To(ConsistOf("auth_login", "server_info"))
	})

	It("Should never include an ai:deny tool even when it matches an include pattern", func() {
		tools, err := FilterTools(fixture(), &config.ToolFilter{Tools: []string{"^server"}}, IncludeFilter)
		Expect(err).NotTo(HaveOccurred())
		Expect(names(tools)).To(ConsistOf("server_info"))
		Expect(names(tools)).NotTo(ContainElement("server_run"))
	})

	It("Should let ai:deny win over ai:confirm so a doubly-tagged tool is never exposed", func() {
		both := []*FiskCommandTool{{Path: []string{"server", "wipe"}, Model: &fisk.CmdModel{Tags: []string{denyTag, confirmTag}}}}
		tools, err := FilterTools(both, nil, IncludeFilter)
		Expect(err).NotTo(HaveOccurred())
		Expect(tools).To(BeEmpty())
	})

	It("Should return an error for an invalid name pattern", func() {
		tools, err := FilterTools(fixture(), &config.ToolFilter{Tools: []string{"("}}, IncludeFilter)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("invalid tool filter pattern"))
		Expect(tools).To(BeNil())
	})
})

var _ = Describe("filterExposed", func() {
	// fixture mirrors the FilterTools fixture; filterExposed runs after LoadTools,
	// which has already stripped ai:deny, but the deny tool is kept here to assert
	// the narrowing can never re-add one.
	fixture := func() []*FiskCommandTool {
		return []*FiskCommandTool{
			{Path: []string{"auth", "user", "add"}, Model: &fisk.CmdModel{Tags: []string{"admin"}}},
			{Path: []string{"auth", "login"}, Model: &fisk.CmdModel{}},
			{Path: []string{"server", "run"}, Model: &fisk.CmdModel{Tags: []string{denyTag}}},
			{Path: []string{"server", "info"}, Model: &fisk.CmdModel{}},
		}
	}

	It("Should serve the set unchanged when the selection is nil", func() {
		// A nil selection does no narrowing; stripping ai:deny is LoadTools' job, so
		// filterExposed returns its input verbatim.
		tools, err := filterExposed(fixture(), nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(names(tools)).To(ConsistOf("auth_user_add", "auth_login", "server_run", "server_info"))
	})

	It("Should ignore include and exclude that carry no patterns or tags", func() {
		sel := &config.ExposedToolSelection{Include: &config.ToolFilter{}, Exclude: &config.ToolFilter{}}
		tools, err := filterExposed(fixture(), sel)
		Expect(err).NotTo(HaveOccurred())
		Expect(names(tools)).To(ConsistOf("auth_user_add", "auth_login", "server_run", "server_info"))
	})

	It("Should narrow to the include and drop ai:deny even when it matches", func() {
		sel := &config.ExposedToolSelection{Include: &config.ToolFilter{Tools: []string{"^server"}}}
		tools, err := filterExposed(fixture(), sel)
		Expect(err).NotTo(HaveOccurred())
		Expect(names(tools)).To(ConsistOf("server_info"))
	})

	It("Should drop tools matched by the exclude", func() {
		sel := &config.ExposedToolSelection{Exclude: &config.ToolFilter{Tools: []string{"^auth"}}}
		tools, err := filterExposed(fixture(), sel)
		Expect(err).NotTo(HaveOccurred())
		Expect(names(tools)).To(ConsistOf("server_info"))
	})

	It("Should apply the include then the exclude", func() {
		sel := &config.ExposedToolSelection{
			Include: &config.ToolFilter{Tools: []string{"^auth"}},
			Exclude: &config.ToolFilter{Tags: []string{"admin"}},
		}
		tools, err := filterExposed(fixture(), sel)
		Expect(err).NotTo(HaveOccurred())
		Expect(names(tools)).To(ConsistOf("auth_login"))
	})

	It("Should return an error for an invalid include pattern", func() {
		sel := &config.ExposedToolSelection{Include: &config.ToolFilter{Tools: []string{"("}}}
		tools, err := filterExposed(fixture(), sel)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("invalid tool filter pattern"))
		Expect(tools).To(BeNil())
	})
})

var _ = Describe("Command execution", func() {
	// doTool builds an "app do" command tool with a flag and a required argument,
	// bound to the given application path.
	doTool := func(appPath string) *FiskCommandTool {
		GinkgoHelper()

		app := fisk.New("app", "an app")
		do := app.Command("do", "do a thing")
		do.Flag("level", "log level").Enum("debug", "info", "warn")
		do.Arg("subject", "the subject").Required().String()

		tools, err := ApplicationTools(introspect(app))
		Expect(err).NotTo(HaveOccurred())

		tool := toolsByName(tools)["do"]
		Expect(tool).NotTo(BeNil())

		tool.AppPath = appPath
		return tool
	}

	It("Should run the command and return its output and exit code", func() {
		tool := doTool(writeExecutable("#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done\n"))

		result, err := tool.Execute(context.Background(), json.RawMessage(`{"level":"info","subject":"hello"}`))
		Expect(err).NotTo(HaveOccurred())

		Expect(result.ExitCode).To(Equal(0))
		Expect(result.Command).To(Equal("do --level=info -- hello"))
		Expect(result.Output).To(Equal("do\n--level=info\n--\nhello\n"))
		Expect(result.Truncated).To(BeFalse())
	})

	It("Should report a non-zero exit in the result rather than as an error", func() {
		tool := doTool(writeExecutable("#!/bin/sh\necho oops >&2\nexit 3\n"))

		result, err := tool.Execute(context.Background(), json.RawMessage(`{"subject":"x"}`))
		Expect(err).NotTo(HaveOccurred())

		Expect(result.ExitCode).To(Equal(3))
		Expect(result.Output).To(Equal("oops\n"))
	})

	It("Should resolve the full command line for tracing", func() {
		tool := doTool(writeExecutable("#!/bin/sh\n"))

		cmdline, err := tool.CommandLine(json.RawMessage(`{"level":"info","subject":"hello"}`))
		Expect(err).NotTo(HaveOccurred())
		Expect(cmdline).To(Equal("do --level=info -- hello"))
	})

	It("Should error from CommandLine when arguments cannot be mapped", func() {
		tool := doTool(writeExecutable("#!/bin/sh\n"))

		_, err := tool.CommandLine(json.RawMessage(`{"unknown":"x"}`))
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("unknown property"))
	})

	It("Should render the resolved command line as a trace line", func() {
		tool := doTool(writeExecutable("#!/bin/sh\n"))

		Expect(tool.TraceLine(json.RawMessage(`{"level":"info","subject":"hello"}`))).To(Equal("do --level=info -- hello"))
	})

	It("Should fall back to the command path when trace arguments cannot be mapped", func() {
		tool := doTool(writeExecutable("#!/bin/sh\n"))

		Expect(tool.TraceLine(json.RawMessage(`{"unknown":"x"}`))).To(Equal("do"))
	})

	It("Should leave short argument values intact in the short trace line", func() {
		tool := doTool(writeExecutable("#!/bin/sh\n"))

		Expect(tool.TraceLineShort(json.RawMessage(`{"level":"info","subject":"hello"}`))).To(Equal("do --level=info -- hello"))
	})

	It("Should elide the middle of a long positional value, keeping head and tail", func() {
		tool := doTool(writeExecutable("#!/bin/sh\n"))

		out := tool.TraceLineShort(json.RawMessage(`{"subject":"orders.events.created"}`))
		Expect(out).To(Equal("do -- orders.eve...reated"))
	})

	It("Should elide only the value of a long flag, never its name", func() {
		app := fisk.New("app", "an app")
		send := app.Command("send", "send a thing")
		send.Flag("subject", "the subject").String()
		send.Arg("body", "the body").Required().String()

		tools, err := ApplicationTools(introspect(app))
		Expect(err).NotTo(HaveOccurred())
		tool := toolsByName(tools)["send"]
		Expect(tool).NotTo(BeNil())

		out := tool.TraceLineShort(json.RawMessage(`{"subject":"orders.events.created","body":"x"}`))
		Expect(out).To(ContainSubstring("--subject=orders.eve...reated"))
		Expect(out).To(ContainSubstring("-- x"))
	})

	It("Should fall back to the command path when short trace arguments cannot be mapped", func() {
		tool := doTool(writeExecutable("#!/bin/sh\n"))

		Expect(tool.TraceLineShort(json.RawMessage(`{"unknown":"x"}`))).To(Equal("do"))
	})

	It("Should set LLMFORMAT=1 in the command environment", func() {
		tool := doTool(writeExecutable("#!/bin/sh\nprintf 'LLMFORMAT=%s\\n' \"$LLMFORMAT\"\n"))

		result, err := tool.Execute(context.Background(), json.RawMessage(`{"subject":"x"}`))
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Output).To(Equal("LLMFORMAT=1\n"))
	})

	It("Should strip every agent credential variable from the command environment", func() {
		for _, name := range []string{
			"ANTHROPIC_API_KEY",
			"ANTHROPIC_AUTH_TOKEN",
			"ANTHROPIC_IDENTITY_TOKEN",
			"ANTHROPIC_WEBHOOK_SIGNING_KEY",
			"ANTHROPIC_CUSTOM_HEADERS",
		} {
			GinkgoT().Setenv(name, "super-secret")
		}
		tool := doTool(writeExecutable("#!/bin/sh\n" +
			"printf 'ANTHROPIC_API_KEY=[%s]\\n' \"$ANTHROPIC_API_KEY\"\n" +
			"printf 'ANTHROPIC_AUTH_TOKEN=[%s]\\n' \"$ANTHROPIC_AUTH_TOKEN\"\n" +
			"printf 'ANTHROPIC_IDENTITY_TOKEN=[%s]\\n' \"$ANTHROPIC_IDENTITY_TOKEN\"\n" +
			"printf 'ANTHROPIC_WEBHOOK_SIGNING_KEY=[%s]\\n' \"$ANTHROPIC_WEBHOOK_SIGNING_KEY\"\n" +
			"printf 'ANTHROPIC_CUSTOM_HEADERS=[%s]\\n' \"$ANTHROPIC_CUSTOM_HEADERS\"\n"))

		result, err := tool.Execute(context.Background(), json.RawMessage(`{"subject":"x"}`))
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Output).To(Equal("ANTHROPIC_API_KEY=[]\nANTHROPIC_AUTH_TOKEN=[]\nANTHROPIC_IDENTITY_TOKEN=[]\nANTHROPIC_WEBHOOK_SIGNING_KEY=[]\nANTHROPIC_CUSTOM_HEADERS=[]\n"))
	})

	It("Should preserve non-credential environment variables", func() {
		GinkgoT().Setenv("ANTHROPIC_BASE_URL", "https://example.test")
		tool := doTool(writeExecutable("#!/bin/sh\nprintf 'URL=[%s]\\n' \"$ANTHROPIC_BASE_URL\"\n"))

		result, err := tool.Execute(context.Background(), json.RawMessage(`{"subject":"x"}`))
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Output).To(Equal("URL=[https://example.test]\n"))
	})

	It("Should combine stdout and stderr preserving their order", func() {
		tool := doTool(writeExecutable("#!/bin/sh\necho first\necho second >&2\necho third\n"))

		result, err := tool.Execute(context.Background(), json.RawMessage(`{"subject":"x"}`))
		Expect(err).NotTo(HaveOccurred())

		Expect(result.Output).To(Equal("first\nsecond\nthird\n"))
	})

	It("Should treat a null input as an empty argument object", func() {
		tool := doTool(writeExecutable("#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done\n"))

		result, err := tool.Execute(context.Background(), json.RawMessage(`null`))
		Expect(err).NotTo(HaveOccurred())
		Expect(result.ExitCode).To(Equal(0))
		Expect(result.Command).To(Equal("do"))
	})

	It("Should treat an empty input as an empty argument object", func() {
		tool := doTool(writeExecutable("#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done\n"))

		result, err := tool.Execute(context.Background(), nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.ExitCode).To(Equal(0))
		Expect(result.Command).To(Equal("do"))
	})

	It("Should error when no application path is set", func() {
		tool := doTool("")

		_, err := tool.Execute(context.Background(), json.RawMessage(`{"subject":"x"}`))
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("no application path"))
	})

	It("Should error when the binary cannot be run", func() {
		tool := doTool(filepath.Join(GinkgoT().TempDir(), "does-not-exist"))

		_, err := tool.Execute(context.Background(), json.RawMessage(`{"subject":"x"}`))
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("running command"))
	})

	It("Should error when the arguments cannot be mapped to a command line", func() {
		tool := doTool(writeExecutable("#!/bin/sh\n"))

		_, err := tool.Execute(context.Background(), json.RawMessage(`{"unknown":"x"}`))
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("unknown property"))
	})

	It("Should surface a canceled context as an error", func() {
		tool := doTool(writeExecutable("#!/bin/sh\nsleep 5\n"))

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := tool.Execute(ctx, json.RawMessage(`{"subject":"x"}`))
		Expect(err).To(HaveOccurred())
	})
})

var _ = Describe("MissingRequired", func() {
	// doTool builds an "app do" command with an optional "level" flag and a
	// required "subject" argument, its schema computed by fisk introspection.
	doTool := func() *FiskCommandTool {
		GinkgoHelper()

		app := fisk.New("app", "an app")
		do := app.Command("do", "do a thing")
		do.Flag("level", "log level").Enum("debug", "info", "warn")
		do.Arg("subject", "the subject").Required().String()

		tools, err := ApplicationTools(introspect(app))
		Expect(err).NotTo(HaveOccurred())

		tool := toolsByName(tools)["do"]
		Expect(tool).NotTo(BeNil())
		return tool
	}

	It("Should report a required parameter absent from the input", func() {
		Expect(doTool().MissingRequired(json.RawMessage(`{"level":"info"}`))).To(Equal([]string{"subject"}))
	})

	It("Should report every required parameter for an empty or null input", func() {
		tool := doTool()
		Expect(tool.MissingRequired(json.RawMessage(`{}`))).To(Equal([]string{"subject"}))
		Expect(tool.MissingRequired(json.RawMessage(`null`))).To(Equal([]string{"subject"}))
		Expect(tool.MissingRequired(nil)).To(Equal([]string{"subject"}))
	})

	It("Should report nothing when the required parameter is supplied", func() {
		Expect(doTool().MissingRequired(json.RawMessage(`{"subject":"hello"}`))).To(BeEmpty())
	})

	It("Should treat a present required key as supplied for a null or empty value", func() {
		tool := doTool()
		// A present key is supplied even when its value is null or an empty string:
		// fisk renders both as a value, so only an absent key is genuinely missing.
		Expect(tool.MissingRequired(json.RawMessage(`{"subject":null}`))).To(BeEmpty())
		Expect(tool.MissingRequired(json.RawMessage(`{"subject":""}`))).To(BeEmpty())
	})

	It("Should treat a present required key as supplied for a false, zero or empty value", func() {
		// The schema is set directly so the check can be exercised against value
		// types fisk positional strings cannot express; presence, not value, is what
		// decides, so a false, a zero and an empty array all count as supplied.
		tool := &FiskCommandTool{Path: []string{"x"}, Model: &fisk.CmdModel{RestrictedSchema: map[string]any{
			"type":     "object",
			"required": []string{"a", "b", "c"},
			"properties": map[string]any{
				"a": map[string]any{"type": "boolean"},
				"b": map[string]any{"type": "integer"},
				"c": map[string]any{"type": "array"},
			},
		}}}

		Expect(tool.MissingRequired(json.RawMessage(`{"a":false,"b":0,"c":[]}`))).To(BeEmpty())
	})

	It("Should pass a non-object input through for the command to reject", func() {
		tool := doTool()
		Expect(tool.MissingRequired(json.RawMessage(`[]`))).To(BeNil())
		Expect(tool.MissingRequired(json.RawMessage(`"x"`))).To(BeNil())
		Expect(tool.MissingRequired(json.RawMessage(`3`))).To(BeNil())
	})

	It("Should report nothing for a command with no required parameters", func() {
		app := fisk.New("app", "an app")
		app.Command("ls", "list").Flag("all", "show all").Bool()

		tools, err := ApplicationTools(introspect(app))
		Expect(err).NotTo(HaveOccurred())
		tool := toolsByName(tools)["ls"]
		Expect(tool).NotTo(BeNil())

		Expect(tool.MissingRequired(json.RawMessage(`{}`))).To(BeNil())
	})

	It("Should report missing required parameters in schema-declared order", func() {
		app := fisk.New("app", "an app")
		cp := app.Command("cp", "copy")
		cp.Arg("src", "source").Required().String()
		cp.Arg("dst", "destination").Required().String()

		tools, err := ApplicationTools(introspect(app))
		Expect(err).NotTo(HaveOccurred())
		tool := toolsByName(tools)["cp"]
		Expect(tool).NotTo(BeNil())

		required := toolkit.SchemaRequired(tool.InputSchema()["required"])
		Expect(required).To(ConsistOf("src", "dst"))
		Expect(tool.MissingRequired(json.RawMessage(`{}`))).To(Equal(required))
	})

	It("Should name the missing parameters and the full roster in the model message", func() {
		msg := doTool().MissingRequiredMessage([]string{"subject"})
		Expect(msg).To(Equal(`tool "do" was called without required parameter(s): subject. required: subject; optional: level`))
	})
})

var _ = Describe("Global flags", func() {
	// globalApp builds an application with a leaf command "server ls", a mix of
	// global flags, and returns its introspected model. The command carries its own
	// flag so collisions can be exercised.
	globalApp := func() *fisk.Application {
		app := fisk.New("nats", "nats app")
		app.Flag("context", "Configuration context").String()
		app.Flag("user", "Username or Token").String()
		srv := app.Command("server", "server commands")
		srv.Command("ls", "list servers").Flag("expand", "expand output").Bool()
		return app
	}

	It("Should expose only allowlisted globals and inject them into every leaf schema", func() {
		tools, err := ApplicationTools(introspect(globalApp()), "context")
		Expect(err).NotTo(HaveOccurred())

		tool := toolsByName(tools)["server_ls"]
		Expect(tool).NotTo(BeNil())

		props, ok := tool.InputSchema()["properties"].(map[string]any)
		Expect(ok).To(BeTrue())
		Expect(props).To(HaveKey("context"))
		Expect(props).NotTo(HaveKey("user"))
		Expect(props).To(HaveKey("expand"))

		ctx, ok := props["context"].(map[string]any)
		Expect(ok).To(BeTrue())
		Expect(ctx).To(HaveKeyWithValue("type", "string"))
		Expect(ctx).To(HaveKeyWithValue("description", "Global flag: Configuration context"))
	})

	It("Should not mutate the shared command schema when injecting globals", func() {
		model := introspect(globalApp())
		tools, err := ApplicationTools(model, "context")
		Expect(err).NotTo(HaveOccurred())
		tool := toolsByName(tools)["server_ls"]

		// The precomputed schema on the model must not gain the global property, and
		// two InputSchema calls must agree, so the injection is a copy each time.
		_ = tool.InputSchema()
		second := tool.InputSchema()
		Expect(second["properties"]).To(HaveKey("context"))

		modelProps, _ := tool.Model.RestrictedSchema["properties"].(map[string]any)
		Expect(modelProps).NotTo(HaveKey("context"))
	})

	It("Should render an exposed global onto the command line after the command path", func() {
		tools, err := ApplicationTools(introspect(globalApp()), "context")
		Expect(err).NotTo(HaveOccurred())
		tool := toolsByName(tools)["server_ls"]

		cmdline, err := tool.CommandLine(json.RawMessage(`{"context":"prod","expand":true}`))
		Expect(err).NotTo(HaveOccurred())
		Expect(cmdline).To(Equal("server ls --context=prod --expand"))
	})

	It("Should place a global ahead of the positional separator", func() {
		app := fisk.New("nats", "nats app")
		app.Flag("context", "Configuration context").String()
		pub := app.Command("pub", "publish")
		pub.Arg("subject", "the subject").Required().String()

		tools, err := ApplicationTools(introspect(app), "context")
		Expect(err).NotTo(HaveOccurred())
		tool := toolsByName(tools)["pub"]

		cmdline, err := tool.CommandLine(json.RawMessage(`{"context":"prod","subject":"orders"}`))
		Expect(err).NotTo(HaveOccurred())
		Expect(cmdline).To(Equal("pub --context=prod -- orders"))
	})

	It("Should omit a global the model does not supply", func() {
		tools, err := ApplicationTools(introspect(globalApp()), "context")
		Expect(err).NotTo(HaveOccurred())
		tool := toolsByName(tools)["server_ls"]

		cmdline, err := tool.CommandLine(json.RawMessage(`{"expand":true}`))
		Expect(err).NotTo(HaveOccurred())
		Expect(cmdline).To(Equal("server ls --expand"))
	})

	It("Should render a negatable boolean global using fisk's own form", func() {
		app := fisk.New("nats", "nats app")
		app.Flag("trace", "trace protocol").Bool()
		app.Command("ping", "ping servers")

		tools, err := ApplicationTools(introspect(app), "trace")
		Expect(err).NotTo(HaveOccurred())
		tool := toolsByName(tools)["ping"]

		on, err := tool.CommandLine(json.RawMessage(`{"trace":true}`))
		Expect(err).NotTo(HaveOccurred())
		Expect(on).To(Equal("ping --trace"))

		off, err := tool.CommandLine(json.RawMessage(`{"trace":false}`))
		Expect(err).NotTo(HaveOccurred())
		Expect(off).To(Equal("ping --no-trace"))
	})

	It("Should surface flag completions as a schema enum", func() {
		app := fisk.New("nats", "nats app")
		app.Flag("context", "Configuration context").HintOptions("prod", "dev").String()
		app.Command("ping", "ping servers")

		tools, err := ApplicationTools(introspect(app), "context")
		Expect(err).NotTo(HaveOccurred())
		tool := toolsByName(tools)["ping"]

		props, _ := tool.InputSchema()["properties"].(map[string]any)
		ctx, _ := props["context"].(map[string]any)
		Expect(ctx).To(HaveKeyWithValue("enum", ConsistOf("prod", "dev")))
	})

	It("Should always expose a required global and mark it required in the schema", func() {
		app := fisk.New("nats", "nats app")
		app.Flag("account", "the account").Required().String()
		app.Command("ping", "ping servers")

		// Not listed under global_flags, yet exposed because it is required.
		tools, err := ApplicationTools(introspect(app))
		Expect(err).NotTo(HaveOccurred())
		tool := toolsByName(tools)["ping"]

		props, _ := tool.InputSchema()["properties"].(map[string]any)
		Expect(props).To(HaveKey("account"))
		Expect(toolkit.SchemaRequired(tool.InputSchema()["required"])).To(ContainElement("account"))
		Expect(tool.MissingRequired(json.RawMessage(`{}`))).To(ContainElement("account"))
	})

	It("Should keep the command's own argument when a global collides by name", func() {
		// fisk forbids a leaf flag that shadows a global flag, so the reachable
		// collision is a positional argument sharing the global's name.
		app := fisk.New("nats", "nats app")
		app.Flag("context", "global context").String()
		save := app.Command("save", "save a thing")
		save.Arg("context", "the local context").Required().String()

		tools, err := ApplicationTools(introspect(app), "context")
		Expect(err).NotTo(HaveOccurred())
		tool := toolsByName(tools)["save"]
		Expect(tool).NotTo(BeNil())
		Expect(tool.GlobalFlags).To(BeEmpty())

		// The local argument renders normally; the global is not double-added.
		cmdline, err := tool.CommandLine(json.RawMessage(`{"context":"local"}`))
		Expect(err).NotTo(HaveOccurred())
		Expect(cmdline).To(Equal("save -- local"))
	})

	It("Should error when an allowlisted name matches no global flag", func() {
		_, err := ApplicationTools(introspect(globalApp()), "contxt")
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring(`entry "contxt" matches no global flag`))
	})

	It("Should error when an allowlisted name is a framework flag", func() {
		// Introspection strips the framework flags (help, version, ...) from the
		// model, so they cannot be exposed and resolve as no such global flag.
		_, err := ApplicationTools(introspect(globalApp()), "help")
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring(`entry "help" matches no global flag`))
	})

	It("Should error when an allowlisted name is a hidden global", func() {
		app := fisk.New("nats", "nats app")
		app.Flag("secret", "a hidden global").Hidden().String()
		app.Command("ping", "ping servers")

		_, err := ApplicationTools(introspect(app), "secret")
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("hidden or framework flag"))
	})
})

var _ = Describe("Application-less config", func() {
	It("LoadTools should return no tools and not introspect when application_path is unset", func() {
		tools, err := LoadTools(&config.Config{})
		Expect(err).NotTo(HaveOccurred())
		Expect(tools).To(BeEmpty())
	})

	It("AppGlobalFlags should return nothing and not introspect when application_path is unset", func() {
		globals, err := AppGlobalFlags(&config.Config{})
		Expect(err).NotTo(HaveOccurred())
		Expect(globals).To(BeEmpty())
	})
})
