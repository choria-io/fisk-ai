//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package util

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/charmbracelet/glamour"
	"github.com/muesli/termenv"
	"golang.org/x/term"

	"github.com/choria-io/fisk-ai/config"
)

// PrintText renders an assistant turn for display, routing by block type rather
// than by the turn's position in the conversation.
//
// Thinking blocks carry the model's reasoning and go to stderr, each line
// prefixed to set them apart from the answer and the trace lines. Empty thinking
// blocks, which some models return when the reasoning itself is not surfaced, are
// skipped so they leave no stray markers.
//
// Text blocks are the model's prose. They are markdown, so they are rendered with
// glamour for readability (raw when the destination is piped or redirected). On a
// terminal turn the text is the answer and goes to stdout; on an intermediate
// turn it is a mid-conversation update and goes to stderr so it stays out of a
// piped result. Either way the turn's text blocks are concatenated and rendered
// once, so markdown spanning several blocks (a table, a fenced code block) is not
// split across separate renders.
func PrintText(resp *anthropic.Message, terminal, noColor bool) {
	for _, block := range resp.Content {
		thinking, ok := block.AsAny().(anthropic.ThinkingBlock)
		if !ok || thinking.Thinking == "" {
			continue
		}

		for _, line := range strings.Split(SanitizeForDisplay(thinking.Thinking), "\n") {
			if line == "" {
				fmt.Fprintln(os.Stderr)
				continue
			}
			fmt.Fprintf(os.Stderr, "💭 %s\n", line)
		}
	}

	var answer strings.Builder
	for _, block := range resp.Content {
		text, ok := block.AsAny().(anthropic.TextBlock)
		if !ok {
			continue
		}
		answer.WriteString(text.Text)
	}

	if answer.Len() == 0 {
		return
	}

	// Intermediate prose stays on stderr so a piped result carries only the final
	// answer; the leading blank line keeps either one clear of the preceding trace.
	out := os.Stdout
	if !terminal {
		out = os.Stderr
	}

	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(out, renderMarkdown(answer.String(), out, noColor))
}

// RenderAnswer formats markdown for display on stdout. See renderMarkdown for the
// rendering rules.
func RenderAnswer(md string, noColor bool) string {
	return renderMarkdown(md, os.Stdout, noColor)
}

// RenderMarkdownTo formats markdown for display on out, using out's own terminal
// detection so a replay written to stderr renders correctly. See renderMarkdown
// for the rendering rules.
func RenderMarkdownTo(md string, out *os.File, noColor bool) string {
	return renderMarkdown(md, out, noColor)
}

// renderMarkdown formats markdown for display on out. When out is a terminal it
// is rendered with glamour using a style matched to the terminal background and
// word wrapped to the terminal width; off a terminal (piped or redirected) the
// raw markdown is returned unchanged so the result is free of ANSI escape codes.
// Rendering is also skipped when noColor is set (the --no-color flag or the
// standard NO_COLOR environment variable). Any rendering failure falls back to
// raw.
func renderMarkdown(md string, out *os.File, noColor bool) string {
	// Strip any terminal escapes the model may have emitted before rendering, so a
	// raw escape in the model's prose cannot set a style that outlives this message
	// and bleeds into later output on the operator's terminal. The full-screen UI
	// sanitizes at its own render seam; this gives the line output the same guarantee.
	md = SanitizeForDisplay(md)

	if noColor || os.Getenv("NO_COLOR") != "" {
		return md
	}

	fd := int(out.Fd())
	if !term.IsTerminal(fd) {
		return md
	}

	opts := []glamour.TermRendererOption{glamour.WithAutoStyle(), glamour.WithEmoji()}

	// Match the word wrap to the terminal width so the output uses the full
	// screen rather than glamour's fixed 80 column default; glamour fits its
	// margins within this budget, so the rendered lines stay inside the width.
	// Fall back to that default when the width cannot be determined.
	width, _, err := term.GetSize(fd)
	if err == nil && width > 0 {
		opts = append(opts, glamour.WithWordWrap(width))
	}

	r, err := glamour.NewTermRenderer(opts...)
	if err != nil {
		return md
	}

	rendered, err := r.Render(md)
	if err != nil {
		return md
	}

	// glamour adds its own surrounding blank lines; trim them so the caller's
	// single newline controls the trailing spacing.
	return strings.Trim(rendered, "\n")
}

// minRenderWidth is the floor for markdown rendering. Below it glamour's word
// wrap produces mangled output, so a very narrow viewport is rendered as if this
// wide and left to scroll horizontally rather than corrupt the text.
const minRenderWidth = 20

// RenderMarkdownWidth renders markdown to ANSI wrapped at width, for display in
// the full-screen UI. Unlike renderMarkdown it never inspects a terminal: it forces
// an explicit style and color profile so nothing queries the tty (which the UI owns
// while its screen is held), making it safe to call under the alt-screen. noColor
// (or the NO_COLOR environment variable) selects the plain "notty" style, which
// still formats and wraps but emits no color. Any failure falls back to the raw
// markdown so the content is never lost.
func RenderMarkdownWidth(md string, width int, noColor bool) string {
	if width < minRenderWidth {
		width = minRenderWidth
	}

	style := "dark"
	profile := termenv.TrueColor
	if noColor || os.Getenv("NO_COLOR") != "" {
		style = "notty"
		profile = termenv.Ascii
	}

	r, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle(style),
		glamour.WithColorProfile(profile),
		glamour.WithWordWrap(width),
		glamour.WithEmoji(),
	)
	if err != nil {
		return md
	}

	out, err := r.Render(md)
	if err != nil {
		return md
	}

	// glamour surrounds its output with blank lines; trim them so the viewport
	// controls the spacing between entries.
	return strings.Trim(out, "\n")
}

