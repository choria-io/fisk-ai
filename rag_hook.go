//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/choria-io/fisk"

	"github.com/choria-io/fisk-ai/internal/rag"
	"github.com/choria-io/fisk-ai/internal/rag/prompthook"
)

// knowledgeHookLabel pads every label of the preview trace to one width, so the
// values start in one column and two runs of a query line up under each other.
const knowledgeHookLabel = "%-8s%s\n"

var (
	knowledgeHookQuery     string
	knowledgeHookTopK      int
	knowledgeHookPerDoc    int
	knowledgeHookMinWords  int
	knowledgeHookAuto      bool
	knowledgeHookDebug     bool
	knowledgeHookLogfile   string
	knowledgeHookStopwords string
)

// userPromptSubmit is the part of the Claude Code UserPromptSubmit payload the hook
// reads. The event also carries a session id, a working directory and the event
// name, and Claude Code adds fields between releases, so the decode ignores every
// field it does not name.
type userPromptSubmit struct {
	Prompt string `json:"prompt"`
}

// registerKnowledgeHookFlags adds the tuning flags to one verb. Both verbs take the
// same set, so what an operator tunes with preview is what the hook then runs.
//
// Every flag carries an explicit default because fisk writes a flag's target only when
// the flag has one, and a process that parses twice would otherwise carry the first
// parse's value into the second.
func registerKnowledgeHookFlags(c *fisk.CmdClause) {
	c.Flag("top-k", fmt.Sprintf("Sections to ask the index for, clamped at %d; zero or less asks for the configured knowledge.top_k", rag.MaxTopK)).Default("8").IntVar(&knowledgeHookTopK)
	c.Flag("per-doc", "Most sections one document contributes to the block; zero or less lets one document fill it").Default("2").IntVar(&knowledgeHookPerDoc)
	c.Flag("min-words", "Fewest terms an untagged message must keep for its lookup to run; applies with --auto only").Default("3").IntVar(&knowledgeHookMinWords)
	c.Flag("auto", fmt.Sprintf("Look up messages carrying no <%s> tag", prompthook.TagName)).Default("false").UnNegatableBoolVar(&knowledgeHookAuto)
	c.Flag("debug", "Name on stderr why the hook wrote nothing, and add the search status and hit count to a preview").Default("false").UnNegatableBoolVar(&knowledgeHookDebug)
	c.Flag("logfile", "Append what every run looked up and produced to this file, created 0600; a relative path resolves against the directory you run from").Default("").StringVar(&knowledgeHookLogfile)
	c.Flag("stopwords", "Replace the built-in stopword list with the words in this file, one per line; a relative path resolves against the directory holding --config. Start one with: fisk rag agent stopwords").Default("").StringVar(&knowledgeHookStopwords)
}

// knowledgeHookOptions builds the pipeline options from the flags. configPath is the
// session's absolute path to the configuration, which the block's fetch instruction
// passes to rag agent show: the model runs that command from a directory this process
// never sees.
//
// It reads --stopwords, so the caller runs it after the session has changed into the
// directory holding --config and reports what it returns as a fault in the
// configuration.
func knowledgeHookOptions(configPath string) (prompthook.RunOptions, error) {
	stopwords, err := knowledgeHookStopwordList()
	if err != nil {
		return prompthook.RunOptions{}, err
	}

	return prompthook.RunOptions{
		Parse: prompthook.Options{
			Auto:      knowledgeHookAuto,
			MinWords:  knowledgeHookMinWords,
			Stopwords: stopwords,
		},
		TopK: knowledgeHookTopK,
		Block: prompthook.BlockOptions{
			BinaryPath: knowledgeHookBinary(),
			ConfigPath: configPath,
			PerDoc:     knowledgeHookPerDoc,
		},
	}, nil
}

// knowledgeHookBinary is the absolute path to the running binary, which the fetch
// instruction names. A hook runs under a login shell's environment rather than the
// one the operator tuned, so a bare name can reach a different binary or none.
// The empty string leaves prompthook.Block to render the bare name, which reaches
// this binary through PATH wherever it is on it.
func knowledgeHookBinary() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}

	return exe
}

func knowledgeAgentHookAction(pc *fisk.ParseContext) error {
	return knowledgeHook(pc, os.Stdin, os.Stdout, os.Stderr)
}

