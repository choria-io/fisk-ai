//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package file

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestRunStateFile(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Internal/RunState/File")
}
