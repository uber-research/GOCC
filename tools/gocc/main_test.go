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
	"io/ioutil"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
)

func TestPkgs(t *testing.T) {

	type testcases struct {
		loc         string
		rev         string
		name        string
		diffFile    string
		rewriteTest bool
	}

	tt := []testcases{
		{
			loc:         "./testdata/src/github.com/uber-go/tally",
			name:        "tally",
			rev:         "164eb6a3c0d4c8c82e5a63a8a12d506f0c9b2637",
			diffFile:    "testdata/tally.diff",
			rewriteTest: false,
		},
		{
			loc:      "./testdata/src/go.uber.org/zap",
			name:     "zap",
			rev:      "5b4722d3797ca2e22f3c16dbe71a97b9c7783c98",
			diffFile: "testdata/zap.diff",
		},

		{
			loc:         "./testdata/src/github.com/patrickmn/go-cache/",
			name:        "go-cache",
			rev:         "5b4722d3797ca2e22f3c16dbe71a97b9c7783c98",
			diffFile:    "testdata/gocache.diff",
			rewriteTest: true,
		},

		{
			loc:         "./testdata/src/github.com/VictoriaMetrics/fastcache",
			name:        "fastcache",
			rev:         "5b4722d3797ca2e22f3c16dbe71a97b9c7783c98",
			diffFile:    "testdata/fastcache.diff",
			rewriteTest: false,
		},
	}

	ignoreTestId := map[int]empty{15: emptyStruct}
	for i := 1; i <= 31; i++ {
		if _, ok := ignoreTestId[i]; ok {
			continue
		}
		id := strconv.Itoa(i)
		tt = append(tt, testcases{
			loc:         "./testdata/test" + id + ".go",
			name:        "test" + id,
			diffFile:    "testdata/test" + id + ".diff",
			rewriteTest: false,
		})
	}

	var replaceRegex = regexp.MustCompile(`(?s)\nindex(.*?)\n`)

	for _, tc := range tt {
		tc := tc
		defer func() {
			cmd := []string{"git", "checkout", "--", tc.loc}
			exec.Command(cmd[0], cmd[1:]...).Output()
		}()
		g := NewGOCC(false, tc.rewriteTest, false, true, false, tc.loc)
		g.Process()
		// Get diff
		cmd := []string{"git", "diff", tc.loc}
		diff, _ := exec.Command(cmd[0], cmd[1:]...).Output()
		diffStr := string(diff)
		//		fmt.Printf("diffStr old = %s\n", diffStr)

		diffStr = replaceRegex.ReplaceAllLiteralString(diffStr, "")
		//		fmt.Printf("diffStr = %s\n", diffStr)

		// ignore lines starting with index

		recordedDiff, err := ioutil.ReadFile(tc.diffFile)
		require.NoError(t, err)
		recordedDiffStr := string(recordedDiff)
		recordedDiffStr = replaceRegex.ReplaceAllLiteralString(recordedDiffStr, "")
		//	fmt.Printf("recordedDiffStr = %s\n", recordedDiffStr)

		require.Equal(t, recordedDiffStr, diffStr)
		// Must build after edits

		pkgConfig := &packages.Config{Mode: packages.LoadAllSyntax, Tests: true}
		loadPath := tc.loc
		if info, err := os.Stat(loadPath); err == nil && info.IsDir() {
			loadPath += "/."
		}
		initial, err := packages.Load(pkgConfig, loadPath)
		require.NoError(t, err)
		if packages.PrintErrors(initial) > 0 {
			require.Fail(t, "packages contain errors")
		}
		// Create SSA packages for all well-typed packages.
		prog, pkgs := ssautil.Packages(initial, ssa.GlobalDebug)
		_ = prog
		// Build SSA code for the well-typed initial packages.
		for _, p := range pkgs {
			if p != nil {
				p.Build()
			}
		}
	}
}
