// Copyright 2013 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package ssa

import (
	"fmt"
	"testing"
)

// This file defines algorithms related to dominance.

// Dominator tree construction ----------------------------------------
//
// We use the algorithm described in Lengauer & Tarjan. 1979.  A fast
// algorithm for finding dominators in a flowgraph.
// http://doi.acm.org/10.1145/357062.357071
//
// We also apply the optimizations to SLT described in Georgiadis et
// al, Finding Dominators in Practice, JGAA 2006,
// http://jgaa.info/accepted/2006/GeorgiadisTarjanWerneck2006.10.1.pdf
// to avoid the need for buckets of size > 1.

func TestBuildPostdomTreeRecover(t *testing.T) {
	var f Function
	b := []*BasicBlock{&BasicBlock{}, &BasicBlock{}, &BasicBlock{}, &BasicBlock{}, &BasicBlock{}}
	b[0].Index = 0
	b[1].Index = 1
	b[2].Index = 2
	b[3].Index = 3
	b[4].Index = 4

	b[0].Succs = []*BasicBlock{b[1], b[3]}
	//	b[0].Preds = []*BasicBloc{}

	//b[1].Succs = []*BasicBloc{}
	b[1].Preds = []*BasicBlock{b[0], b[3]}

	//	b[2].Succs = []*BasicBloc{}
	b[2].Preds = []*BasicBlock{b[3]}

	b[3].Succs = []*BasicBlock{b[1], b[2]}
	b[3].Preds = []*BasicBlock{b[0]}
	f.Blocks = b
	f.Recover = b[4]
	//buildDomTree(&f)
	buildPostdomTree(&f)

	for _, v := range f.Blocks {
		if v.Ipdom() != nil {
			t.Error(fmt.Errorf("In correct parent, expected nil"))
		}
	}

	b = []*BasicBlock{&BasicBlock{}, &BasicBlock{}, &BasicBlock{}, &BasicBlock{}, &BasicBlock{}, &BasicBlock{}}
	b[0].Index = 0
	b[1].Index = 1
	b[2].Index = 2
	b[3].Index = 3
	b[4].Index = 4
	b[5].Index = 5

	b[0].Succs = []*BasicBlock{b[3]}

	b[1].Succs = []*BasicBlock{b[4], b[5]}
	b[1].Preds = []*BasicBlock{b[3]}

	b[2].Preds = []*BasicBlock{b[3]}

	b[3].Succs = []*BasicBlock{b[1], b[2]}
	b[3].Preds = []*BasicBlock{b[0], b[5]}

	b[4].Preds = []*BasicBlock{b[1]}

	b[5].Succs = []*BasicBlock{b[3]}
	b[5].Preds = []*BasicBlock{b[1]}

	f.Blocks = b
	//buildDomTree(&f)
	buildPostdomTree(&f)

	for _, v := range []*BasicBlock{b[1], b[2], b[3], b[4]} {
		if v.Ipdom() != nil {
			t.Error(fmt.Errorf("In correct parent, expected nil"))
		}
	}

	for _, v := range []*BasicBlock{b[0], b[5]} {
		if v.Ipdom() != b[3] {
			t.Error(fmt.Errorf("In correct parent, expected nil"))
		}
	}
}
