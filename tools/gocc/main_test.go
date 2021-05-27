//Copyright (c) 2020 Uber Technologies, Inc.
//
//Licensed under the Uber Non-Commercial License (the "License");
//you may not use this file except in compliance with the License.
//You may obtain a copy of the License at the root directory of this project.
//
//See the License for the specific language governing permissions and
//limitations under the License.
package main

import (
	"fmt"
	"io/ioutil"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPkgs(t *testing.T) {
	tempDir := t.TempDir()

	tt := []struct {
		loc      string
		rev      string
		name     string
		diffFile string
	}{
		{
			loc:      "./testdata/src/github.com/uber-go/tally",
			name:     "tally",
			rev:      "164eb6a3c0d4c8c82e5a63a8a12d506f0c9b2637",
			diffFile: "testdata/tally.diff",
		},
	}

	for _, tc := range tt {
		_ = tc
		g := NewGOCC(false, false, false, true, true, false, tc.loc)
		g.Process()

		// Get diff
		cmd := []string{"git", "diff"}
		diff, err := exec.Command(cmd[0], cmd[1:]...).Output()
		fmt.Printf("cmd= %v", cmd)
		//require.NoError(t, err)
		diffStr := string(diff)
		recordedDiff, err := ioutil.ReadFile(tc.diffFile)
		require.NoError(t, err)
		recordedDiffStr := string(recordedDiff)
		require.Equal(t, recordedDiffStr, diffStr)
	}
}