// knowledgeHook reads one UserPromptSubmit payload from in, runs the message through
// the pipeline and writes the block to out. Claude Code adds whatever the hook wrote
// to stdout to the message's context when the hook exits 0.
//
// A run with nothing to add exits 0 having written nothing, and names its reason on
// stderr only under --debug. Every gate lands here, and so does everything about the
// index: one nobody has built, one that will not open, one whose format the binary
// has outgrown, and a search that fails. A person mid-sentence can act on none of
// them, a reindex fixes most of them, and an error line on every message until
// someone does costs that person a line of each answer they asked for.
//
// A fault in the configuration file is returned as an error, which fisk prints on
// stderr before exiting 1: a --config that is missing, unparseable, or carrying no
// harness.knowledge block, and a --logfile that will not open or will not take an
// entry. A hook that has never once run should not read as one with nothing to say.
// Exit 1 rather than 2: on UserPromptSubmit, exit 2 blocks the message and discards
// it, so a mistyped --config would cost a person the message they were sending, where
// every other non-zero exit shows the text and sends the message anyway. The hook
// names itself in the message, since a transcript may be the only place the operator
// ever reads it.
func knowledgeHook(pc *fisk.ParseContext, in io.Reader, out io.Writer, stderr io.Writer) error {
	start := time.Now()

	ctx, cancel := interruptContext()
	defer cancel()

	// Opened before the payload is read and before the group changes directory, so a
	// relative path names a file where the operator ran the command and a path that
	// will not open is reported whatever else the run goes on to find.
	logfile, err := knowledgeHookOpenLog()
	if err != nil {
		return fmt.Errorf("the knowledge prompt hook cannot record its runs: %w", err)
	}
	defer logfile.Close()

	configPath := knowledgeHookConfigPath(pc)

	// Every fault from here on is recorded. An index that will not open is silent
	// everywhere else, and an operator asks for a logfile to find out whether the hook
	// runs at all; an empty file is the one answer it must not give. A run records the
	// files it named and the message it read, and a fault carries what it has: stdin
	// that held no message leaves the entry without a prompt line, and the fault says
	// why.
	prompt, payloadErr := knowledgeHookPayload(in)
	if payloadErr != nil {
		err = knowledgeHookFault(logfile, "hook", configPath, "", payloadErr, start)
		if err != nil {
			return fmt.Errorf("the knowledge prompt hook cannot record its runs: %w", err)
		}

		return payloadErr
	}

	session, openErr := openKnowledgeAgent(pc)
	if openErr != nil {
		err = knowledgeHookFault(logfile, "hook", configPath, prompt, openErr, start)
		if err != nil {
			return fmt.Errorf("the knowledge prompt hook cannot record its runs: %w", err)
		}

		if errors.Is(openErr, errKnowledgeIndexOpen) {
			return knowledgeHookSilence(stderr, openErr.Error())
		}

		return fmt.Errorf("the knowledge prompt hook cannot run: %w", openErr)
	}
	defer session.Close()

	opts, optsErr := knowledgeHookOptions(session.configPath)
	if optsErr != nil {
		err = knowledgeHookFault(logfile, "hook", session.configPath, prompt, optsErr, start)
		if err != nil {
			return fmt.Errorf("the knowledge prompt hook cannot record its runs: %w", err)
		}

		return fmt.Errorf("the knowledge prompt hook cannot run: %w", optsErr)
	}

	res, runErr := prompthook.Run(ctx, session.store, prompt, opts)

	// Recorded before the block reaches stdout, so a logfile that cannot be written
	// ends the run rather than reporting a fault over a block Claude Code has already
	// taken. The entry reads the store while the session still holds it open.
	err = logfile.record(knowledgeHookEntry("hook", session, prompt, res, runErr, start))
	if err != nil {
		return fmt.Errorf("the knowledge prompt hook cannot record its runs: %w", err)
	}

	if runErr != nil {
		return knowledgeHookSilence(stderr, fmt.Sprintf("the knowledge index cannot be searched: %s", runErr))
	}

	reason := knowledgeHookReason(res)
	if reason != "" {
		return knowledgeHookSilence(stderr, reason)
	}

	fmt.Fprintln(out, res.Block)

	return nil
}

// knowledgeHookSilence ends a hook run that adds nothing to the message, writing the
// reason to stderr under --debug and returning the nil that exits 0.
func knowledgeHookSilence(stderr io.Writer, reason string) error {
	if knowledgeHookDebug {
		fmt.Fprintf(stderr, "knowledge hook: %s\n", reason)
	}

	return nil
}

// knowledgeHookLog is the open logfile and the path an error about it names. A nil
// pointer records nothing, which is a command run without --logfile.
type knowledgeHookLog struct {
	to   io.Writer
	path string
	file *os.File
}

