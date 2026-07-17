//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/choria-io/fisk"
	"github.com/choria-io/fisk-ai/config"
	"github.com/choria-io/fisk-ai/internal/remotetools"
	"github.com/choria-io/fisk-ai/internal/util"
	"github.com/jedib0t/go-pretty/v6/table"
)

// maxInfoDescriptionLen is the width at which tool descriptions are truncated in
// the info table before an ellipsis is appended.
const maxInfoDescriptionLen = 50

func registerInfoAction(cmd *fisk.Application) {
	info := cmd.Command("info", "Shows the tools and prompt loaded from a configuration").Action(infoAction)
	info.Flag("config", "Path to the agent configuration file").Default("agent.yaml").ExistingFileVar(&configFile)
	info.Flag("no-color", "Disable markdown rendering of the prompt, emitting raw text").Envar("NO_COLOR").UnNegatableBoolVar(&noColor)
}

// infoAction reports, without contacting the LLM, the tools that the
// configuration resolves to and the system prompt that would be sent.
//
// The config is validated in ModeMCP, the most lenient mode: info introspects a
// configuration without running it, so it must work for an MCP-only config that
// carries no prompt or model as well as for a full agent config. Requiring a
// model or prompt here, as ModeAgent does, would reject a valid MCP config it is
// meant to inspect.
func infoAction(_ *fisk.ParseContext) error {
	cfg, err := config.ParseConfigFileForMode(configFile, config.ModeMCP)
	if err != nil {
		return err
	}

	if cfg.ApplicationPath == "" && cfg.AppToolFiltersConfigured() {
		fmt.Fprintln(os.Stderr, "warning: include/exclude have no effect without application_path; they filter the wrapped application's tools")
	}

	tools, err := util.LoadTools(cfg)
	if err != nil {
		return err
	}

	// Names already claimed by local tools and the built-ins, so remote tools are
	// named (and prefixed on clash) exactly as a run would name them.
	taken := make(map[string]bool, len(tools))
	for _, t := range tools {
		taken[t.Name()] = true
	}
	for _, b := range util.HITLTools(cfg) {
		taken[b.Name()] = true
	}
	// The memory tools are enumerated with a nil store: info only needs their names
	// and descriptions, and never invokes a handler.
	for _, b := range util.MemoryTools(cfg, nil) {
		taken[b.Name()] = true
	}
	// The knowledge_search tool is likewise enumerated with a nil store.
	for _, b := range util.RAGTools(cfg, nil) {
		taken[b.Name()] = true
	}

	// Discover remote tools best-effort: info must stay usable offline and when a
	// remote agent is down, so a connection or discovery failure is reported as a
	// warning and the local tools are still shown.
	imports, err := remotetools.DiscoverForInfo(cfg, taken)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: cannot connect to NATS context %q to discover remote tools: %v\n", cfg.NatsContext, err)
	}

	tbl := table.NewWriter()
	tbl.SetOutputMirror(os.Stdout)
	tbl.SetStyle(table.StyleRounded)
	tbl.SuppressTrailingSpaces()
	tbl.AppendHeader(table.Row{"Tool", "Source", "Confirm", "Description", "Tags"})
	// The Confirm column marks the commands a run would gate behind operator
	// confirmation, so an author can see confirm_tags resolves to the commands they
	// expect rather than discovering a typo (an unmatched tag) only mid-run. Only the
	// introspected local tools carry tags; the built-ins and remote tools are not gated
	// here, so their cell stays blank.
	for _, t := range tools {
		confirm := ""
		if t.NeedsConfirm(cfg.ConfirmTags()) {
			confirm = "Yes"
		}
		tbl.AppendRow(table.Row{t.Name(), "local", confirm, util.TruncateString(t.Description(), maxInfoDescriptionLen), strings.Join(t.Tags(), ", ")})
	}
	// Built-in human-in-the-loop tools are not introspected from the application,
	// so list them too when enabled, to show the full tool set a run would expose.
	// They carry no tags.
	for _, b := range util.HITLTools(cfg) {
		tbl.AppendRow(table.Row{b.Name(), "local", "", util.TruncateString(b.Description(), maxInfoDescriptionLen), ""})
	}
	// Built-in memory tools are likewise not introspected from the application, so
	// list them when enabled to show the full tool set a run would expose.
	for _, b := range util.MemoryTools(cfg, nil) {
		tbl.AppendRow(table.Row{b.Name(), "local", "", util.TruncateString(b.Description(), maxInfoDescriptionLen), ""})
	}
	// The built-in knowledge_search tool, likewise, when RAG is enabled.
	for _, b := range util.RAGTools(cfg, nil) {
		tbl.AppendRow(table.Row{b.Name(), "local", "", util.TruncateString(b.Description(), maxInfoDescriptionLen), ""})
	}
	// Imported remote tools are listed with the host alias as their source, so the
	// provenance of a tool the prompt may reference is clear.
	for _, imp := range imports {
		alias := imp.Host.EffectiveAlias()
		for _, rt := range imp.Tools {
			// A remote tool's description is the serving agent's model-facing
			// description, which has the command's tags appended as a trailing
			// "Tags:" block. Split that back out so the table shows a clean,
			// single-line description and the tags in their own column, matching the
			// local rows.
			desc, tags := splitRemoteDescription(rt.Description())
			desc = strings.ReplaceAll(desc, "\n", " ")
			tbl.AppendRow(table.Row{rt.Name(), alias, "", util.TruncateString(desc, maxInfoDescriptionLen), tags})
		}
	}
	tbl.Render()

	if cfg.ApplicationPath == "" {
		fmt.Println()
		fmt.Println("No wrapped application configured (application_path unset); built-in and remote tools only.")
	}

	printRemoteToolStatus(cfg, imports)

	// List the application's exposable global flags so an operator can see which
	// exist and which they have allowlisted under global_flags, closing the loop
	// between "what can I expose" and the error a bad name would raise at load.
	globals, err := util.AppGlobalFlags(cfg)
	if err != nil {
		return err
	}
	if len(globals) > 0 {
		fmt.Println()
		fmt.Println("Exposable application global flags (add names under global_flags to expose to the model):")
		keys := make([]string, len(globals))
		width := 0
		for i, g := range globals {
			marker := ""
			switch {
			case g.Required:
				marker = " [exposed, required]"
			case g.Exposed:
				marker = " [exposed]"
			}
			keys[i] = g.Name + marker
			if len(keys[i]) > width {
				width = len(keys[i])
			}
		}
		fmt.Println()
		for i, g := range globals {
			fmt.Printf("  %*s: %s\n", width, keys[i], util.TruncateString(g.Help, maxInfoDescriptionLen))
		}
	}

	fmt.Println()
	fmt.Println("Prompt:")
	fmt.Println()
	fmt.Println(util.RenderAnswer(cfg.SystemPrompt, noColor))

	fmt.Println()
	fmt.Printf("These tools can also be served over MCP with: fisk-ai mcp --config %s\n", configFile)

	return nil
}

// splitRemoteDescription separates a remote tool's advertised description into its
// human-facing text and its tag list. A serving agent advertises the model-facing
// description, which is the command help followed by a "\n\nTags: ..." block (or,
// when the help is empty, just that block). This recovers the two parts for
// display so a remote row matches a local one: clean description, tags column. A
// description with no tag block is returned unchanged with empty tags.
func splitRemoteDescription(s string) (desc string, tags string) {
	const sep = "\n\nTags: "
	if idx := strings.LastIndex(s, sep); idx >= 0 {
		return s[:idx], s[idx+len(sep):]
	}

	const prefix = "Tags: "
	if strings.HasPrefix(s, prefix) {
		return "", strings.TrimPrefix(s, prefix)
	}

	return s, ""
}
