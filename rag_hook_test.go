//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/choria-io/fisk"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/choria-io/fisk-ai/config"
	"github.com/choria-io/fisk-ai/internal/rag"
	"github.com/choria-io/fisk-ai/internal/rag/prompthook"
)

// hookExit stands in for the process exit fisk asks for. The real terminate is
// os.Exit, which never returns, so a handler that returns would run the usage dump
// and the second terminate that MustParseWithUsage has after every Fatalf and no
// binary ever reaches. Panicking and recovering leaves the exit where the binary
// leaves it.
type hookExit int

// countingWriter counts the calls that reach it, which is what tells one write of a
// whole entry from one write per line. A file holds the same bytes either way.
type countingWriter struct {
	writes int
	body   strings.Builder
}

func (w *countingWriter) Write(p []byte) (int, error) {
	w.writes++

	return w.body.Write(p)
}

var _ = Describe("knowledge agent hook", func() {
	ctx := context.Background()

	// Every section names the cache, so one term ranks four chunks of one document
	// and the two count flags have something to cut.
	const guide = `# Guide

The guide covers the transport cache.

## Framing

Every frame carries a cache length prefix.

## Backpressure

The queue slows the cache when the buffer fills.

## Cache

Cache entries expire after an hour.
`

	// A second document holding as many matching sections. One count caps the block
	// per document and the other caps the search, so telling them apart takes a
	// corpus where the two produce different numbers.
	const notes = `# Notes

The notes cover the cache too.

## Eviction

The cache evicts the oldest entry first.

## Warmup

A cold cache fills on the first request.

## Sizing

Size the cache to the working set.
`

	// No directory key, so the index lands under whatever directory the command
	// runs in, which is how the change of directory the group performs is proven.
	const agentYAML = `identity: corpus
harness:
  knowledge:
    enabled: true
    paths:
      - docs
`

	// A configuration naming a document as the store directory, so rag.Open fails
	// after the configuration itself has parsed.
	const notADirYAML = `identity: corpus
harness:
  knowledge:
    enabled: true
    directory: docs/guide.md
`

	const disabledYAML = `identity: corpus
harness:
  knowledge:
    enabled: false
`

	const (
		builtConfig    = "corpus/agent.yaml"
		notADirConfig  = "corpus/notadir.yaml"
		disabledConfig = "corpus/disabled.yaml"
		unbuiltConfig  = "unbuilt/agent.yaml"
		emptyConfig    = "empty/agent.yaml"
	)

	var (
		tmp        string
		configPath string
		builtCfg   *config.Config
	)

	// writeCorpus lays out one directory holding a configuration and, where docs is
	// given, the guide under it.
	writeCorpus := func(dir string, name string, body string, docs bool) string {
		GinkgoHelper()

		Expect(os.MkdirAll(dir, 0o755)).To(Succeed())
		if docs {
			Expect(os.MkdirAll(filepath.Join(dir, "docs"), 0o755)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(dir, "docs", "guide.md"), []byte(guide), 0o644)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(dir, "docs", "notes.md"), []byte(notes), 0o644)).To(Succeed())
		}

		path := filepath.Join(dir, name)
		Expect(os.WriteFile(path, []byte(body), 0o644)).To(Succeed())

		return path
	}

	// index builds the index for a configuration from its own directory, the way an
	// operator builds it, and returns the configuration it read.
	index := func(dir string, name string) *config.Config {
		GinkgoHelper()

		prior, err := os.Getwd()
		Expect(err).ToNot(HaveOccurred())
		Expect(os.Chdir(dir)).To(Succeed())
		defer func() {
			Expect(os.Chdir(prior)).To(Succeed())
		}()

		cfg, err := config.ParseConfigFileForMode(name, config.ModeMCP)
		Expect(err).ToNot(HaveOccurred())

		w, err := rag.OpenWriter(cfg, "")
		Expect(err).ToNot(HaveOccurred())
		_, err = w.Index(ctx, []string{"docs"}, rag.IndexOptions{Reconcile: true})
		Expect(err).ToNot(HaveOccurred())
		Expect(w.Close()).To(Succeed())

		return cfg
	}

	BeforeEach(func() {
		// Resolved, because the session takes its absolute --config from the working
		// directory and macOS hands that back with the symlinks followed. A spec
		// comparing the two needs them spelled the same way.
		resolved, err := filepath.EvalSymlinks(GinkgoT().TempDir())
		Expect(err).ToNot(HaveOccurred())
		tmp = resolved

		corpus := filepath.Join(tmp, "corpus")
		configPath = writeCorpus(corpus, "agent.yaml", agentYAML, true)
		writeCorpus(corpus, "notadir.yaml", notADirYAML, false)
		writeCorpus(corpus, "disabled.yaml", disabledYAML, false)
		builtCfg = index(corpus, "agent.yaml")

		// An index nobody has built, and one built over a directory holding no
		// documents. The two report different statuses and want different sentences.
		writeCorpus(filepath.Join(tmp, "unbuilt"), "agent.yaml", agentYAML, true)

		empty := filepath.Join(tmp, "empty")
		writeCorpus(empty, "agent.yaml", agentYAML, false)
		Expect(os.MkdirAll(filepath.Join(empty, "docs"), 0o755)).To(Succeed())
		index(empty, "agent.yaml")

		// Every spec runs from a directory holding neither a configuration nor an
		// index, which is where a hook runs from, and every relative --config below
		// resolves against it.
		prior, err := os.Getwd()
		Expect(err).ToNot(HaveOccurred())
		Expect(os.Chdir(tmp)).To(Succeed())
		DeferCleanup(func() {
			Expect(os.Chdir(prior)).To(Succeed())
		})
	})

	// run parses args as the fisk binary does, with stdin carrying payload, and
	// returns what the command wrote to each stream along with the exit it asked for.
	run := func(payload string, args ...string) (string, string, int) {
		GinkgoHelper()

		inR, inW, err := os.Pipe()
		Expect(err).ToNot(HaveOccurred())
		_, err = inW.WriteString(payload)
		Expect(err).ToNot(HaveOccurred())
		Expect(inW.Close()).To(Succeed())

		outR, outW, err := os.Pipe()
		Expect(err).ToNot(HaveOccurred())

		errR, errW, err := os.Pipe()
		Expect(err).ToNot(HaveOccurred())

		priorIn, priorOut, priorErr := os.Stdin, os.Stdout, os.Stderr
		os.Stdin, os.Stdout, os.Stderr = inR, outW, errW

		// After the swap: fisk takes its error writer from os.Stderr as it is built,
		// and the message a failing verb produces is written through that.
		app := fisk.New("fisk", "Fisk AI Toolkit")
		registerRAGCommand(app)

		code := 0
		app.Terminate(func(status int) { panic(hookExit(status)) })

		func() {
			defer func() {
				r := recover()
				if r == nil {
					return
				}

				exit, ok := r.(hookExit)
				if !ok {
					panic(r)
				}

				code = int(exit)
			}()

			app.MustParseWithUsage(args)
		}()

		os.Stdin, os.Stdout, os.Stderr = priorIn, priorOut, priorErr
		Expect(inR.Close()).To(Succeed())
		Expect(outW.Close()).To(Succeed())
		Expect(errW.Close()).To(Succeed())

		stdout, err := io.ReadAll(outR)
		Expect(err).ToNot(HaveOccurred())
		Expect(outR.Close()).To(Succeed())

		stderr, err := io.ReadAll(errR)
		Expect(err).ToNot(HaveOccurred())
		Expect(errR.Close()).To(Succeed())

		return string(stdout), string(stderr), code
	}

	hook := func(payload string, args ...string) (string, string, int) {
		GinkgoHelper()

		return run(payload, append([]string{"knowledge", "agent", "hook", "--config", builtConfig}, args...)...)
	}

	preview := func(query string, args ...string) (string, string, int) {
		GinkgoHelper()

		return run("", append([]string{"knowledge", "agent", "preview", "--config", builtConfig, query}, args...)...)
	}

	// citationLines counts the lines of a block that cite a section. The fetch
	// instruction is indented the same way and names the binary, so the corpus path
	// is what tells the two apart.
	citationLines := func(block string) int {
		n := 0

		for _, line := range strings.Split(block, "\n") {
			if strings.HasPrefix(line, "  docs/") {
				n++
			}
		}

		return n
	}

	Describe("hook", func() {
		It("Should write the block for a bare tag, ignoring the fields it does not read", func() {
			stdout, stderr, code := hook(`{"session_id":"s1","cwd":"/somewhere","hook_event_name":"UserPromptSubmit","prompt":"<rag> what does the transport cache"}`)

			Expect(code).To(Equal(0))
			Expect(stderr).To(BeEmpty())
			Expect(stdout).To(HavePrefix("<knowledge-index>\n"))
			Expect(stdout).To(ContainSubstring("docs/guide.md#"))
			Expect(stdout).To(HaveSuffix("</knowledge-index>\n"))
		})

		// A hook runs under a login shell's environment rather than the one the
		// operator tuned, so the instruction names this binary by its own path and
		// the configuration by the absolute one the session resolved.
		It("Should name the running binary and the absolute config in the fetch instruction", func() {
			exe, err := os.Executable()
			Expect(err).ToNot(HaveOccurred())

			stdout, _, code := hook(`{"prompt":"<rag transport cache>"}`)

			Expect(code).To(Equal(0))
			Expect(stdout).To(ContainSubstring(exe + " rag agent show --config " + configPath))
			Expect(configPath).To(Equal(filepath.Join(tmp, builtConfig)))
		})

		It("Should write the block for a tag carrying its own words and say the words chose it", func() {
			stdout, _, code := hook(`{"prompt":"anything at all <rag transport cache> and more"}`)

			Expect(code).To(Equal(0))
			Expect(stdout).To(ContainSubstring("The words inside the <rag ...> tag chose these sections"))
			Expect(stdout).To(ContainSubstring("docs/guide.md#"))
		})

		It("Should write nothing for an untagged message until --auto is given", func() {
			payload := `{"prompt":"how does the transport cache framing work"}`

			stdout, stderr, code := hook(payload)
			Expect(code).To(Equal(0))
			Expect(stdout).To(BeEmpty())
			Expect(stderr).To(BeEmpty())

			stdout, stderr, code = hook(payload, "--auto")
			Expect(code).To(Equal(0))
			Expect(stderr).To(BeEmpty())
			Expect(stdout).To(ContainSubstring("docs/guide.md#"))
		})

		// --top-k caps the whole search and --per-doc caps one document's share of the
		// block, so the corpus of two documents holding four matching sections each
		// gives the two counts different numbers and a swap of them shows.
		It("Should ask the index for --top-k sections in total", func() {
			stdout, _, code := hook(`{"prompt":"<rag cache>"}`, "--top-k", "1", "--per-doc", "8")
			Expect(code).To(Equal(0))
			Expect(citationLines(stdout)).To(Equal(1))

			stdout, _, code = hook(`{"prompt":"<rag cache>"}`, "--top-k", "3", "--per-doc", "8")
			Expect(code).To(Equal(0))
			Expect(citationLines(stdout)).To(Equal(3))
		})

		It("Should let each document contribute --per-doc sections", func() {
			stdout, _, code := hook(`{"prompt":"<rag cache>"}`, "--top-k", "8", "--per-doc", "1")
			Expect(code).To(Equal(0))
			Expect(citationLines(stdout)).To(Equal(2))
			Expect(strings.Count(stdout, "  docs/guide.md#")).To(Equal(1))
			Expect(strings.Count(stdout, "  docs/notes.md#")).To(Equal(1))

			stdout, _, code = hook(`{"prompt":"<rag cache>"}`, "--top-k", "8", "--per-doc", "2")
			Expect(code).To(Equal(0))
			Expect(citationLines(stdout)).To(Equal(4))
		})

		DescribeTable("Should exit zero writing nothing, and name the reason on stderr only under --debug",
			func(configName string, payload string, reason string, args ...string) {
				base := append([]string{"knowledge", "agent", "hook", "--config", configName}, args...)

				stdout, stderr, code := run(payload, base...)
				Expect(code).To(Equal(0))
				Expect(stdout).To(BeEmpty())
				Expect(stderr).To(BeEmpty())

				stdout, stderr, code = run(payload, append(base, "--debug")...)
				Expect(code).To(Equal(0))
				Expect(stdout).To(BeEmpty())
				Expect(stderr).To(ContainSubstring(reason))
			},
			Entry("the message carries no tag",
				builtConfig, `{"prompt":"how does the transport cache framing work"}`,
				"carries no <rag> tag and --auto is off"),

			Entry("the payload carries no prompt field",
				builtConfig, `{"session_id":"s1","hook_event_name":"UserPromptSubmit"}`,
				"the message is empty"),

			Entry("the filter left the message no terms",
				builtConfig, `{"prompt":"well then see what you can do"}`,
				"every word is a stopword or too short", "--auto"),

			Entry("the filter left the message too few terms",
				builtConfig, `{"prompt":"tell me about the cache"}`,
				"--min-words is 3 and the message kept 1", "--auto"),

			Entry("the message is a slash command",
				builtConfig, `{"prompt":"/clear the transport cache framing"}`,
				"is a slash command", "--auto"),

			Entry("nothing ranked against the terms",
				builtConfig, `{"prompt":"<rag quicksilver octopus>"}`,
				"nothing ranked against these terms"),

			// Everything about the index lands here. A reindex or a fisk knowledge
			// index fixes each of them, and neither is something a person can do
			// mid-sentence.
			Entry("nobody has built the index",
				unbuiltConfig, `{"prompt":"<rag transport cache>"}`,
				"the knowledge index has not been built yet"),

			Entry("the index holds no documents",
				emptyConfig, `{"prompt":"<rag transport cache>"}`,
				"the knowledge index is empty"),

			Entry("the index will not open",
				notADirConfig, `{"prompt":"<rag transport cache>"}`,
				"the knowledge index cannot be opened"),
		)

		// An ordinary fisk upgrade produces this, and a reindex clears it. A loud
		// hook would put a line on every message the operator sent until they got
		// around to running one.
		It("Should exit zero writing nothing when the index is from an older format", func() {
			db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)", rag.StorePath(builtCfg, filepath.Join(tmp, "corpus"))))
			Expect(err).ToNot(HaveOccurred())
			_, err = db.Exec(`UPDATE rag_meta SET value = '1' WHERE key = 'format_version'`)
			Expect(err).ToNot(HaveOccurred())
			Expect(db.Close()).To(Succeed())

			stdout, stderr, code := hook(`{"prompt":"<rag transport cache>"}`)
			Expect(code).To(Equal(0))
			Expect(stdout).To(BeEmpty())
			Expect(stderr).To(BeEmpty())

			stdout, stderr, code = hook(`{"prompt":"<rag transport cache>"}`, "--debug")
			Expect(code).To(Equal(0))
			Expect(stdout).To(BeEmpty())
			Expect(stderr).To(ContainSubstring("format_version"))
		})

		// Exit 1 rather than 2, which on UserPromptSubmit would discard the message
		// the hook was meant to help with. The error names the hook, since a
		// transcript may be where the operator reads it.
		DescribeTable("Should exit one naming the hook on stderr, and write nothing to stdout",
			func(configName string, payload string, message string) {
				args := []string{"knowledge", "agent", "hook"}
				if configName != "" {
					args = append(args, "--config", configName)
				}

				stdout, stderr, code := run(payload, args...)

				Expect(code).To(Equal(1))
				Expect(stdout).To(BeEmpty())
				Expect(stderr).To(ContainSubstring(message))
				Expect(stderr).To(ContainSubstring("knowledge prompt hook"))
			},
			Entry("stdin carried nothing at all",
				builtConfig, "", "read nothing on stdin"),

			Entry("stdin carried something that is not JSON",
				builtConfig, "not a payload", "cannot read the UserPromptSubmit payload on stdin"),

			// A bare null decodes into every field of a value untouched, so it would
			// otherwise pass for a message nobody typed.
			Entry("stdin carried a bare null",
				builtConfig, "null", "read a null payload on stdin"),

			Entry("no --config was given",
				"", `{"prompt":"<rag transport cache>"}`, "--config is required here and has no default"),

			Entry("--config names a file that is not there",
				"absent.yaml", `{"prompt":"<rag transport cache>"}`, "absent.yaml"),

			Entry("the configuration carries no enabled knowledge block",
				disabledConfig, `{"prompt":"<rag transport cache>"}`, "knowledge is not enabled"),
		)
	})

	// A hook adds nothing a person can see. An operator reads the file to tell one that
	// is working from one that has never run.
	Describe("logfile", func() {
		var logPath string

		BeforeEach(func() {
			logPath = filepath.Join(tmp, "hook.log")
		})

		// entries splits the file into the blocks it holds, dropping the empty tail the
		// blank line closing the last entry leaves.
		entries := func() []string {
			GinkgoHelper()

			body, err := os.ReadFile(logPath)
			Expect(err).ToNot(HaveOccurred())

			var out []string

			for _, block := range strings.Split(string(body), "\n\n") {
				if strings.TrimSpace(block) != "" {
					out = append(out, block)
				}
			}

			return out
		}

		It("Should record every field of a lookup that produced a block", func() {
			stdout, _, code := hook(`{"prompt":"<rag transport cache> whats the story here"}`, "--logfile", logPath)
			Expect(code).To(Equal(0))
			Expect(stdout).To(ContainSubstring("docs/guide.md#"))

			Expect(entries()).To(HaveLen(1))

			lines := strings.Split(entries()[0], "\n")

			Expect(lines[0]).To(MatchRegexp(`^=== \d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z  hook$`))
			Expect(lines[1]).To(Equal("config  " + configPath))
			Expect(lines[2]).To(Equal("store   " + rag.StorePath(builtCfg, filepath.Join(tmp, "corpus"))))
			Expect(lines[3]).To(Equal("prompt  <rag transport cache> whats the story here"))
			Expect(lines[4]).To(Equal("mode    tag_words"))
			Expect(lines[5]).To(Equal("terms   transport cache"))
			Expect(lines[6]).To(MatchRegexp(`^search  ok, lexical, \d+ hits, \d+ sections after the per-document cap$`))
			Expect(lines[7]).To(MatchRegexp(`^took    \d+ms$`))
			Expect(lines[8]).To(Equal("block"))
			Expect(lines[9]).To(HavePrefix("  docs/"))
		})

		It("Should record a gate by name and record no block", func() {
			stdout, _, code := hook(`{"prompt":"/clear the transport cache"}`, "--auto", "--logfile", logPath)
			Expect(code).To(Equal(0))
			Expect(stdout).To(BeEmpty())

			Expect(entries()[0]).To(ContainSubstring("gate    slash_command"))
			Expect(entries()[0]).ToNot(ContainSubstring("block"))
		})

		// The hook fires on every message a person sends, so recording the untagged
		// ones would make the file a transcript of everything typed at Claude.
		It("Should record nothing for an untagged message while --auto is off", func() {
			stdout, _, code := hook(`{"prompt":"how does the transport cache framing work"}`, "--logfile", logPath)
			Expect(code).To(Equal(0))
			Expect(stdout).To(BeEmpty())

			body, err := os.ReadFile(logPath)
			Expect(err).ToNot(HaveOccurred())
			Expect(body).To(BeEmpty())
		})

		It("Should append to the file rather than truncate it, and record preview under its own verb", func() {
			_, _, code := hook(`{"prompt":"<rag transport cache>"}`, "--logfile", logPath)
			Expect(code).To(Equal(0))

			_, _, code = preview("<rag transport cache>", "--logfile", logPath)
			Expect(code).To(Equal(0))

			blocks := entries()
			Expect(blocks).To(HaveLen(2))
			Expect(blocks[0]).To(ContainSubstring("  hook\n"))
			Expect(blocks[1]).To(ContainSubstring("  preview\n"))
		})

		// The mask is cleared for the run, so the mode is the one the code asked for
		// rather than the one the machine's umask happened to leave of it.
		It("Should create the file readable by its owner alone", func() {
			restore, masked := withCreationMask(0)
			if !masked {
				Skip("this platform has no file creation mask")
			}
			defer restore()

			_, _, code := hook(`{"prompt":"<rag transport cache>"}`, "--logfile", logPath)
			Expect(code).To(Equal(0))

			info, err := os.Stat(logPath)
			Expect(err).ToNot(HaveOccurred())
			Expect(info.Mode().Perm()).To(Equal(os.FileMode(0o600)))
		})

		// One write per entry, so two hooks running at once interleave entries rather
		// than lines. The file reads the same either way, so only a writer that counts
		// its calls tells them apart.
		It("Should append an entry in a single write", func() {
			w := &countingWriter{}
			log := &knowledgeHookLog{to: w, path: logPath}

			entry := prompthook.LogEntry{
				Time:       time.Now(),
				Verb:       "hook",
				ConfigPath: configPath,
				Prompt:     "<rag transport cache>",
			}

			Expect(log.record(entry)).To(Succeed())
			Expect(w.writes).To(Equal(1))
			Expect(w.body.String()).To(Equal(entry.String()))
		})

		// The hook runs from wherever the person is working and the group then changes
		// into the corpus, so a path resolved after that change would leave the file
		// among the documents.
		It("Should resolve a relative path against the directory the command ran from", func() {
			_, _, code := hook(`{"prompt":"<rag transport cache>"}`, "--logfile", "hook.log")
			Expect(code).To(Equal(0))

			Expect(logPath).To(BeAnExistingFile())
			Expect(filepath.Join(tmp, "corpus", "hook.log")).ToNot(BeAnExistingFile())
		})

		// The operator reads the message in a transcript, away from the directory the
		// hook ran in, so the relative path they typed names nothing there.
		It("Should name the file it opened by its absolute path", func() {
			_, stderr, code := hook(`{"prompt":"<rag transport cache>"}`, "--logfile", "corpus")

			Expect(code).To(Equal(1))
			Expect(stderr).To(ContainSubstring("--logfile " + filepath.Join(tmp, "corpus") + " cannot be opened"))
		})

		// A person pastes text with blank lines in it, and an entry opens with "=== "
		// and ends with a blank line. Neither may come out of a message.
		It("Should hold one entry for a message carrying an entry of its own", func() {
			stdout, _, code := hook(`{"prompt":"<rag transport cache>\n\n=== 2020-01-01T00:00:00Z  hook\nconfig  /etc/shadow"}`, "--logfile", logPath)
			Expect(code).To(Equal(0))
			Expect(stdout).To(ContainSubstring("docs/guide.md#"))

			body, err := os.ReadFile(logPath)
			Expect(err).ToNot(HaveOccurred())

			Expect(entries()).To(HaveLen(1))
			Expect(strings.Count(string(body), "=== ")).To(Equal(2), "the opener and the one the message carried, indented")
			Expect(string(body)).To(ContainSubstring(strings.Join([]string{
				"prompt  <rag transport cache>",
				"        ",
				"        === 2020-01-01T00:00:00Z  hook",
				"        config  /etc/shadow",
			}, "\n")))
		})

		// An index that will not open is silent on stderr and exits zero, so an empty
		// file is the one answer the flag must not give the operator who turned it on
		// to find out whether the hook runs at all. Four faults, one shape: the files
		// the run named, the message it read, and the fault that ended it.
		DescribeTable("Should record a fault the run could not get past",
			func(configName string, exit int, fault string) {
				args := []string{"knowledge", "agent", "hook", "--logfile", logPath}
				if configName != "" {
					args = append(args, "--config", configName)
				}

				stdout, _, code := run(`{"prompt":"<rag transport cache>"}`, args...)
				Expect(code).To(Equal(exit))
				Expect(stdout).To(BeEmpty())
				Expect(entries()).To(HaveLen(1))

				entry := entries()[0]
				lines := strings.Split(entry, "\n")

				Expect(lines[0]).To(MatchRegexp(`^=== \d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z  hook$`))
				Expect(entry).To(ContainSubstring("prompt  <rag transport cache>"))
				Expect(entry).To(ContainSubstring("fault   " + fault))
				Expect(entry).To(MatchRegexp(`\ntook    \d+ms`))

				// No session, so no index path to name and no message the pipeline read.
				Expect(entry).ToNot(ContainSubstring("\nstore   "))
				Expect(entry).ToNot(ContainSubstring("\nmode    "))
				Expect(entry).ToNot(ContainSubstring("\nblock"))

				if configName == "" {
					Expect(entry).ToNot(ContainSubstring("\nconfig  "))
					return
				}

				Expect(entry).To(ContainSubstring("config  " + filepath.Join(tmp, configName)))
			},
			Entry("the index will not open", notADirConfig, 0, "the knowledge index cannot be opened"),
			Entry("--config names a file that is not there", "absent.yaml", 1, "reading config"),
			Entry("the configuration carries no enabled knowledge block", disabledConfig, 1, "knowledge is not enabled"),
			Entry("no --config was given", "", 1, "--config is required here"),
		)

		// The run had created the file by the time stdin failed it, so leaving these
		// three out would leave the operator a file of zero bytes. There is no prompt
		// line, since the message that never arrived is the fault.
		DescribeTable("Should record a payload it could not read",
			func(payload string, fault string) {
				stdout, _, code := run(payload, "knowledge", "agent", "hook", "--config", builtConfig, "--logfile", logPath)

				Expect(code).To(Equal(1))
				Expect(stdout).To(BeEmpty())
				Expect(entries()).To(HaveLen(1))

				entry := entries()[0]

				Expect(entry).To(ContainSubstring("config  " + configPath))
				Expect(entry).To(ContainSubstring("fault   the knowledge prompt hook " + fault))
				Expect(entry).To(MatchRegexp(`\ntook    \d+ms`))
				Expect(entry).ToNot(ContainSubstring("\nprompt  "))
			},
			Entry("stdin carried nothing at all", "", "read nothing on stdin"),
			Entry("stdin carried something that is not JSON", "not a payload", "cannot read the UserPromptSubmit payload on stdin"),
			Entry("stdin carried a bare null", "null", "read a null payload on stdin"),
		)

		// A logfile the operator asked for and is not getting is the fault this flag
		// exists to prevent, so it is the configuration class rather than the silent one.
		It("Should exit one writing nothing when the file will not open", func() {
			stdout, stderr, code := hook(`{"prompt":"<rag transport cache>"}`, "--logfile", filepath.Join(tmp, "corpus"))

			Expect(code).To(Equal(1))
			Expect(stdout).To(BeEmpty())
			Expect(stderr).To(ContainSubstring("cannot be opened"))
			Expect(stderr).To(ContainSubstring("knowledge prompt hook"))
		})
	})

	Describe("preview", func() {
		It("Should print the mode and the terms above the block it ran", func() {
			stdout, stderr, code := preview("<rag> what does the transport cache")

			Expect(code).To(Equal(0))
			Expect(stderr).To(BeEmpty())

			lines := strings.Split(stdout, "\n")
			Expect(lines[0]).To(Equal("mode:   tag"))
			Expect(lines[1]).To(Equal("terms:  transport cache"))
			Expect(lines[2]).To(BeEmpty())
			Expect(lines[3]).To(Equal("<knowledge-index>"))
			Expect(stdout).To(ContainSubstring("docs/guide.md#"))
		})

		It("Should name the gate in a sentence and print no block when one fires", func() {
			stdout, _, code := preview("tell me about the cache", "--auto")

			Expect(code).To(Equal(0))
			Expect(stdout).To(Equal(strings.Join([]string{
				"mode:   auto",
				"terms:  cache",
				"gate:   --min-words is 3 and the message kept 1, so no lookup ran",
				"",
			}, "\n")))
		})

		It("Should say the terms are none when the filter left the message nothing", func() {
			stdout, _, code := preview("well then see what you can do", "--auto")

			Expect(code).To(Equal(0))
			Expect(stdout).To(ContainSubstring("terms:  (none)"))
			Expect(stdout).To(ContainSubstring("gate:   every word is a stopword or too short"))
		})

		It("Should report an empty ranking as a result rather than a gate", func() {
			stdout, _, code := preview("<rag quicksilver octopus>")

			Expect(code).To(Equal(0))
			Expect(stdout).To(ContainSubstring("mode:   tag_words"))
			Expect(stdout).To(ContainSubstring("result: nothing ranked against these terms"))
			Expect(stdout).ToNot(ContainSubstring("<knowledge-index>"))
		})

		It("Should add the search status and the hit count under --debug", func() {
			stdout, _, code := preview("<rag> what does the transport cache", "--debug")

			Expect(code).To(Equal(0))
			Expect(stdout).To(MatchRegexp(`search: ok, \d+ hits before the per-document cap`))
		})

		// A person ran preview and is waiting on it, where the hook runs behind a
		// message somebody else is typing.
		It("Should fail on an index the hook passes over in silence", func() {
			stdout, stderr, code := run("", "knowledge", "agent", "preview", "--config", notADirConfig, "<rag transport cache>")

			Expect(code).To(Equal(1))
			Expect(stdout).To(BeEmpty())
			Expect(stderr).To(ContainSubstring("the knowledge index cannot be opened"))
		})
	})

	// The stopword list is what an operator tunes, and the words worth dropping are
	// the ones they wrap their own questions in. A list compiled into the binary takes
	// a rebuild to change.
	Describe("stopwords", func() {
		var listPath string

		BeforeEach(func() {
			listPath = filepath.Join(tmp, "mine.txt")
		})

		It("Should print the built-in list and read it back through --stopwords unchanged", func() {
			stdout, stderr, code := run("", "knowledge", "agent", "stopwords", "--config", builtConfig)
			Expect(code).To(Equal(0))
			Expect(stderr).To(BeEmpty())

			words, err := prompthook.ReadStopwords(strings.NewReader(stdout))
			Expect(err).ToNot(HaveOccurred())
			Expect(words).To(Equal(prompthook.DefaultStopwords()))

			Expect(os.WriteFile(listPath, []byte(stdout), 0o644)).To(Succeed())

			withFile, _, code := preview("show me the transport cache", "--auto", "--stopwords", listPath)
			Expect(code).To(Equal(0))

			byDefault, _, code := preview("show me the transport cache", "--auto")
			Expect(code).To(Equal(0))
			Expect(withFile).To(Equal(byDefault))
		})

		// The list is compiled into the binary and the corpus has no part in it, so
		// the verb prints it over a configuration whose index will not open.
		It("Should print the list without opening a store", func() {
			stdout, stderr, code := run("", "knowledge", "agent", "stopwords", "--config", notADirConfig)

			Expect(code).To(Equal(0))
			Expect(stderr).To(BeEmpty())
			Expect(strings.Split(stdout, "\n")).To(ContainElement("about"))
		})

		It("Should require --config as the rest of the group does", func() {
			_, stderr, code := run("", "knowledge", "agent", "stopwords")

			Expect(code).To(Equal(1))
			Expect(stderr).To(ContainSubstring("--config is required here"))
		})

		// The file replaces the built-in list rather than adding to it, so a corpus
		// that discusses a word the default drops gets to query for it by deleting
		// one line.
		It("Should keep a term the built-in list drops when the file omits it", func() {
			byDefault, _, code := preview("show me the transport cache", "--auto")
			Expect(code).To(Equal(0))
			Expect(byDefault).To(ContainSubstring("terms:  transport cache"))

			var kept []string
			for _, w := range prompthook.DefaultStopwords() {
				if w == "show" {
					continue
				}

				kept = append(kept, w)
			}

			var b strings.Builder
			Expect(prompthook.WriteStopwords(&b, kept)).To(Succeed())
			Expect(os.WriteFile(listPath, []byte(b.String()), 0o644)).To(Succeed())

			stdout, _, code := preview("show me the transport cache", "--auto", "--stopwords", listPath)
			Expect(code).To(Equal(0))
			Expect(stdout).To(ContainSubstring("terms:  show transport cache"))
		})

		// An empty list is a list. Falling back to the built-in one would ignore the
		// file the operator wrote.
		It("Should drop no word for a file holding none", func() {
			Expect(os.WriteFile(listPath, []byte("# every word is commented out\n\n   \n"), 0o644)).To(Succeed())

			stdout, _, code := preview("show me the transport cache", "--auto", "--stopwords", listPath)
			Expect(code).To(Equal(0))
			Expect(stdout).To(ContainSubstring("terms:  show me the transport cache"))
		})

		// Claude Code runs the hook from whatever directory the person is working in,
		// off one command line written once into a settings file, so a list resolved
		// against that directory would be found on some messages and not others.
		It("Should resolve a relative path against the directory holding --config", func() {
			Expect(os.WriteFile(filepath.Join(tmp, "corpus", "mine.txt"), []byte("cache\n"), 0o644)).To(Succeed())
			Expect(os.WriteFile(listPath, []byte("transport\n"), 0o644)).To(Succeed())

			stdout, _, code := preview("show the transport cache", "--auto", "--stopwords", "mine.txt")

			Expect(code).To(Equal(0))
			Expect(stdout).To(ContainSubstring("terms:  show the transport"))
		})

		// A file the operator named and the hook cannot read is a fault in the
		// configuration, as a --config that is not there is. Running the built-in list
		// instead would strip words they had deleted and never say so.
		It("Should exit one from either verb when the file cannot be opened", func() {
			absent := filepath.Join(tmp, "corpus", "absent.txt")

			stdout, stderr, code := preview("<rag transport cache>", "--stopwords", "absent.txt")
			Expect(code).To(Equal(1))
			Expect(stdout).To(BeEmpty())
			Expect(stderr).To(ContainSubstring("--stopwords " + absent + " cannot be opened"))

			// An operator who redirected a dump into the directory they were standing
			// in and passed the bare name back needs to be told which directory was
			// searched, since the two are not the same one.
			Expect(stderr).To(ContainSubstring("resolved against the directory holding --config"))

			logPath := filepath.Join(tmp, "stopwords.log")

			stdout, stderr, code = hook(`{"prompt":"<rag transport cache>"}`, "--stopwords", "absent.txt", "--logfile", logPath)
			Expect(code).To(Equal(1))
			Expect(stdout).To(BeEmpty())
			Expect(stderr).To(ContainSubstring("the knowledge prompt hook cannot run"))
			Expect(stderr).To(ContainSubstring("--stopwords " + absent + " cannot be opened"))

			body, err := os.ReadFile(logPath)
			Expect(err).ToNot(HaveOccurred())
			Expect(string(body)).To(ContainSubstring("fault   --stopwords " + absent))
			Expect(string(body)).To(ContainSubstring("prompt  <rag transport cache>"))
		})
	})
})
