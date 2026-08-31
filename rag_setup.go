//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/choria-io/fisk"

	"github.com/choria-io/fisk-ai/internal/rag"
	"github.com/choria-io/fisk-ai/internal/rag/prompthook"
)

// knowledgeSetupTimeout is the seconds the installed entry gives one run of the
// hook. Claude Code holds the message until the hook exits or the timeout fires, so
// a person waits this long on an index that is slow to answer. A search over a local
// index answers in milliseconds and the vector tier adds one call to an embeddings
// server, which ten seconds covers from cold.
const knowledgeSetupTimeout = 10

// knowledgeSetupSettings is the settings.json shape the emitted prompt carries. The
// fragment is marshaled from these types rather than written out as text, so the
// command line reaches the model correctly escaped whatever characters a path holds.
//
// A UserPromptSubmit group takes no matcher: matchers select tools, and this event
// carries none.
type knowledgeSetupSettings struct {
	Hooks knowledgeSetupEvents `json:"hooks"`
}

type knowledgeSetupEvents struct {
	UserPromptSubmit []knowledgeSetupGroup `json:"UserPromptSubmit"`
}

type knowledgeSetupGroup struct {
	Hooks []knowledgeSetupCommand `json:"hooks"`
}

type knowledgeSetupCommand struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout"`
}

func knowledgeAgentSetupAction(pc *fisk.ParseContext) error {
	return knowledgeSetup(pc, os.Stdout, os.Stderr)
}

// knowledgeSetup writes the installation prompt to out.
//
// The prompt is printed rather than performed. The target file belongs to the person
// running this, it may hold hooks and settings fisk knows nothing about, and a person
// handed the text reads what is about to happen to it before any of it happens.
//
// The configuration is opened through openKnowledgeAgentConfig, which resolves --config
// to an absolute path, parses the file and checks that harness.knowledge is enabled. A
// hook installed against a configuration failing either of the last two exits 1 on every
// message the person sends, so setup exits 1 on them now.
//
// The store is left shut. An index that will not open is the fault the hook is built to
// swallow: errKnowledgeIndexOpen routes to silence and the hook exits 0, because a
// reindex fixes it and a person mid-sentence can act on none of it. Opening the store
// here would refuse to print the prompt for a corpus whose hook runs. An index nobody
// has built reads the same way and is found by rag.StoreExists, which stats the file
// rather than opening it. Both are named on stderr, which keeps them out of the text
// the person pastes into Claude.
func knowledgeSetup(pc *fisk.ParseContext, out io.Writer, stderr io.Writer) error {
	// Resolved before the session changes into the corpus, so a relative --logfile names
	// a file where the operator ran the command, which is where knowledgeHookOpenLog
	// resolves it too. --stopwords resolves the other way and is read below.
	logfile, err := knowledgeSetupLogfile()
	if err != nil {
		return err
	}

	session, err := openKnowledgeAgentConfig(pc)
	if err != nil {
		return err
	}
	defer session.Close()

	stopwords, err := knowledgeSetupStopwords()
	if err != nil {
		return err
	}

	built, err := rag.StoreExists(session.cfg, knowledgeStoreDir)
	if err != nil {
		return err
	}

	if !built {
		fmt.Fprintf(stderr, "The knowledge index at %s has not been built yet, so the hook adds nothing to a message until you run: fisk knowledge index --config %s\n\n", knowledgeHookStorePath(session), session.configPath)
	}

	command, anchor := knowledgeSetupHookCommand(pc, session.configPath, logfile, stopwords)

	prompt, err := knowledgeSetupPrompt(command, anchor)
	if err != nil {
		return err
	}

	fmt.Fprint(out, prompt)

	return nil
}

