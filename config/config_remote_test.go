//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package config

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Remote tools and server mode", func() {
	Describe("RemoteToolHost.EffectiveAlias", func() {
		It("Should use the alias when set", func() {
			Expect(RemoteToolHost{Name: "orders", Alias: "db"}.EffectiveAlias()).To(Equal("db"))
		})

		It("Should fall back to the host name", func() {
			Expect(RemoteToolHost{Name: "orders"}.EffectiveAlias()).To(Equal("orders"))
		})
	})

	Describe("ValidateForMode", func() {
		base := func() *Config {
			cfg := &Config{
				ApplicationPath: "/bin/true",
				Identity:        "agent",
				NatsContext:     "ctx",
				SystemPrompt:    "do things",
			}
			cfg.LLM.Model = ModelClaudeSonnet46
			Expect(cfg.prepare()).To(Succeed())

			return cfg
		}

		Context("server mode", func() {
			It("Should accept a config with an application, identity and nats_context", func() {
				Expect(ValidateForMode(base(), ModeServer)).To(Succeed())
			})

			It("Should require nats_context", func() {
				cfg := base()
				cfg.NatsContext = ""
				err := ValidateForMode(cfg, ModeServer)
				Expect(err).To(MatchError(ContainSubstring("nats_context is required for the a2a server")))
			})
		})

		Context("agent mode with remote tools", func() {
			It("Should require nats_context when remote_tools is set", func() {
				cfg := base()
				cfg.NatsContext = ""
				cfg.RemoteTools = []RemoteToolHost{{Name: "orders"}}
				err := ValidateForMode(cfg, ModeAgent)
				Expect(err).To(MatchError(ContainSubstring("nats_context is required when remote_tools is set")))
			})

			It("Should accept remote_tools when nats_context is set", func() {
				cfg := base()
				cfg.RemoteTools = []RemoteToolHost{{Name: "orders"}}
				Expect(ValidateForMode(cfg, ModeAgent)).To(Succeed())
			})
		})

		Context("remote tool host validation", func() {
			It("Should reject a host name that is not a legal subject token", func() {
				cfg := base()
				cfg.RemoteTools = []RemoteToolHost{{Name: "bad.name"}}
				err := ValidateForMode(cfg, ModeAgent)
				Expect(err).To(MatchError(ContainSubstring("remote_tools host name \"bad.name\" is invalid")))
			})

			It("Should reject an invalid alias", func() {
				cfg := base()
				cfg.RemoteTools = []RemoteToolHost{{Name: "orders", Alias: "has space"}}
				err := ValidateForMode(cfg, ModeAgent)
				Expect(err).To(MatchError(ContainSubstring("invalid alias")))
			})

			It("Should reject an exclude-by-tag filter, which discovery cannot honor", func() {
				cfg := base()
				cfg.RemoteTools = []RemoteToolHost{{Name: "orders", Exclude: &ToolFilter{Tags: []string{"impact:rw"}}}}
				err := ValidateForMode(cfg, ModeAgent)
				Expect(err).To(MatchError(ContainSubstring("exclude.tags filter, which cannot be honored")))
			})

			It("Should allow an exclude-by-name filter", func() {
				cfg := base()
				cfg.RemoteTools = []RemoteToolHost{{Name: "orders", Exclude: &ToolFilter{Tools: []string{"^danger_"}}}}
				Expect(ValidateForMode(cfg, ModeAgent)).To(Succeed())
			})
		})
	})
})
