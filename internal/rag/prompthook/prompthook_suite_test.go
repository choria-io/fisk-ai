//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

// The bootstrap lives in the external test package because ginkgo is dot imported
// and its Skip would collide with this package's own.
package prompthook_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestPromptHook(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Internal/RAG/PromptHook")
}