// knowledgeSetupLogfile resolves --logfile to an absolute path, and returns the empty
// string for a command that asked for no logfile. The installed hook runs from
// whatever directory Claude Code is working in, so a relative path there would put the
// file somewhere neither the operator nor this command names.
func knowledgeSetupLogfile() (string, error) {
	if knowledgeHookLogfile == "" {
		return "", nil
	}

	path, err := filepath.Abs(knowledgeHookLogfile)
	if err != nil {
		return "", fmt.Errorf("--logfile %q cannot be resolved: %w", knowledgeHookLogfile, err)
	}

	return path, nil
}

// knowledgeSetupStopwords resolves --stopwords to an absolute path, and returns the
// empty string for a command that named no file. It runs after the session has changed
// into the directory holding --config, because knowledgeHookStopwordList reads the flag
// from there: a relative path names a file beside the configuration, and resolving it
// anywhere else would write a path into the settings file naming a different file or
// none.
func knowledgeSetupStopwords() (string, error) {
	if knowledgeHookStopwords == "" {
		return "", nil
	}

	path, err := filepath.Abs(knowledgeHookStopwords)
	if err != nil {
		return "", fmt.Errorf("--stopwords %q cannot be resolved: %w", knowledgeHookStopwords, err)
	}

	return path, nil
}

// knowledgeSetupHookCommand renders the command line the installed entry runs.
//
// The binary is named by its own absolute path. A hook runs under a login shell's
// environment rather than the one the operator tuned, and a bare name that is not on
// that PATH fails with nothing to point at.
//
// The flags divide two ways. --config, --store-dir, --logfile and --stopwords locate
// files, and each one reaches the line whenever it holds a value, absolute. --store-dir
// also reads FISK_AI_STORE_DIR, and a value that arrived that way is written out here
// too, since the environment a hook runs under is the one this command cannot trust. An
// index the operator built under a --store-dir the line omits is an index the hook never
// reads, and the hook passes a missing index over in silence, so the operator would get
// nothing on every message and an error nowhere.
//
// --top-k, --per-doc, --min-words, --auto and --debug tune a lookup, and one reaches the
// line only where the operator typed it. They carry defaults, so passing them all would
// write today's defaults into a file installed once and read for months, and a later
// change to a default would never reach it.
//
// It returns the whole line and the prefix naming this binary and this corpus, which the
// prompt gives the model to match an already installed entry on. A tuning flag changes
// the line and leaves the prefix alone, so a re-run with different tuning replaces the
// entry for this corpus rather than installing a second one beside it.
func knowledgeSetupHookCommand(pc *fisk.ParseContext, configPath string, logfile string, stopwords string) (string, string) {
	args := []string{knowledgeSetupBinary(), "rag", "agent", "hook", "--config", configPath}

	// Everything that identifies the corpus, which is every argument the prefix covers.
	const anchorArgs = 6

	if knowledgeStoreDir != "" {
		args = append(args, "--store-dir", knowledgeStoreDir)
	}
	if logfile != "" {
		args = append(args, "--logfile", logfile)
	}
	if stopwords != "" {
		args = append(args, "--stopwords", stopwords)
	}
	if flagWasSet(pc, "auto") {
		args = append(args, "--auto")
	}
	if flagWasSet(pc, "debug") {
		args = append(args, "--debug")
	}
	if flagWasSet(pc, "top-k") {
		args = append(args, "--top-k", strconv.Itoa(knowledgeHookTopK))
	}
	if flagWasSet(pc, "per-doc") {
		args = append(args, "--per-doc", strconv.Itoa(knowledgeHookPerDoc))
	}
	if flagWasSet(pc, "min-words") {
		args = append(args, "--min-words", strconv.Itoa(knowledgeHookMinWords))
	}

	for i, arg := range args {
		args[i] = prompthook.ShellWord(arg)
	}

	return strings.Join(args, " "), strings.Join(args[:anchorArgs], " ")
}

// knowledgeSetupBinary is the absolute path to the running binary, falling back to the
// bare name for a platform that will not report one. The bare name reaches this binary
// through PATH wherever it is on it.
func knowledgeSetupBinary() string {
	exe := knowledgeHookBinary()
	if exe == "" {
		return "fisk"
	}

	return exe
}

