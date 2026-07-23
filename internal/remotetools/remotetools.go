//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

// Package remotetools discovers, filters and names the tools an agent imports
// from remote agents over A2A. It is the policy layer over the a2a transport:
// given the configured hosts it decides which advertised tools survive each
// host's include/exclude filters and what model-facing name each ends up with,
// prefixing on clash. It returns its findings as data (HostImport); formatting
// and warning are left to the caller so the same import feeds both the run path
// and the info command.
package remotetools

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/choria-io/fisk-ai/config"
	"github.com/choria-io/fisk-ai/internal/a2a"
	_ "github.com/choria-io/fisk-ai/internal/a2a/nats"
	"github.com/choria-io/fisk-ai/internal/conns"
	"github.com/choria-io/fisk-ai/internal/toolkit/functool"
)

// remoteToolNamePattern is the character set an imported tool's local name must
// match to be usable as a model tool name; it mirrors the server and MCP rule. A
// name outside it (after the alias prefix) is skipped rather than silently broken.
var remoteToolNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// HostImport is the outcome of importing one remote tool host. Discovery and
// filtering fill Kept (the descriptors that survived the host's include/exclude);
// the later naming pass fills Tools (the built, named tools) and Skipped. A
// non-nil Err means discovery or filtering failed, in which case Kept is empty.
type HostImport struct {
	Host       config.RemoteToolHost
	Err        error
	RTT        time.Duration
	Discovered int
	Version    string
	Kept       []a2a.ToolDescriptor
	Tools      []*functool.Tool
	Skipped    []string
	// IgnoredIncludeTags is set when the host's include filter used tags, which
	// discovery cannot carry, so the tag part of the filter was ignored.
	IgnoredIncludeTags bool
}

// ImportForRun discovers and imports every configured remote tool host for an
// agent run. It is strict: a host that fails discovery aborts with an error, as
// does a name collision, since the prompt may depend on the missing tools. It
// returns the imported tools in host order, a name-keyed dispatch map, and the
// per-host outcomes so the caller can warn about ignored tag filters, skipped
// tools, or a host that contributed nothing. The client stays owned by the
// caller, which must keep it open for the run and close it afterwards.
func ImportForRun(ctx context.Context, client *a2a.Client, cfg *config.Config, taken map[string]bool) ([]*functool.Tool, map[string]*functool.Tool, []HostImport, error) {
	imports := importRemoteToolHosts(ctx, client, cfg.RemoteTools)

	for _, imp := range imports {
		if imp.Err != nil {
			return nil, nil, imports, fmt.Errorf("importing tools from remote agent %q on context %q: %w", imp.Host.Name, cfg.NatsContext, imp.Err)
		}
	}

	remoteByName, err := resolveRemoteTools(taken, imports, client)
	if err != nil {
		return nil, nil, imports, err
	}

	var remoteTools []*functool.Tool
	for i := range imports {
		remoteTools = append(remoteTools, imports[i].Tools...)
	}

	return remoteTools, remoteByName, imports, nil
}

// DiscoverForInfo discovers and resolves the configured remote tool hosts for the
// info command, using the same naming as a run (taken holds the local tool and
// built-in names) so info shows the names a run would actually use. Unlike the
// run path it is best-effort: it returns nil when there are no hosts, and returns
// the connection error (with nil imports) on a connection failure so the caller
// can warn and still show the local tools. A collision is not fatal here; the
// colliding tools are recorded as skipped for the caller to render. The tools are
// built with no invoker, since info never calls them.
func DiscoverForInfo(cfg *config.Config, taken map[string]bool) ([]HostImport, error) {
	if len(cfg.RemoteTools) == 0 {
		return nil, nil
	}

	provider, err := conns.Connect(cfg.NatsContext, cfg.Identity)
	if err != nil {
		return nil, err
	}
	defer provider.Close()

	transport, err := a2a.NewTransport(cfg.A2ATransport(), provider, a2a.TransportConfig{Identity: cfg.Identity, Timeout: cfg.LLM.Budget.CallTimeoutParsed})
	if err != nil {
		return nil, err
	}

	client, err := a2a.NewClient(transport, cfg.Identity)
	if err != nil {
		return nil, err
	}

	imports := importRemoteToolHosts(context.Background(), client, cfg.RemoteTools)
	_, _ = resolveRemoteTools(taken, imports, nil)

	return imports, nil
}

// importRemoteToolHosts discovers and filters each configured host. Discovery
// failures are captured per host in the returned slice rather than aborting, so a
// caller can decide whether to fail (run) or warn (info). Naming the surviving
// tools is a separate, global step (resolveRemoteTools), because the choice of
// whether to prefix a tool depends on the whole set, not on one host.
func importRemoteToolHosts(ctx context.Context, client *a2a.Client, hosts []config.RemoteToolHost) []HostImport {
	out := make([]HostImport, 0, len(hosts))

	for _, host := range hosts {
		out = append(out, importHost(ctx, client, host))
	}

	return out
}

// importHost discovers a single host and applies its include/exclude filters,
// recording the surviving descriptors in Kept. It does not name or build tools;
// that is done globally afterwards.
func importHost(ctx context.Context, client *a2a.Client, host config.RemoteToolHost) HostImport {
	result := HostImport{Host: host}

	start := time.Now()
	card, err := client.Discover(ctx, host.Name)
	result.RTT = time.Since(start)
	if err != nil {
		result.Err = err
		return result
	}

	result.Version = card.Version
	result.Discovered = len(card.Tools)

	kept, ignoredIncludeTags, err := filterDescriptors(card.Tools, host)
	if err != nil {
		result.Err = err
		return result
	}
	result.Kept = kept
	result.IgnoredIncludeTags = ignoredIncludeTags

	return result
}

