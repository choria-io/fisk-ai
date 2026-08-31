//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/choria-io/fisk"

	"github.com/choria-io/fisk-ai/config"
	"github.com/choria-io/fisk-ai/internal/rag"
	"github.com/choria-io/fisk-ai/internal/rag/prompthook"
)

// knowledgeAgentMaxSpan is the widest range one citation may cover. The block
// cites a run of sections that ranked together and one search returns at most
// twenty chunks, so a run it emits is a handful; a person reading a long section
// by hand has room here too. The cap costs one comparison and stops a mistyped
// ordinal from reading a corpus one query at a time: the read is linear in the
// span, so a nine-digit ordinal runs for hours.
const knowledgeAgentMaxSpan = 64

var knowledgeAgentCitation string

// errKnowledgeIndexOpen marks the one failure openKnowledgeAgent reports that is
// about the index rather than the configuration file. The prompt hook answers the
// two differently: a configuration it cannot read has never worked and is said out
// loud, where an index it cannot open is the same fault as one nobody has built and
// is left to --debug.
var errKnowledgeIndexOpen = errors.New("the knowledge index cannot be opened")

// registerKnowledgeAgentCommand adds the agent group to the knowledge command,
// which puts its verbs at "fisk knowledge agent" and, through the knowledge
// aliases, at "fisk rag agent". The knowledge block injected into a model's
// context names the second spelling, so the model and an operator debugging the
// block run the same command.
func registerKnowledgeAgentCommand(k *fisk.CmdClause) {
	agent := k.Command("agent", "Commands the knowledge block injected into a model's context tells it to run")

	show := agent.Command("show", "Prints the chunks a citation names, one ordinal or a range of them").Action(knowledgeAgentShowAction)
	show.Arg("citation", "A citation token of the form <relpath>#<ordinal> or <relpath>#<first>-<last>").Required().StringVar(&knowledgeAgentCitation)

	hook := agent.Command("hook", "Reads a Claude Code UserPromptSubmit payload on stdin and writes the knowledge block to stdout").Action(knowledgeAgentHookAction)
	registerKnowledgeHookFlags(hook)

	agent.Command("stopwords", "Prints the built-in stopword list in the format --stopwords reads").Action(knowledgeAgentStopwordsAction)

	preview := agent.Command("preview", "Runs one message through the hook pipeline and shows the mode, the terms and the block it produces").Action(knowledgeAgentPreviewAction)
	preview.Arg("query", "The message to run the pipeline over, tag and all").Required().StringVar(&knowledgeHookQuery)
	registerKnowledgeHookFlags(preview)

	setup := agent.Command("setup", "Prints a prompt that has Claude install the hook into a Claude Code settings.json").Action(knowledgeAgentSetupAction)
	registerKnowledgeHookFlags(setup)
}

// knowledgeAgentSession carries the three things every agent verb needs: the
// parsed configuration, a read-only store, and the configuration file as an
// absolute path. openKnowledgeAgent leaves the process in the configuration's own
// directory and Close returns it to the directory the command started in.
//
// Read the store before Close. The reader pool retires an idle connection and
// opens another one from the same relative path, so a query made after the
// directory is restored looks for the index under the caller's directory.
type knowledgeAgentSession struct {
	cfg   *config.Config
	store *rag.Store

	// configPath is --config resolved against the directory the command started
	// in. A verb that renders a command for the model names it, since the model
	// runs that command from a directory this process never sees.
	configPath string

	priorDir string
}

// openKnowledgeAgent resolves --config, changes into the directory holding it and
// opens the index from there.
//
// rag.Open resolves a relative knowledge.directory against the working directory
// and knowledge.paths are read the same way, so after the change of directory both
// resolve against the corpus. Claude runs these verbs from whatever directory it is
// working in and passes a single absolute --config.
//
// --config therefore cannot take the default the knowledge command gives it: a
// defaulted flag would pick the directory out of wherever the caller stood.
func openKnowledgeAgent(pc *fisk.ParseContext) (*knowledgeAgentSession, error) {
	s, err := openKnowledgeAgentConfig(pc)
	if err != nil {
		return nil, err
	}

	s.store, err = rag.Open(s.cfg, knowledgeStoreDir)
	if err != nil {
		return nil, s.restore(fmt.Errorf("%w: %w", errKnowledgeIndexOpen, err))
	}

	return s, nil
}

