package cfg

import "golang.org/x/tools/go/ssa"

func intersect(a, b []int) (c []int) {
	m := make(map[int]bool)
	for _, item := range a {
		m[item] = true
	}
	for _, item := range b {
		if _, ok := m[item]; ok {
			c = append(c, item)
		}
	}
	return
}

func union(a, b []int) []int {
	m := make(map[int]bool)
	for _, item := range a {
		m[item] = true
	}
	for _, item := range b {
		if _, ok := m[item]; !ok {
			a = append(a, item)
		}
	}
	return a
}

func equal(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i, v := range a {
		if v != b[i] {
			return false
		}
	}
	return true
}

// return a set of basic block, whose last instruction is return or panic
func GetExit(f *ssa.Function) map[int]bool {
	exitMap := make(map[int]bool)
	for _, blk := range f.Blocks {
		ins := blk.Instrs[len(blk.Instrs)-1]
		_, isPanic := ins.(*ssa.Panic)
		_, isReturn := ins.(*ssa.Return)
		if isPanic || isReturn {
			exitMap[blk.Index] = true
		}
	}
	return exitMap
}

// PostDominators returns all post-dominators for all nodes in g. It does not
// prune for strict post-dominators, immediate post-dominators etc.
//
// A post-dominates B if and only if all paths from B travel through A.
// https://github.com/gonum/graph/blob/master/path/control_flow.go#L74
func PostDominators(g *ssa.Function) map[int][]int {
	exitBlks := GetExit(g)
	allNodes := make([]int, 5)
	nlist := g.Blocks
	dominators := make(map[int][]int, len(nlist))
	for _, node := range nlist {
		allNodes = append(allNodes, node.Index)
	}

	for _, node := range nlist {
		dominators[node.Index] = make([]int, 5)
		if exitBlks[node.Index] {
			dominators[node.Index] = append(dominators[node.Index], node.Index)
		} else {
			dominators[node.Index] = allNodes
		}
	}

	for somethingChanged := true; somethingChanged; {
		somethingChanged = false
		for _, node := range nlist {
			if exitBlks[node.Index] {
				continue
			}
			succs := node.Succs
			if len(succs) == 0 {
				continue
			}
			tmp := make([]int, len(dominators[succs[0].Index]))
			copy(tmp, dominators[succs[0].Index])
			for _, succ := range succs[1:] {
				tmp = intersect(tmp, dominators[succ.Index])
			}

			dom := make([]int, 5)
			dom = append(dom, node.Index)

			dom = union(dom, tmp)
			if !equal(dom, dominators[node.Index]) {
				dominators[node.Index] = dom
				somethingChanged = true
			}
		}
	}

	return dominators
}