// knowledgeHookOpenLog opens --logfile for appending, and returns nil when the operator
// asked for no logfile. A relative path resolves against the directory the command
// started in, so it must be opened before openKnowledgeAgent changes into the directory
// holding the configuration; a path resolved after that change would put the file in
// the corpus. The path is made absolute so an error about the file names the file it
// opened rather than a relative path read against a directory the operator has left.
//
// The file is created 0600, since it holds messages as they were typed. A file that is
// already there keeps the mode it has.
func knowledgeHookOpenLog() (*knowledgeHookLog, error) {
	if knowledgeHookLogfile == "" {
		return nil, nil
	}

	path, err := filepath.Abs(knowledgeHookLogfile)
	if err != nil {
		return nil, fmt.Errorf("--logfile %q cannot be resolved: %w", knowledgeHookLogfile, err)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("--logfile %s cannot be opened: %w", path, err)
	}

	return &knowledgeHookLog{to: f, path: path, file: f}, nil
}

// Close closes the logfile, and a run that opened none closes nothing.
func (l *knowledgeHookLog) Close() error {
	if l == nil || l.file == nil {
		return nil
	}

	return l.file.Close()
}

// record appends one entry, in a single write so two hooks running at once interleave
// entries rather than lines.
//
// A message carrying no tag while --auto is off is passed over. The hook fires on every
// message a person sends, so recording those would make the file a transcript of
// everything typed at Claude with one useful entry in fifty. Every other run is
// recorded, gates and faults included, since a gate firing correctly is most of what
// confirms a hook works.
func (l *knowledgeHookLog) record(e prompthook.LogEntry) error {
	if l == nil {
		return nil
	}
	if e.Result.Decision.Skip == prompthook.SkipNoTag {
		return nil
	}

	_, err := io.WriteString(l.to, e.String())
	if err != nil {
		return fmt.Errorf("--logfile %s cannot be appended to: %w", l.path, err)
	}

	return nil
}

// knowledgeHookFault records a run that failed before it reached the pipeline, naming
// the files it had resolved and the fault that ended it. It reports what the logfile
// itself raised, leaving the caller to report the fault its own way: the hook passes an
// index it cannot open over in silence and says the rest out loud.
func knowledgeHookFault(l *knowledgeHookLog, verb string, configPath string, prompt string, cause error, start time.Time) error {
	return l.record(prompthook.LogEntry{
		Time:       start,
		Verb:       verb,
		ConfigPath: configPath,
		Prompt:     prompt,
		Elapsed:    time.Since(start),
		Err:        cause,
	})
}

// knowledgeHookPayload reads one UserPromptSubmit payload from in and returns the
// message it carries.
//
// The payload decodes into a pointer, so the JSON null that leaves every field of a
// value untouched is refused the way any other payload that is not an object is.
func knowledgeHookPayload(in io.Reader) (string, error) {
	var payload *userPromptSubmit

	err := json.NewDecoder(in).Decode(&payload)
	if errors.Is(err, io.EOF) {
		return "", fmt.Errorf("the knowledge prompt hook read nothing on stdin; it takes the UserPromptSubmit payload Claude Code writes there")
	}
	if err != nil {
		return "", fmt.Errorf("the knowledge prompt hook cannot read the UserPromptSubmit payload on stdin: %w", err)
	}
	if payload == nil {
		return "", fmt.Errorf("the knowledge prompt hook read a null payload on stdin; it takes the UserPromptSubmit object Claude Code writes there")
	}

	return payload.Prompt, nil
}

// knowledgeHookEntry records a run that reached the pipeline. It reads the store and
// the configuration while the session still holds them, so rag.StorePath resolves a
// relative knowledge.directory against the corpus rather than against the directory
// the command started in.
func knowledgeHookEntry(verb string, session *knowledgeAgentSession, prompt string, res prompthook.Result, runErr error, start time.Time) prompthook.LogEntry {
	return prompthook.LogEntry{
		Time:          start,
		Verb:          verb,
		ConfigPath:    session.configPath,
		StorePath:     knowledgeHookStorePath(session),
		Prompt:        prompt,
		Result:        res,
		PerDoc:        knowledgeHookPerDoc,
		VectorEnabled: session.store.VectorEnabled(),
		Elapsed:       time.Since(start),
		Err:           runErr,
	}
}

// knowledgeHookConfigPath resolves --config the way openKnowledgeAgent does, for an
// entry recording a run that failed before the session could hand its own path back.
// A run given no --config names no file, and the fault it carries says so.
func knowledgeHookConfigPath(pc *fisk.ParseContext) string {
	if !flagWasSet(pc, "config") {
		return ""
	}

	path, err := filepath.Abs(configFile)
	if err != nil {
		return configFile
	}

	return path
}

// knowledgeHookStorePath renders the index file as an absolute path. rag.StorePath
// resolves a relative knowledge.directory against the working directory, which is the
// configuration's own directory while the session is open.
func knowledgeHookStorePath(session *knowledgeAgentSession) string {
	path := rag.StorePath(session.cfg, knowledgeStoreDir)

	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}

	return abs
}

