//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/choria-io/fisk-ai/config"
	"github.com/choria-io/fisk-ai/internal/a2anats"
	"github.com/choria-io/fisk-ai/internal/remotetools"
)

// printRemoteToolStatus prints a per-host status block after the tool table so an
// operator can tell why a remote tool is or is not present: whether the host
// answered, how long it took, how many tools it advertised and how many were
// imported, and any ignored filters or skipped tools.
func printRemoteToolStatus(cfg *config.Config, imports []remotetools.HostImport) {
	if len(imports) == 0 {
		return
	}

	fmt.Println()
	fmt.Println("Remote tool hosts:")
	fmt.Println()
	for _, imp := range imports {
		if imp.Err != nil {
			fmt.Printf("  %s: UNAVAILABLE via context %q on %q after %s: %v\n",
				imp.Host.Name, cfg.NatsContext, a2anats.DiscoverySubject(imp.Host.Name), imp.RTT.Round(time.Millisecond), imp.Err)
			continue
		}

		fmt.Printf("  %s (%s): reachable in %s, advertised %d tool(s), imported %d as %q\n",
			imp.Host.Name, imp.Version, imp.RTT.Round(time.Millisecond), imp.Discovered, len(imp.Tools), imp.Host.EffectiveAlias())
		warnHostNotes(imp)
	}
}

// warnHostNotes emits the per-host warnings shared by run and info: an ignored
// tag-based include filter and any tools skipped during import.
func warnHostNotes(imp remotetools.HostImport) {
	if imp.IgnoredIncludeTags {
		fmt.Fprintf(os.Stderr, "warning: remote agent %q include filter uses tags, which discovery does not carry; the tag filter was ignored (filter by tool name instead)\n", imp.Host.Name)
	}
	if len(imp.Skipped) > 0 {
		fmt.Fprintf(os.Stderr, "warning: remote agent %q: skipped %s\n", imp.Host.Name, strings.Join(imp.Skipped, "; "))
	}
}