// version is the build version reported by Version. It defaults to "devel" and is
// overridden at release time when main sets it from its own ldflags-injected value.
var version = "devel"

// Version reports the build version used for the MCP server identity, the splash card
// and the trace header. It returns the value main propagated via SetVersion, so a
// release stamps a short tag rather than the verbose module build info.
func Version() string {
	return version
}

// SetVersion sets the version reported by Version. main calls it once at startup with
// its ldflags-injected main.Version so every caller shares a single source. An empty
// value is ignored so the "devel" default survives an unset build.
func SetVersion(v string) {
	if v != "" {
		version = v
	}
}

// TruncateString shortens s to at most max characters, appending an
// ellipsis when anything was cut. It counts runes so multi-byte text is not
// split mid-character.
func TruncateString(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}

	return string(r[:max]) + "..."
}

// LoadTools introspects the application, then always strips ai:deny tools before
// applying the configured include and exclude filters. The unconditional first
// pass is what enforces ai:deny even when neither filter is set.
func LoadTools(cfg *config.Config) ([]*Tool, error) {
	// With no wrapped application there is nothing to introspect; the agent runs on
	// its built-in and remote tools alone.
	if cfg.ApplicationPath == "" {
		return nil, nil
	}

	tools, err := ToolsForApp(cfg.ApplicationPath, cfg.GlobalFlagNames()...)
	if err != nil {
		return nil, err
	}

	tools, err = FilterTools(tools, nil, IncludeFilter)
	if err != nil {
		return nil, err
	}

	if cfg.Include != nil && (len(cfg.Include.Tools) > 0 || len(cfg.Include.Tags) > 0) {
		tools, err = FilterTools(tools, cfg.Include, IncludeFilter)
		if err != nil {
			return nil, err
		}
	}

	if cfg.Exclude != nil && (len(cfg.Exclude.Tools) > 0 || len(cfg.Exclude.Tags) > 0) {
		tools, err = FilterTools(tools, cfg.Exclude, ExcludeFilter)
		if err != nil {
			return nil, err
		}
	}

	return tools, nil
}

// ServedTools returns the tools exposed to MCP and a2a callers: the agent's
// loaded tool set (LoadTools, which applies the top-level include/exclude and
// strips ai:deny) narrowed by the optional expose.agent.tools selection. The
// selection can only remove tools the agent already has; when it is absent the
// whole loaded set is served, so enabling a transport with no selection serves
// the agent's focused toolset as-is.
func ServedTools(cfg *config.Config) ([]*Tool, error) {
	tools, err := LoadTools(cfg)
	if err != nil {
		return nil, err
	}

	if cfg.Expose == nil || cfg.Expose.Agent == nil {
		return tools, nil
	}

	return filterExposed(tools, cfg.Expose.Agent.Tools)
}

// filterExposed narrows a loaded tool set by an exposure selection, applying its
// include then its exclude, each honored only when it carries patterns or tags,
// mirroring the top-level filtering in LoadTools. A nil selection serves the set
// unchanged.
func filterExposed(tools []*Tool, sel *config.ExposedToolSelection) ([]*Tool, error) {
	if sel == nil {
		return tools, nil
	}

	var err error
	if sel.Include != nil && (len(sel.Include.Tools) > 0 || len(sel.Include.Tags) > 0) {
		tools, err = FilterTools(tools, sel.Include, IncludeFilter)
		if err != nil {
			return nil, err
		}
	}

	if sel.Exclude != nil && (len(sel.Exclude.Tools) > 0 || len(sel.Exclude.Tags) > 0) {
		tools, err = FilterTools(tools, sel.Exclude, ExcludeFilter)
		if err != nil {
			return nil, err
		}
	}

	return tools, nil
}

// DumpJSONBody writes raw to out, pretty-printed if it is JSON.
func DumpJSONBody(out io.Writer, raw []byte) {
	var pretty bytes.Buffer
	if json.Indent(&pretty, raw, "", "  ") == nil {
		fmt.Fprintf(out, "%s\n", pretty.String())
		return
	}
	fmt.Fprintf(out, "%s\n", raw)
}

// HttpDebugMiddleware returns middleware that dumps the Anthropic API request and
// response bodies to out. Bodies are read non-destructively: the request via
// GetBody (the SDK sets it on retryable requests) and the response by buffering and
// replacing it, so the SDK still parses them normally. JSON bodies are pretty-printed.
// The sink is injected so a caller can direct the dump to a file rather than stderr,
// letting debugging coexist with the full-screen UI whose alt-screen stderr would
// otherwise be corrupted.
func HttpDebugMiddleware(out io.Writer) option.Middleware {
	return func(req *http.Request, next option.MiddlewareNext) (*http.Response, error) {
		fmt.Fprintf(out, "\n=== HTTP request: %s %s ===\n", req.Method, req.URL)
		if req.GetBody != nil {
			body, err := req.GetBody()
			if err == nil {
				raw, _ := io.ReadAll(body)
				body.Close()
				DumpJSONBody(out, raw)
			}
		}

		resp, err := next(req)
		if err != nil {
			return resp, err
		}

		raw, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return resp, fmt.Errorf("reading response body for debug: %w", err)
		}
		resp.Body = io.NopCloser(bytes.NewReader(raw))

		fmt.Fprintf(out, "\n=== HTTP response: %s ===\n", resp.Status)
		DumpJSONBody(out, raw)

		return resp, nil
	}
}