func knowledgeAgentPreviewAction(pc *fisk.ParseContext) error {
	return knowledgePreview(pc, os.Stdout)
}

// knowledgePreview runs the query through the same pipeline the hook runs and writes
// what it made of it to out, then the block. A person ran it and is waiting on it, so
// every fault the hook leaves to --debug is returned here as an error.
//
// The trace goes to stdout with the block, because a person tuning a stopword list
// reads the two together and a split across two streams reorders them under a pager
// or a redirect. It names the mode, the terms that survived the filter and, where no
// block was built, the gate or the empty ranking that stopped it. A preview printing
// the block alone leaves a person to guess why the block came back empty.
func knowledgePreview(pc *fisk.ParseContext, out io.Writer) error {
	start := time.Now()

	ctx, cancel := interruptContext()
	defer cancel()

	logfile, err := knowledgeHookOpenLog()
	if err != nil {
		return err
	}
	defer logfile.Close()

	session, openErr := openKnowledgeAgent(pc)
	if openErr != nil {
		err = knowledgeHookFault(logfile, "preview", knowledgeHookConfigPath(pc), knowledgeHookQuery, openErr, start)
		if err != nil {
			return err
		}

		return openErr
	}
	defer session.Close()

	opts, optsErr := knowledgeHookOptions(session.configPath)
	if optsErr != nil {
		err = knowledgeHookFault(logfile, "preview", session.configPath, knowledgeHookQuery, optsErr, start)
		if err != nil {
			return err
		}

		return optsErr
	}

	res, runErr := prompthook.Run(ctx, session.store, knowledgeHookQuery, opts)

	err = logfile.record(knowledgeHookEntry("preview", session, knowledgeHookQuery, res, runErr, start))
	if err != nil {
		return err
	}

	if runErr != nil {
		return runErr
	}

	terms := strings.Join(res.Decision.Terms, " ")
	if terms == "" {
		terms = "(none)"
	}

	fmt.Fprintf(out, knowledgeHookLabel, "mode:", knowledgeHookModeName(res.Decision.Mode))
	fmt.Fprintf(out, knowledgeHookLabel, "terms:", terms)

	if knowledgeHookDebug && res.Search != nil {
		fmt.Fprintf(out, knowledgeHookLabel, "search:", fmt.Sprintf("%s, %d hits before the per-document cap", res.Search.Status, len(res.Search.Hits)))
	}

	reason := knowledgeHookReason(res)
	if reason != "" {
		label := "result:"
		if res.Outcome() == prompthook.OutcomeSkipped {
			label = "gate:"
		}

		fmt.Fprintf(out, knowledgeHookLabel, label, reason)

		return nil
	}

	fmt.Fprintln(out)
	fmt.Fprintln(out, res.Block)

	return nil
}

// knowledgeHookModeName names the mode for a person reading a terminal, spelling the
// zero value a message that asked for nothing carries as "none".
func knowledgeHookModeName(m prompthook.Mode) string {
	if m == prompthook.ModeNone {
		return "none"
	}

	return string(m)
}

// knowledgeHookReason says in a sentence why a run built no block, and returns the
// empty string for a run that built one. The hook prints it under --debug and preview
// prints it where the block would be, so both name a gate the same way and an
// operator tuning against preview reads what the hook decided.
func knowledgeHookReason(res prompthook.Result) string {
	switch res.Outcome() {
	case prompthook.OutcomeBlock:
		return ""

	case prompthook.OutcomeIndexNotBuilt:
		return "the knowledge index has not been built yet; run: fisk knowledge index"

	case prompthook.OutcomeIndexEmpty:
		return "the knowledge index is empty, or it holds none of these terms"

	case prompthook.OutcomeNoHits:
		return "nothing ranked against these terms"

	case prompthook.OutcomeNoSearch:
		return "the index answered the search with nothing at all"
	}

	// What is left is OutcomeSkipped, which the gate that fired words.
	switch res.Decision.Skip {
	case prompthook.SkipEmptyPrompt:
		return "the message is empty, so no lookup ran"

	case prompthook.SkipNoTag:
		return fmt.Sprintf("the message carries no <%s> tag and --auto is off, so no lookup ran", prompthook.TagName)

	case prompthook.SkipSlashCommand:
		return "the message is a slash command, so no lookup ran"

	case prompthook.SkipNoTerms:
		return "every word is a stopword or too short to be indexed, so no lookup ran"

	case prompthook.SkipTooFewWords:
		return fmt.Sprintf("--min-words is %d and the message kept %d, so no lookup ran", knowledgeHookMinWords, len(res.Decision.Terms))
	}

	return string(res.Decision.Skip)
}
