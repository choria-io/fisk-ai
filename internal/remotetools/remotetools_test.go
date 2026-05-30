//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package remotetools

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/choria-io/fisk-ai/a2a"
	"github.com/choria-io/fisk-ai/config"
	"github.com/choria-io/fisk-ai/internal/util"
)

func TestRemoteTools(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "RemoteTools")
}

func descriptors(names ...string) []a2a.ToolDescriptor {
	out := make([]a2a.ToolDescriptor, len(names))
	for i, n := range names {
		out[i] = a2a.ToolDescriptor{Name: n}
	}

	return out
}

func descriptorNames(tools []a2a.ToolDescriptor) []string {
	out := make([]string, len(tools))
	for i, t := range tools {
		out[i] = t.Name
	}

	return out
}

var _ = Describe("filterDescriptors", func() {
	tools := descriptors("stream_info", "stream_report", "consumer_ls", "danger_rm")

	It("Should keep everything when no filter is set", func() {
		kept, ignored, err := filterDescriptors(tools, config.RemoteToolHost{Name: "h"})
		Expect(err).NotTo(HaveOccurred())
		Expect(ignored).To(BeFalse())
		Expect(descriptorNames(kept)).To(ConsistOf("stream_info", "stream_report", "consumer_ls", "danger_rm"))
	})

	It("Should restrict to include name patterns", func() {
		host := config.RemoteToolHost{Name: "h", Include: &config.ToolFilter{Tools: []string{"^stream_"}}}
		kept, _, err := filterDescriptors(tools, host)
		Expect(err).NotTo(HaveOccurred())
		Expect(descriptorNames(kept)).To(ConsistOf("stream_info", "stream_report"))
	})

	It("Should remove exclude name patterns", func() {
		host := config.RemoteToolHost{Name: "h", Exclude: &config.ToolFilter{Tools: []string{"^danger_"}}}
		kept, _, err := filterDescriptors(tools, host)
		Expect(err).NotTo(HaveOccurred())
		Expect(descriptorNames(kept)).To(ConsistOf("stream_info", "stream_report", "consumer_ls"))
	})

	It("Should apply include before exclude", func() {
		host := config.RemoteToolHost{Name: "h",
			Include: &config.ToolFilter{Tools: []string{"^stream_"}},
			Exclude: &config.ToolFilter{Tools: []string{"_report$"}},
		}
		kept, _, err := filterDescriptors(tools, host)
		Expect(err).NotTo(HaveOccurred())
		Expect(descriptorNames(kept)).To(ConsistOf("stream_info"))
	})

	It("Should report an ignored include tag filter", func() {
		host := config.RemoteToolHost{Name: "h", Include: &config.ToolFilter{Tags: []string{"public"}}}
		_, ignored, err := filterDescriptors(tools, host)
		Expect(err).NotTo(HaveOccurred())
		Expect(ignored).To(BeTrue())
	})

	It("Should return an error for an invalid pattern", func() {
		host := config.RemoteToolHost{Name: "h", Include: &config.ToolFilter{Tools: []string{"("}}}
		_, _, err := filterDescriptors(tools, host)
		Expect(err).To(MatchError(ContainSubstring("invalid remote tool filter pattern")))
	})
})

var _ = Describe("resolveRemoteTools", func() {
	host := func(name string, tools ...string) HostImport {
		return HostImport{Host: config.RemoteToolHost{Name: name}, Kept: descriptors(tools...)}
	}

	toolNames := func(imp HostImport) []string {
		out := make([]string, len(imp.Tools))
		for i, t := range imp.Tools {
			out[i] = t.Name()
		}
		return out
	}

	It("Should keep the bare name when there is no clash", func() {
		imports := []HostImport{host("nats", "stream_info", "consumer_ls")}
		byName, err := resolveRemoteTools(map[string]bool{}, imports, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(byName).To(HaveKey("stream_info"))
		Expect(byName).To(HaveKey("consumer_ls"))
		Expect(toolNames(imports[0])).To(ConsistOf("stream_info", "consumer_ls"))
	})

	It("Should prefix only the name that clashes with a local tool", func() {
		imports := []HostImport{host("nats", "stream_info", "consumer_ls")}
		taken := map[string]bool{"stream_info": true}
		byName, err := resolveRemoteTools(taken, imports, nil)
		Expect(err).NotTo(HaveOccurred())
		// consumer_ls is unique so it stays bare; stream_info clashes so it is prefixed.
		Expect(byName).To(HaveKey("consumer_ls"))
		Expect(byName).To(HaveKey("nats_stream_info"))
		Expect(byName).NotTo(HaveKey("stream_info"))
	})

	It("Should prefix both hosts symmetrically when they share a bare name", func() {
		imports := []HostImport{
			{Host: config.RemoteToolHost{Name: "a"}, Kept: descriptors("dup", "only_a")},
			{Host: config.RemoteToolHost{Name: "b"}, Kept: descriptors("dup", "only_b")},
		}
		byName, err := resolveRemoteTools(map[string]bool{}, imports, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(byName).To(HaveKey("a_dup"))
		Expect(byName).To(HaveKey("b_dup"))
		Expect(byName).To(HaveKey("only_a"))
		Expect(byName).To(HaveKey("only_b"))
		Expect(byName).NotTo(HaveKey("dup"))
	})

	It("Should be deterministic regardless of host order", func() {
		taken := map[string]bool{"stream_info": true}
		forward := []HostImport{host("a", "stream_info", "x"), host("b", "stream_info", "y")}
		reverse := []HostImport{host("b", "stream_info", "y"), host("a", "stream_info", "x")}

		f, err := resolveRemoteTools(taken, forward, nil)
		Expect(err).NotTo(HaveOccurred())
		r, err := resolveRemoteTools(taken, reverse, nil)
		Expect(err).NotTo(HaveOccurred())

		keys := func(m map[string]*util.RemoteTool) []string {
			out := make([]string, 0, len(m))
			for k := range m {
				out = append(out, k)
			}
			return out
		}
		// stream_info is shared and also local, so both hosts prefix it; x and y are
		// unique and stay bare. The resolved set is identical either way.
		Expect(keys(f)).To(ConsistOf("a_stream_info", "b_stream_info", "x", "y"))
		Expect(keys(r)).To(ConsistOf("a_stream_info", "b_stream_info", "x", "y"))
	})

	It("Should error and skip when a prefixed name still collides", func() {
		imports := []HostImport{
			{Host: config.RemoteToolHost{Name: "a", Alias: "shared"}, Kept: descriptors("t")},
			{Host: config.RemoteToolHost{Name: "b", Alias: "shared"}, Kept: descriptors("t")},
		}
		_, err := resolveRemoteTools(map[string]bool{}, imports, nil)
		Expect(err).To(MatchError(ContainSubstring("collision")))
		// Both colliding tools are dropped, not arbitrarily kept.
		Expect(imports[0].Tools).To(BeEmpty())
		Expect(imports[1].Tools).To(BeEmpty())
	})
})
