//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package memory

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("frontmatter", func() {
	roundTrip := func(description, content string) {
		GinkgoHelper()
		data, err := Serialize(description, content)
		Expect(err).ToNot(HaveOccurred())
		gotDesc, gotContent := Parse(data)
		Expect(gotDesc).To(Equal(description))
		Expect(gotContent).To(Equal(content))
	}

	It("Should round-trip a simple value", func() {
		roundTrip("how the build is wired", "step one\nstep two\n")
	})

	It("Should round-trip a body that itself contains a --- line", func() {
		roundTrip("notes", "intro\n---\nmore\n")
	})

	It("Should round-trip a description with YAML-significant characters", func() {
		roundTrip("key: value, \"quoted\" and - dashed", "body")
	})

	It("Should round-trip empty content", func() {
		roundTrip("just a description", "")
	})

	It("Should treat a headerless document as body with an empty description", func() {
		desc, content := Parse([]byte("no frontmatter here\n"))
		Expect(desc).To(BeEmpty())
		Expect(content).To(Equal("no frontmatter here\n"))
	})

	It("Should treat an unterminated header as body, not frontmatter", func() {
		desc, content := Parse([]byte("---\ndescription: dangling\n"))
		Expect(desc).To(BeEmpty())
		Expect(content).To(Equal("---\ndescription: dangling\n"))
	})
})