// knowledgeSetupFragment renders the settings.json object the prompt carries, indented
// two spaces as a person writes one by hand.
func knowledgeSetupFragment(command string) (string, error) {
	settings := knowledgeSetupSettings{
		Hooks: knowledgeSetupEvents{
			UserPromptSubmit: []knowledgeSetupGroup{{
				Hooks: []knowledgeSetupCommand{{
					Type:    "command",
					Command: command,
					Timeout: knowledgeSetupTimeout,
				}},
			}},
		},
	}

	body, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return "", err
	}

	return string(body), nil
}

// knowledgeSetupPrompt renders the whole prompt.
//
// It is written for a model that will read a JSON file and edit it. The target may hold
// no file at all, a file with no hooks key, a hooks key carrying other events, a
// UserPromptSubmit array with entries in it, and settings that have nothing to do with
// hooks, so the prompt states each of those cases and then forbids touching anything
// else. That last instruction stops a rewrite of the whole file, which is how an
// unrelated setting gets reformatted or lost.
//
// An entry already installed is matched on anchor rather than on the whole command,
// which changes whenever a tuning flag changes or the binary moves. Matching the whole
// string would install a second hook for the corpus on a re-run with different tuning,
// and would leave an entry naming a binary a go install has moved, whose failure Claude
// Code reports on every message.
//
// The project file is named first. The command names one corpus by an absolute path,
// so the user file fires that corpus's lookup in every project the person opens,
// including ones the corpus knows nothing about. The local file is named for a
// repository whose .claude/settings.json is committed, since the absolute paths here
// are this machine's.
//
// The tag is rendered through prompthook.TagName, the constant the pattern Parse matches
// with and the block's own preamble line are built from.
func knowledgeSetupPrompt(command string, anchor string) (string, error) {
	fragment, err := knowledgeSetupFragment(command)
	if err != nil {
		return "", err
	}

	var b strings.Builder

	fmt.Fprintf(&b, `Add the fisk knowledge prompt hook to my Claude Code settings.

The hook runs on every message I send you. It looks the message up in a fisk knowledge
index and adds the sections that rank against it to your context.

1. Pick the settings file. The command below names one corpus by an absolute path, so
   put the hook in this project's .claude/settings.json when that corpus is this
   project's own. Put it in ~/.claude/settings.json when I want this corpus searched
   from every project I open. Write .claude/settings.local.json instead when this
   repository commits .claude/settings.json, because the paths below are this machine's.

2. Read the file before you write it. Create it holding exactly the object below when
   there is no file. Add the hooks key holding that object's hooks value when the file
   is there and has no hooks key.

3. Merge into what the file already holds. Keep every key it has, other hook events and
   settings that are not hooks included. Append this entry to hooks.UserPromptSubmit
   where that array already exists, leaving the entries in it as they are.

4. Replace rather than add where this corpus already has an entry. An entry whose command
   starts with

   %[3]s

   installs this same corpus, whatever flags follow it. Overwrite that entry with the one
   below instead of appending a second, and do this even where the rest of the command
   differs, since the flags change between runs and a moved binary changes the path.

5. Leave the rest of the file alone. Do not reorder, reformat or rewrite anything you
   did not add.

6. Run /hooks and show me that UserPromptSubmit lists the command.

`+"```json\n%[1]s\n```"+`

Then tell me in your own words what you have installed:

- A message carrying a <%[2]s> tag is looked up in the index, and the rest of the message
  is the query.
- A message carrying <%[2]s word word> is looked up on those words alone, and the rest of
  the message stays out of the query.
- A message carrying no tag is passed over unless the command above carries --auto.
- What comes back is a block of section titles and citations rather than the text of
  those sections.
- You read one of those sections by running the command the block spells out, which is
  fisk rag agent show with a citation from the block.
`, fragment, prompthook.TagName, anchor)

	return b.String(), nil
}
