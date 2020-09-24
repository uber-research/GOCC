// modified from https://github.com/golang/tools/blob/master/go/ssa/lift.go#L78
package cfg

import (
	"golang.org/x/tools/go/ssa"
)

type domFrontier [][]*ssa.BasicBlock

func (df domFrontier) add(u, v *ssa.BasicBlock) {
	p := &df[u.Index]
	*p = append(*p, v)
}

// build builds the dominance frontier df for the dominator (sub)tree
// rooted at u, using the Cytron et al. algorithm.
//
// TODO(adonovan): opt: consider Berlin approach, computing pruned SSA
// by pruning the entire IDF computation, rather than merely pruning
// the DF -> IDF step.
func (df domFrontier) build(u *ssa.BasicBlock) {
	// Encounter each node u in postorder of dom tree.
	for _, child := range u.Dominees() {
		df.build(child)
	}
	for _, vb := range u.Succs {
		if vb.Idom() != u {
			df.add(u, vb)
		}
	}
	for _, w := range u.Dominees() {
		for _, vb := range df[w.Index] {
			// TODO(adonovan): opt: use word-parallel bitwise union.
			if vb.Idom() != u {
				df.add(u, vb)
			}
		}
	}
}

func buildDomFrontier(fn *ssa.Function) domFrontier {
	df := make(domFrontier, len(fn.Blocks))
	df.build(fn.Blocks[0])
	if fn.Recover != nil {
		df.build(fn.Recover)
	}
	return df
}
