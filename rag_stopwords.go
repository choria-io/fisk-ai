//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/choria-io/fisk"

	"github.com/choria-io/fisk-ai/internal/rag/prompthook"
)

func knowledgeAgentStopwordsAction(pc *fisk.ParseContext) error {
	return knowledgeStopwords(pc, os.Stdout)
}

// knowledgeStopwords writes the built-in list to out in the format --stopwords reads,
// so "fisk rag agent stopwords > mine.txt" starts a list an operator then edits.
//
// It opens no store. The list is compiled into the binary and the corpus has no part
// in it, so this verb prints it over a configuration whose index will not open. It
// takes --config and changes into the configuration's directory, as every verb in the
// group does.
func knowledgeStopwords(pc *fisk.ParseContext, out io.Writer) error {
	session, err := openKnowledgeAgentConfig(pc)
	if err != nil {
		return err
	}
	defer session.Close()

	return prompthook.WriteStopwords(out, prompthook.DefaultStopwords())
}

// knowledgeHookStopwordList reads --stopwords. A command naming no file gets nil back,
// which leaves prompthook on its built-in list.
//
// The caller reads it after openKnowledgeAgent has changed into the directory holding
// --config, so a relative path names a file beside the configuration. Claude Code runs
// the hook from whatever directory the person is working in, off one command line
// written once into a settings file, and a stopword list resolved against that
// directory would be found on some messages and not others. --logfile resolves the
// other way, against the directory the command ran from, because an operator passes it
// by hand to watch a run and reads the file where they stood.
//
// A file that will not open or will not read is a fault in the configuration and the
// caller exits 1 on it, as it does for a --config it cannot read. A file that reads as
// empty is a list of no words, which drops nothing.
func knowledgeHookStopwordList() ([]string, error) {
	if knowledgeHookStopwords == "" {
		return nil, nil
	}

	// An error quotes the absolute path. The operator reads that message from the
	// directory they ran in, where the relative path they typed names another file or
	// none.
	path, err := filepath.Abs(knowledgeHookStopwords)
	if err != nil {
		return nil, fmt.Errorf("--stopwords %q cannot be resolved: %w", knowledgeHookStopwords, err)
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("--stopwords %s cannot be opened, resolved against the directory holding --config: %w", path, err)
	}
	defer f.Close()

	words, err := prompthook.ReadStopwords(f)
	if err != nil {
		return nil, fmt.Errorf("--stopwords %s cannot be read: %w", path, err)
	}

	return words, nil
}
