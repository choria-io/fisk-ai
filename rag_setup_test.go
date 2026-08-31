//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/choria-io/fisk"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/choria-io/fisk-ai/config"
	"github.com/choria-io/fisk-ai/internal/rag"
	"github.com/choria-io/fisk-ai/internal/rag/prompthook"
)

// setupSettings decodes the fragment the prompt carries, so a spec reads the entry
// rather than matching text that happens to sit near it.
type setupSettings struct {
	Hooks struct {
		UserPromptSubmit []struct {
			Hooks []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
				Timeout int    `json:"timeout"`
			} `json:"hooks"`
		} `json:"UserPromptSubmit"`
	} `json:"hooks"`
}

var _ = Describe("knowledge agent setup", func() {
	ctx := context.Background()

	const guide = `# Guide

The guide covers the transport cache.

## Framing

Every frame carries a cache length prefix.
`

	// No directory key, so the index lands under whatever directory the command runs
	// in and the change of directory the group performs decides where it is looked for.
	const agentYAML = `identity: corpus
harness:
  knowledge:
    enabled: true
    paths:
      - docs
`

	const (
		builtConfig   = "corpus/agent.yaml"
		unbuiltConfig = "unbuilt/agent.yaml"
	)

	var (
		tmp        string
		configPath string
	)

	writeCorpus := func(dir string, body string) string {
		GinkgoHelper()

		Expect(os.MkdirAll(filepath.Join(dir, "docs"), 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(dir, "docs", "guide.md"), []byte(guide), 0o644)).To(Succeed())

		path := filepath.Join(dir, "agent.yaml")
		Expect(os.WriteFile(path, []byte(body), 0o644)).To(Succeed())

		return path
	}

	// indexIn builds the index for a configuration from its own directory, the way an
	// operator builds it, under the store base given. An empty base leaves the index
	// where a relative knowledge.directory puts it, beside the configuration.
	indexIn := func(dir string, storeDir string) {
		GinkgoHelper()

		prior, err := os.Getwd()
		Expect(err).ToNot(HaveOccurred())
		Expect(os.Chdir(dir)).To(Succeed())
		defer func() {
			Expect(os.Chdir(prior)).To(Succeed())
		}()

		cfg, err := config.ParseConfigFileForMode("agent.yaml", config.ModeMCP)
		Expect(err).ToNot(HaveOccurred())

		w, err := rag.OpenWriter(cfg, storeDir)
		Expect(err).ToNot(HaveOccurred())
		_, err = w.Index(ctx, []string{"docs"}, rag.IndexOptions{Reconcile: true})
		Expect(err).ToNot(HaveOccurred())
		Expect(w.Close()).To(Succeed())
	}

	index := func(dir string) {
		GinkgoHelper()

		indexIn(dir, "")
	}

	// storePath names the index file of a corpus, resolved from the corpus directory the
	// way every verb in this group resolves it.
	storePath := func(dir string) string {
		GinkgoHelper()

		prior, err := os.Getwd()
		Expect(err).ToNot(HaveOccurred())
		Expect(os.Chdir(dir)).To(Succeed())
		defer func() {
			Expect(os.Chdir(prior)).To(Succeed())
		}()

		cfg, err := config.ParseConfigFileForMode("agent.yaml", config.ModeMCP)
		Expect(err).ToNot(HaveOccurred())

		abs, err := filepath.Abs(rag.StorePath(cfg, ""))
		Expect(err).ToNot(HaveOccurred())

		return abs
	}

	BeforeEach(func() {
		// Resolved, because the session takes its absolute --config from the working
		// directory and macOS hands that back with the symlinks followed. A spec
		// comparing the two needs them spelled the same way.
		resolved, err := filepath.EvalSymlinks(GinkgoT().TempDir())
		Expect(err).ToNot(HaveOccurred())
		tmp = resolved

		configPath = writeCorpus(filepath.Join(tmp, "corpus"), agentYAML)
		index(filepath.Join(tmp, "corpus"))

		// A corpus nobody has indexed, which setup names on stderr and prints for
		// anyway.
		writeCorpus(filepath.Join(tmp, "unbuilt"), agentYAML)

		// --store-dir carries no default, and fisk writes a flag's target only when the
		// flag has one, so a value one spec passes would reach every spec after it. A
		// binary parses once and starts empty here.
		knowledgeStoreDir = ""

		// Every spec runs from a directory holding neither a configuration nor an
		// index, and every relative path below resolves against it.
		prior, err := os.Getwd()
		Expect(err).ToNot(HaveOccurred())
		Expect(os.Chdir(tmp)).To(Succeed())
		DeferCleanup(func() {
			Expect(os.Chdir(prior)).To(Succeed())
		})
	})

	// run parses args as the fisk binary does and returns what the command wrote to
	// each stream along with the exit it asked for.
	run := func(args ...string) (string, string, int) {
		GinkgoHelper()

		outR, outW, err := os.Pipe()
		Expect(err).ToNot(HaveOccurred())

		errR, errW, err := os.Pipe()
		Expect(err).ToNot(HaveOccurred())

		priorOut, priorErr := os.Stdout, os.Stderr
		os.Stdout, os.Stderr = outW, errW

		// After the swap: fisk takes its error writer from os.Stderr as it is built.
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

		os.Stdout, os.Stderr = priorOut, priorErr
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

	setup := func(args ...string) (string, string, int) {
		GinkgoHelper()

		return run(append([]string{"knowledge", "agent", "setup", "--config", builtConfig}, args...)...)
	}

	// settings decodes the fenced fragment of a prompt, reading it out of the fence the
	// model is told to read it out of, so the object a spec asserts on is the object the
	// model gets.
	settings := func(prompt string) setupSettings {
		GinkgoHelper()

		_, rest, found := strings.Cut(prompt, "```json\n")
		Expect(found).To(BeTrue(), "the prompt carries a fenced json fragment")

		body, _, found := strings.Cut(rest, "\n```")
		Expect(found).To(BeTrue(), "the fence is closed")

		var out setupSettings
		Expect(json.Unmarshal([]byte(body), &out)).To(Succeed())

		return out
	}

	// anchor reads the prefix the prompt tells the model to match an installed entry on,
	// which stands on a line of its own under the step that introduces it.
	anchor := func(prompt string) string {
		GinkgoHelper()

		_, rest, found := strings.Cut(prompt, "starts with\n\n")
		Expect(found).To(BeTrue(), "the prompt names a prefix to match on")

		line, _, found := strings.Cut(rest, "\n")
		Expect(found).To(BeTrue())

		return strings.TrimSpace(line)
	}

	// command reads the one hook command line out of the fragment.
	command := func(prompt string) string {
		GinkgoHelper()

		s := settings(prompt)
		Expect(s.Hooks.UserPromptSubmit).To(HaveLen(1))
		Expect(s.Hooks.UserPromptSubmit[0].Hooks).To(HaveLen(1))

		return s.Hooks.UserPromptSubmit[0].Hooks[0].Command
	}

	It("Should refuse to run without a config and say the flag picks the working directory", func() {
		_, stderr, code := run("knowledge", "agent", "setup")

		Expect(code).ToNot(Equal(0))
		Expect(stderr).To(ContainSubstring("--config is required here and has no default"))
	})

	// A hook runs under a login shell's environment rather than the one the operator
	// tuned, so a bare name on the emitted line can reach a different binary or none,
	// and a relative --config resolves against a directory Claude Code chose.
	It("Should name the running binary and the config by absolute path", func() {
		exe, err := os.Executable()
		Expect(err).ToNot(HaveOccurred())

		stdout, _, code := setup()
		Expect(code).To(Equal(0))

		line := command(stdout)
		Expect(line).To(Equal(exe + " rag agent hook --config " + configPath))
		Expect(configPath).To(Equal(filepath.Join(tmp, builtConfig)))
		Expect(filepath.IsAbs(exe)).To(BeTrue())
		Expect(filepath.IsAbs(configPath)).To(BeTrue())
	})

	It("Should carry a fragment that parses, with one command entry and a timeout", func() {
		stdout, _, code := setup()
		Expect(code).To(Equal(0))

		entry := settings(stdout).Hooks.UserPromptSubmit[0].Hooks[0]
		Expect(entry.Type).To(Equal("command"))
		Expect(entry.Timeout).To(Equal(knowledgeSetupTimeout))
		Expect(entry.Timeout).To(BeNumerically(">", 0))
	})

	It("Should resolve a relative logfile against the directory the command ran in", func() {
		stdout, _, code := setup("--logfile", "hook.log")
		Expect(code).To(Equal(0))

		Expect(command(stdout)).To(HaveSuffix(" --logfile " + filepath.Join(tmp, "hook.log")))
	})

	It("Should emit only the tuning flags the operator gave", func() {
		stdout, _, code := setup()
		Expect(code).To(Equal(0))

		bare := command(stdout)
		Expect(bare).ToNot(ContainSubstring("--top-k"))
		Expect(bare).ToNot(ContainSubstring("--per-doc"))
		Expect(bare).ToNot(ContainSubstring("--min-words"))
		Expect(bare).ToNot(ContainSubstring("--auto"))
		Expect(bare).ToNot(ContainSubstring("--logfile"))
		Expect(bare).ToNot(ContainSubstring("--stopwords"))
		Expect(bare).ToNot(ContainSubstring("--store-dir"))

		stdout, _, code = setup("--top-k", "4", "--per-doc", "1", "--min-words", "5", "--auto")
		Expect(code).To(Equal(0))

		tuned := command(stdout)
		Expect(tuned).To(ContainSubstring(" --top-k 4"))
		Expect(tuned).To(ContainSubstring(" --per-doc 1"))
		Expect(tuned).To(ContainSubstring(" --min-words 5"))
		Expect(tuned).To(ContainSubstring(" --auto"))
	})

	// The tag word lives in prompthook.TagName, which the pattern Parse matches with
	// and the block's own preamble line are built from too. A prompt spelling it out
	// would go stale the next time the tag is renamed.
	It("Should render the tag through prompthook.TagName", func() {
		stdout, _, code := setup()
		Expect(code).To(Equal(0))

		Expect(stdout).To(ContainSubstring("<" + prompthook.TagName + ">"))
		Expect(stdout).To(ContainSubstring("<" + prompthook.TagName + " word word>"))
	})

	It("Should tell the model to merge rather than replace, and to confirm with /hooks", func() {
		stdout, _, code := setup()
		Expect(code).To(Equal(0))

		Expect(stdout).To(ContainSubstring("hooks.UserPromptSubmit"))
		Expect(stdout).To(ContainSubstring("Append"))
		Expect(stdout).To(ContainSubstring("/hooks"))
		Expect(stdout).To(ContainSubstring(".claude/settings.json"))
		Expect(stdout).To(ContainSubstring("~/.claude/settings.json"))
	})

	// Installing the hook before the first index is a reasonable order to work in, so
	// the note goes to stderr and the prompt prints anyway.
	It("Should name an index nobody has built without withholding the prompt", func() {
		stdout, stderr, code := run("knowledge", "agent", "setup", "--config", unbuiltConfig)

		Expect(code).To(Equal(0))
		Expect(stderr).To(ContainSubstring("has not been built yet"))
		Expect(stderr).To(ContainSubstring(filepath.Join(tmp, unbuiltConfig)))
		Expect(command(stdout)).To(ContainSubstring("rag agent hook --config " + filepath.Join(tmp, unbuiltConfig)))

		stdout, stderr, code = setup()
		Expect(code).To(Equal(0))
		Expect(stderr).To(BeEmpty())
		Expect(stdout).ToNot(BeEmpty())
	})

	// Claude Code runs the entry's command through a shell, so the spec runs it through
	// one too. A directory name holding a space splits into two arguments unquoted, and
	// one holding $(...) or a backtick runs whatever it spells; both reach the hook as a
	// --config it cannot open, which is the one fault the hook reports out loud. Reaching
	// the corpus is what says the quoting held, and the marker file the substitution
	// would create says the shell ran none of it.
	It("Should quote a path a shell would split or substitute", func() {
		marker := filepath.Join(tmp, "substituted")
		dir := "my $(touch " + marker + ") `id` it's corpus"

		corpus := filepath.Join(tmp, dir)
		writeCorpus(corpus, agentYAML)

		stdout, _, code := run("knowledge", "agent", "setup", "--config", filepath.Join(dir, "agent.yaml"))
		Expect(code).To(Equal(0))

		// The emitted line names this test binary, which takes none of these arguments,
		// so the binary word is swapped for a script printing the argument vector the
		// shell built. What the shell did to the rest of the line is the whole question.
		argv := filepath.Join(tmp, "argv.sh")
		Expect(os.WriteFile(argv, []byte("#!/bin/sh\nfor a in \"$@\"; do echo \"$a\"; done\n"), 0o755)).To(Succeed())

		exe, err := os.Executable()
		Expect(err).ToNot(HaveOccurred())

		line := command(stdout)
		prefix := prompthook.ShellWord(exe)
		Expect(line).To(HavePrefix(prefix))
		line = prompthook.ShellWord(argv) + strings.TrimPrefix(line, prefix)

		out, err := exec.Command("/bin/sh", "-c", line).Output()
		Expect(err).ToNot(HaveOccurred())

		args := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
		Expect(args).To(Equal([]string{"rag", "agent", "hook", "--config", filepath.Join(corpus, "agent.yaml")}))

		_, err = os.Stat(marker)
		Expect(os.IsNotExist(err)).To(BeTrue(), "the shell ran no substitution out of the path")
		Expect(string(out)).ToNot(ContainSubstring("uid="))
	})

	// --store-dir decides where the index lives, so a line omitting it names an index the
	// hook never reads. The hook passes a missing index over in silence, so the operator
	// would get nothing on every message and an error nowhere.
	It("Should emit --store-dir, including a value that arrived by environment", func() {
		base := filepath.Join(tmp, "storebase")
		Expect(os.MkdirAll(base, 0o755)).To(Succeed())

		indexIn(filepath.Join(tmp, "corpus"), base)

		stdout, stderr, code := setup("--store-dir", base)
		Expect(code).To(Equal(0))
		Expect(stderr).To(BeEmpty(), "the index under --store-dir is built")
		Expect(command(stdout)).To(ContainSubstring(" --store-dir " + base))

		// Envar rather than a typed flag, because a hook runs under a login shell's
		// environment and the value has to be on the line rather than looked for there.
		GinkgoT().Setenv("FISK_AI_STORE_DIR", base)

		stdout, _, code = setup()
		Expect(code).To(Equal(0))
		Expect(command(stdout)).To(ContainSubstring(" --store-dir " + base))
	})

	// A tuned stopword list is what an operator arrives at with preview, and the hook
	// resolves the flag against the directory holding --config.
	It("Should emit --stopwords, resolved against the config directory", func() {
		corpus := filepath.Join(tmp, "corpus")
		Expect(os.WriteFile(filepath.Join(corpus, "stop.txt"), []byte("cache\n"), 0o644)).To(Succeed())

		stdout, _, code := setup("--stopwords", "stop.txt")
		Expect(code).To(Equal(0))

		Expect(command(stdout)).To(ContainSubstring(" --stopwords " + filepath.Join(corpus, "stop.txt")))
	})

	// The hook swallows an index that will not open: errKnowledgeIndexOpen routes to
	// silence and it exits 0, because a reindex fixes it. A setup that refused to print
	// would withhold the prompt over a fault the hook never reports, for a corpus whose
	// hook runs.
	It("Should print the prompt for an index that will not open", func() {
		broken := filepath.Join(tmp, "broken")
		writeCorpus(broken, agentYAML)
		index(broken)

		// Overwritten rather than deleted, so the file is there and rag.StoreExists says
		// the index is built while every open of it fails.
		Expect(os.WriteFile(storePath(broken), []byte("not a database"), 0o644)).To(Succeed())

		stdout, stderr, code := run("knowledge", "agent", "setup", "--config", "broken/agent.yaml")

		Expect(code).To(Equal(0))
		Expect(stderr).To(BeEmpty())
		Expect(command(stdout)).To(ContainSubstring("rag agent hook --config " + filepath.Join(broken, "agent.yaml")))
	})

	// The command changes whenever a flag changes or the binary moves, so a model told to
	// match on the whole string installs a second hook for one corpus.
	It("Should give the model an anchor that survives a change of tuning", func() {
		exe, err := os.Executable()
		Expect(err).ToNot(HaveOccurred())

		want := exe + " rag agent hook --config " + configPath

		stdout, _, code := setup()
		Expect(code).To(Equal(0))
		Expect(anchor(stdout)).To(Equal(want))

		stdout, _, code = setup("--auto", "--top-k", "4", "--logfile", "hook.log")
		Expect(code).To(Equal(0))

		Expect(anchor(stdout)).To(Equal(want), "the anchor holds when the tuning changes")
		Expect(command(stdout)).To(HavePrefix(want), "and it is a prefix of the command it matches")
		Expect(command(stdout)).ToNot(Equal(want), "which the tuned command is longer than")
	})

	It("Should tell the model what to do with a file holding no hooks key", func() {
		stdout, _, code := setup()
		Expect(code).To(Equal(0))

		Expect(stdout).To(ContainSubstring("no hooks key"))
	})
})
