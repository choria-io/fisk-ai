//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package functool

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestFunctool(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Internal/Toolkit/Functool")
}