// resolveRemoteTools assigns every imported tool its final model-facing name and
// builds it, prefixing a tool's name with its host alias only when the bare name
// would clash. The decision is deterministic and independent of discovery and
// iteration order: a bare name is prefixed when a local tool or built-in already
// holds it, or when more than one host exposes that bare name, so the same inputs
// always prefix the same names between runs. A residual collision (two final
// names that still coincide, or a prefixed name that lands on a local tool) is
// detected by counting final names rather than by which tool was processed first;
// the colliding tools are skipped and reported, and an error is returned so the
// run path can fail closed. taken holds the names already claimed by local tools
// and built-ins. The imports slice is updated in place with the built Tools and
// any Skipped notes.
func resolveRemoteTools(taken map[string]bool, imports []HostImport, invoker a2a.RemoteInvoker) (map[string]*functool.Tool, error) {
	// Count bare names across every host so a name exposed by more than one host
	// is prefixed for all of them, symmetrically.
	bareCount := map[string]int{}
	for i := range imports {
		if imports[i].Err != nil {
			continue
		}
		for _, d := range imports[i].Kept {
			bareCount[d.Name]++
		}
	}

	finalName := func(host config.RemoteToolHost, bare string) string {
		if taken[bare] || bareCount[bare] > 1 {
			return fmt.Sprintf("%s_%s", host.EffectiveAlias(), bare)
		}

		return bare
	}

	// Count the resulting final names so a residual collision is found globally,
	// not by processing order.
	finalCount := map[string]int{}
	for i := range imports {
		if imports[i].Err != nil {
			continue
		}
		for _, d := range imports[i].Kept {
			finalCount[finalName(imports[i].Host, d.Name)]++
		}
	}

	byName := map[string]*functool.Tool{}
	var collisions []string

	for i := range imports {
		if imports[i].Err != nil {
			continue
		}
		host := imports[i].Host
		for _, d := range imports[i].Kept {
			name := finalName(host, d.Name)

			if !remoteToolNamePattern.MatchString(name) {
				imports[i].Skipped = append(imports[i].Skipped, fmt.Sprintf("%s (invalid name %q)", d.Name, name))
				continue
			}
			if taken[name] || finalCount[name] > 1 {
				imports[i].Skipped = append(imports[i].Skipped, fmt.Sprintf("%s (name %q collides)", d.Name, name))
				collisions = append(collisions, fmt.Sprintf("%q (agent %q)", name, host.Name))
				continue
			}

			rt, err := a2a.NewRemoteTool(name, host.Name, d, invoker)
			if err != nil {
				imports[i].Skipped = append(imports[i].Skipped, fmt.Sprintf("%s (%v)", d.Name, err))
				continue
			}

			byName[name] = rt
			imports[i].Tools = append(imports[i].Tools, rt)
		}
	}

	if len(collisions) > 0 {
		return byName, fmt.Errorf("imported tool name collision: %s; set a distinct alias on the remote_tools host", strings.Join(collisions, ", "))
	}

	return byName, nil
}

// filterDescriptors applies a host's include/exclude filters to discovered tool
// descriptors, matching on the tool name only. Include restricts to matching
// names; exclude removes matching names; include runs first. A tag-based include
// cannot be honored (discovery carries no tags) and is reported via the returned
// bool so the caller can warn; a tag-based exclude is rejected at config parse
// time and so cannot appear here.
func filterDescriptors(tools []a2a.ToolDescriptor, host config.RemoteToolHost) ([]a2a.ToolDescriptor, bool, error) {
	kept := tools
	ignoredIncludeTags := false

	if host.Include != nil && len(host.Include.Tools) > 0 {
		matched, err := matchDescriptors(kept, host.Include.Tools)
		if err != nil {
			return nil, false, err
		}
		kept = matched
	}
	if host.Include != nil && len(host.Include.Tags) > 0 {
		ignoredIncludeTags = true
	}

	if host.Exclude != nil && len(host.Exclude.Tools) > 0 {
		matched, err := matchDescriptors(kept, host.Exclude.Tools)
		if err != nil {
			return nil, false, err
		}
		kept = subtractDescriptors(kept, matched)
	}

	return kept, ignoredIncludeTags, nil
}

// matchDescriptors returns the descriptors whose name matches any of the
// patterns.
func matchDescriptors(tools []a2a.ToolDescriptor, patterns []string) ([]a2a.ToolDescriptor, error) {
	res := make([]*regexp.Regexp, 0, len(patterns))
	for _, p := range patterns {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, fmt.Errorf("invalid remote tool filter pattern %q: %w", p, err)
		}
		res = append(res, re)
	}

	var out []a2a.ToolDescriptor
	for _, t := range tools {
		for _, re := range res {
			if re.MatchString(t.Name) {
				out = append(out, t)
				break
			}
		}
	}

	return out, nil
}

// subtractDescriptors returns the descriptors in tools that are not in remove,
// compared by name.
func subtractDescriptors(tools, remove []a2a.ToolDescriptor) []a2a.ToolDescriptor {
	removed := make(map[string]bool, len(remove))
	for _, t := range remove {
		removed[t.Name] = true
	}

	var out []a2a.ToolDescriptor
	for _, t := range tools {
		if !removed[t.Name] {
			out = append(out, t)
		}
	}

	return out
}
