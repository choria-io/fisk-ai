//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestMainPackage(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Main")
}

var _ = Describe("splitRemoteDescription", func() {
	It("Should split a model description into its text and tags", func() {
		desc, tags := splitRemoteDescription("List known consumers\n\nTags: scope:user, impact:ro")
		Expect(desc).To(Equal("List known consumers"))
		Expect(tags).To(Equal("scope:user, impact:ro"))
	})

	It("Should handle a description that is only a tags block", func() {
		desc, tags := splitRemoteDescription("Tags: impact:ro")
		Expect(desc).To(Equal(""))
		Expect(tags).To(Equal("impact:ro"))
	})

	It("Should leave a plain description untouched", func() {
		desc, tags := splitRemoteDescription("Just a description")
		Expect(desc).To(Equal("Just a description"))
		Expect(tags).To(Equal(""))
	})
})