// openKnowledgeAgentConfig resolves --config, changes into the directory holding it
// and parses it, leaving the session without a store. The stopwords verb prints a
// list the corpus has no part in, so an index that will not open leaves it printing
// the list. Close returns the process to the directory the command started in whether
// a store was opened or not.
func openKnowledgeAgentConfig(pc *fisk.ParseContext) (*knowledgeAgentSession, error) {
	if !flagWasSet(pc, "config") {
		return nil, fmt.Errorf("--config is required here and has no default: this command runs in the directory holding the configuration file, so knowledge.directory and knowledge.paths resolve from there rather than from the directory you started in; pass an absolute path, as in --config /srv/corpus/agent.yaml")
	}

	configPath, err := filepath.Abs(configFile)
	if err != nil {
		return nil, err
	}

	priorDir, err := os.Getwd()
	if err != nil {
		return nil, err
	}

	err = os.Chdir(filepath.Dir(configPath))
	if err != nil {
		return nil, err
	}

	s := &knowledgeAgentSession{configPath: configPath, priorDir: priorDir}

	// knowledgeConfig reads the package-level path, which now names a file in the
	// directory the command came from. The absolute path also reads better in the
	// errors that quote it.
	configFile = configPath

	s.cfg, err = knowledgeConfig()
	if err != nil {
		return nil, s.restore(err)
	}

	return s, nil
}

// Close closes the store and returns the process to the directory the command
// started in. A session opened without a store closes nothing.
func (s *knowledgeAgentSession) Close() error {
	if s.store == nil {
		return s.restore(nil)
	}

	return s.restore(s.store.Close())
}

// restore returns the process to the directory the command started in and reports
// err along with whatever the change of directory added to it.
func (s *knowledgeAgentSession) restore(err error) error {
	return errors.Join(err, os.Chdir(s.priorDir))
}

func knowledgeAgentShowAction(pc *fisk.ParseContext) error {
	ctx, cancel := interruptContext()
	defer cancel()

	// prompthook.ParseCitation rather than parseCitation: the block emits a range
	// and this verb takes back whatever the block wrote, so the block's own parser
	// is the one that agrees with it.
	relPath, first, last, err := prompthook.ParseCitation(knowledgeAgentCitation)
	if err != nil {
		return err
	}

	session, err := openKnowledgeAgent(pc)
	if err != nil {
		return err
	}
	defer session.Close()

	return showKnowledgeChunks(ctx, os.Stdout, session.store, relPath, first, last)
}

// showKnowledgeChunks writes the chunks from first through last to w in ordinal
// order.
//
// One ordinal prints its heading and body alone, as knowledge show does. Several
// print a citation line above each, so the model cites back the section it read.
// A citation naming a single chunk and one naming a range of one are the same
// token once parsed, so both print the single form.
//
// Printing stops at the first ordinal the index does not hold, and a last line
// names it. A reindex rewrites a document's ordinals from zero, so a citation
// written before one runs off the end of a shortened document rather than into a
// gap, and every ordinal past the first absent one is absent too. An absent first
// ordinal prints nothing and returns an error, since the caller got no text at
// all.
//
// The document path is sanitized on the way out. It arrives as the caller typed
// it, and every line here carries it.
func showKnowledgeChunks(ctx context.Context, w io.Writer, store *rag.Store, relPath string, first int, last int) error {
	span := last - first + 1
	if span > knowledgeAgentMaxSpan {
		return fmt.Errorf("a citation may cover %d chunks and this one covers %d; the knowledge block cites a run of adjacent sections and never one that wide", knowledgeAgentMaxSpan, span)
	}

	path := terminalToken(relPath)
	single := first == last
	printed := 0

	for ordinal := first; ordinal <= last; ordinal++ {
		headingPath, content, err := store.ChunkText(ctx, relPath, ordinal)
		if errors.Is(err, rag.ErrIndexNotBuilt) {
			return fmt.Errorf("the knowledge index has not been built yet; run: fisk knowledge index")
		}

		missing := errors.Is(err, sql.ErrNoRows)
		if err != nil && !missing {
			return err
		}

		if missing {
			if printed == 0 {
				token := rag.Citation(path, first)
				if !single {
					token = fmt.Sprintf("%s#%d-%d", path, first, last)
				}

				return fmt.Errorf("no chunk found for citation %q: it may have shifted since the last reindex; run 'fisk knowledge sources' to list files", token)
			}

			fmt.Fprintf(w, "\n%s has no chunk %d; the range stops there\n", path, ordinal)

			return nil
		}

		if printed > 0 {
			fmt.Fprintln(w)
		}
		printed++

		if !single {
			fmt.Fprintln(w, rag.Citation(path, ordinal))
		}

		if headingPath != "" {
			fmt.Fprintf(w, "# %s\n\n", headingPath)
		}
		fmt.Fprintln(w, content)
	}

	return nil
}
