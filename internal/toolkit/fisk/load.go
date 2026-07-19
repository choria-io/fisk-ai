//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package fisk

import (
	"github.com/choria-io/fisk-ai/config"
)

// LoadTools introspects the application, then always strips ai:deny tools before
// applying the configured include and exclude filters. The unconditional first
// pass is what enforces ai:deny even when neither filter is set.
func LoadTools(cfg *config.Config) ([]*FiskCommandTool, error) {
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
func ServedTools(cfg *config.Config) ([]*FiskCommandTool, error) {
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
func filterExposed(tools []*FiskCommandTool, sel *config.ExposedToolSelection) ([]*FiskCommandTool, error) {
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
