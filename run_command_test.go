//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package main

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("validateRunFlags", func() {
	// The run flags are package globals; reset the ones this suite touches after each
	// case so cases stay independent.
	AfterEach(func() {
		chatMode = false
		checkpoint = false
		resumeID = ""
	})

	It("Should accept --chat combined with --checkpoint", func() {
		chatMode = true
		checkpoint = true
		Expect(validateRunFlags()).To(Succeed())
	})

	It("Should accept --chat combined with --resume, ignoring it in favor of the stored session", func() {
		chatMode = true
		resumeID = "abc"
		Expect(validateRunFlags()).To(Succeed())
	})

	It("Should accept --chat on its own", func() {
		chatMode = true
		Expect(validateRunFlags()).To(Succeed())
	})
})
