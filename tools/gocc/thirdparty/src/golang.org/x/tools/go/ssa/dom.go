// Copyright 2013 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package ssa

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

import (
	"bytes"
	"fmt"
	"math/big"
	"os"
	"sort"
	"sync"
)

// Idom returns the block that immediately dominates b:
// its parent in the dominator tree, if any.
// Neither the entry node (b.Index==0) nor recover node
// (b==b.Parent().Recover()) have a parent.
//
func (b *BasicBlock) Idom() *BasicBlock { return b.dom.idom }

// Dominees returns the list of blocks that b immediately dominates:
// its children in the dominator tree.
//
func (b *BasicBlock) Dominees() []*BasicBlock { return b.dom.children }

// Dominates reports whether b dominates c.
func (b *BasicBlock) Dominates(c *BasicBlock) bool {
	return b.dom.pre <= c.dom.pre && c.dom.post <= b.dom.post
}

type byDomPreorder []*BasicBlock

func (a byDomPreorder) Len() int           { return len(a) }
func (a byDomPreorder) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }
func (a byDomPreorder) Less(i, j int) bool { return a[i].dom.pre < a[j].dom.pre }

// DomPreorder returns a new slice containing the blocks of f in
// dominator tree preorder.
//
func (f *Function) DomPreorder() []*BasicBlock {
	n := len(f.Blocks)
	order := make(byDomPreorder, n)
	copy(order, f.Blocks)
	sort.Sort(order)
	return order
}

type byDomPostorder []*BasicBlock

func (a byDomPostorder) Len() int           { return len(a) }
func (a byDomPostorder) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }
func (a byDomPostorder) Less(i, j int) bool { return a[i].dom.post < a[j].dom.post }

// byDomPostorder returns a new slice containing the blocks of f in
// dominator tree postorder.
//
func (f *Function) DomPostorder() []*BasicBlock {
	n := len(f.Blocks)
	order := make(byDomPostorder, n)
	copy(order, f.Blocks)
	sort.Sort(order)
	return order
}

// Ipdom returns the block that immediately post-dominates b:
// its parent in the post-dominator tree, if any.
// The exit nodesand  recover nodes have no parent.
func (b *BasicBlock) Ipdom() *BasicBlock { return b.pdom.idom }

// PostDominees returns the list of blocks that b immediately post-dominates:
// its children in the post-dominator tree.
//
func (b *BasicBlock) PostDominees() []*BasicBlock { return b.pdom.children }

// PostDominates reports whether b post-dominates c.
func (b *BasicBlock) PostDominates(c *BasicBlock) bool {
	return b.pdom.pre <= c.pdom.pre && c.pdom.post <= b.pdom.post
}

type byPostdomPreorder []*BasicBlock

func (a byPostdomPreorder) Len() int           { return len(a) }
func (a byPostdomPreorder) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }
func (a byPostdomPreorder) Less(i, j int) bool { return a[i].pdom.pre < a[j].pdom.pre }

// PostdomPreorder returns a new slice containing the blocks of f in
// dominator tree preorder.
//
func (f *Function) PostdomPreorder() []*BasicBlock {
	n := len(f.Blocks)
	order := make(byPostdomPreorder, n)
	copy(order, f.Blocks)
	sort.Sort(order)
	return order
}

type byPostdomPostorder []*BasicBlock

func (a byPostdomPostorder) Len() int           { return len(a) }
func (a byPostdomPostorder) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }
func (a byPostdomPostorder) Less(i, j int) bool { return a[i].pdom.post < a[j].pdom.post }

// byDomPostorder returns a new slice containing the blocks of f in
// dominator tree postorder.
//
func (f *Function) PostdomPostorder() []*BasicBlock {
	n := len(f.Blocks)
	order := make(byPostdomPostorder, n)
	copy(order, f.Blocks)
	sort.Sort(order)
	return order
}

// domInfo contains a BasicBlock's dominance information.
type domInfo struct {
	idom      *BasicBlock   // immediate dominator (parent in domtree)
	children  []*BasicBlock // nodes immediately dominated by this one
	pre, post int32         // pre- and post-order numbering within domtree
}

// ltState holds the working state for Lengauer-Tarjan algorithm
// (during which domInfo.pre is repurposed for CFG DFS preorder number).
type ltState struct {
	isPostdom bool
	// Each slice is indexed by b.Index.
	sdom     []*BasicBlock // b's semidominator
	parent   []*BasicBlock // b's parent in DFS traversal of CFG
	ancestor []*BasicBlock // b's ancestor with least sdom
}

// dfs implements the depth-first search part of the LT algorithm.
func (lt *ltState) dfsDom(v *BasicBlock, i int32, preorder []*BasicBlock) int32 {
	preorder[i] = v
	v.dom.pre = i // For now: DFS preorder of spanning tree of CFG
	i++
	lt.sdom[v.Index] = v
	lt.link(nil, v)
	for _, w := range v.Succs {
		if lt.sdom[w.Index] == nil {
			lt.parent[w.Index] = v
			i = lt.dfsDom(w, i, preorder)
		}
	}
	return i
}

// dfs implements the depth-first search part of the LT algorithm.
func (lt *ltState) dfsPostdom(v *BasicBlock, i int32, preorder []*BasicBlock) int32 {
	preorder[i] = v
	v.pdom.pre = i // For now: DFS preorder of spanning tree of CFG
	i++
	lt.sdom[v.Index] = v
	lt.link(nil, v)
	for _, w := range v.Preds {
		if lt.sdom[w.Index] == nil {
			lt.parent[w.Index] = v
			i = lt.dfsPostdom(w, i, preorder)
		}
	}
	return i
}

// dfs implements the depth-first search part of the LT algorithm.
func (lt *ltState) dfs(v *BasicBlock, i int32, preorder []*BasicBlock) int32 {
	if lt.isPostdom {
		return lt.dfsPostdom(v, i, preorder)
	} else {
		return lt.dfsDom(v, i, preorder)
	}
}

// eval implements the EVAL part of the LT algorithm.
func (lt *ltState) eval(v *BasicBlock) *BasicBlock {
	// TODO(adonovan): opt: do path compression per simple LT.
	u := v
	for ; lt.ancestor[v.Index] != nil; v = lt.ancestor[v.Index] {
		if lt.isPostdom {
			if lt.sdom[v.Index].pdom.pre < lt.sdom[u.Index].pdom.pre {
				u = v
			}
		} else {
			if lt.sdom[v.Index].dom.pre < lt.sdom[u.Index].dom.pre {
				u = v
			}
		}

	}
	return u
}

// link implements the LINK part of the LT algorithm.
func (lt *ltState) link(v, w *BasicBlock) {
	lt.ancestor[w.Index] = v
}

// buildDomTree computes the dominator tree of f using the LT algorithm.
// Precondition: all blocks are reachable (e.g. optimizeBlocks has been run).
//
func buildDomTree(f *Function) {
	// The step numbers refer to the original LT paper; the
	// reordering is due to Georgiadis.

	// Clear any previous domInfo.
	for _, b := range f.Blocks {
		b.dom = domInfo{}
	}

	n := len(f.Blocks)
	// Allocate space for 5 contiguous [n]*BasicBlock arrays:
	// sdom, parent, ancestor, preorder, buckets.
	space := make([]*BasicBlock, 5*n)
	lt := ltState{
		isPostdom: false,
		sdom:      space[0:n],
		parent:    space[n : 2*n],
		ancestor:  space[2*n : 3*n],
	}

	// Step 1.  Number vertices by depth-first preorder.
	preorder := space[3*n : 4*n]
	root := f.Blocks[0]
	prenum := lt.dfs(root, 0, preorder)
	recover := f.Recover
	if recover != nil {
		lt.dfs(recover, prenum, preorder)
	}

	buckets := space[4*n : 5*n]
	copy(buckets, preorder)

	// In reverse preorder...
	for i := int32(n) - 1; i > 0; i-- {
		w := preorder[i]

		// Step 3. Implicitly define the immediate dominator of each node.
		for v := buckets[i]; v != w; v = buckets[v.dom.pre] {
			u := lt.eval(v)
			if lt.sdom[u.Index].dom.pre < i {
				v.dom.idom = u
			} else {
				v.dom.idom = w
			}
		}

		// Step 2. Compute the semidominators of all nodes.
		lt.sdom[w.Index] = lt.parent[w.Index]
		for _, v := range w.Preds {
			u := lt.eval(v)
			if lt.sdom[u.Index].dom.pre < lt.sdom[w.Index].dom.pre {
				lt.sdom[w.Index] = lt.sdom[u.Index]
			}
		}

		lt.link(lt.parent[w.Index], w)

		if lt.parent[w.Index] == lt.sdom[w.Index] {
			w.dom.idom = lt.parent[w.Index]
		} else {
			buckets[i] = buckets[lt.sdom[w.Index].dom.pre]
			buckets[lt.sdom[w.Index].dom.pre] = w
		}
	}

	// The final 'Step 3' is now outside the loop.
	for v := buckets[0]; v != root; v = buckets[v.dom.pre] {
		v.dom.idom = root
	}

	// Step 4. Explicitly define the immediate dominator of each
	// node, in preorder.
	for _, w := range preorder[1:] {
		if w == root || w == recover {
			w.dom.idom = nil
		} else {
			if w.dom.idom != lt.sdom[w.Index] {
				w.dom.idom = w.dom.idom.dom.idom
			}
			// Calculate Children relation as inverse of Idom.
			w.dom.idom.dom.children = append(w.dom.idom.dom.children, w)
		}
	}

	pre, post := numberDomTree(root, 0, 0)
	if recover != nil {
		numberDomTree(recover, pre, post)
	}

	// printDomTreeDot(os.Stderr, f)        // debugging
	// printDomTreeText(os.Stderr, root, 0) // debugging

	if f.Prog.mode&SanityCheckFunctions != 0 {
		sanityCheckDomTree(f)
	}
}

func exitBlocks(f *Function) []*BasicBlock {
	exists := make([]*BasicBlock, 0)
	for _, b := range f.Blocks {
		if len(b.Succs) == 0 {
			exists = append(exists, b)
		}
	}
	return exists
}

func dumpFn(fn *Function) {
	for _, b := range fn.Blocks {
		fmt.Printf("%s:\n", b.String())
		for _, i := range b.Instrs {
			if v, ok := i.(Value); ok {
				fmt.Printf("\t[%-20T] %s = %s\n", i, v.Name(), i)
			} else {
				fmt.Printf("\t[%-20T] %s\n", i, i)
			}
		}
	}
}

func dumptree(b *BasicBlock) {
	if b == nil {
		return
	}
	fmt.Printf("\n %d: ", b.Index)
	for _, i := range b.pdom.children {
		fmt.Printf(" %d ", i.Index)
	}
	for _, i := range b.pdom.children {
		dumptree(i)
	}
}

var ioLocker sync.Mutex

func dumpPDomTree(f *Function, root *BasicBlock) {
	var s []int
	for _, v := range f.PostdomPreorder() {
		s = append(s, v.Index)
	}

	var t []int
	for _, v := range f.PostdomPostorder() {
		t = append(t, v.Index)
	}

	ioLocker.Lock()
	dumpFn(f)
	fmt.Printf("\n%v PostdomPreorder = %v\n", f, s)
	fmt.Printf("\n%v PostdomPostorder = %v\n", f, t)
	fmt.Printf("\n%v root Children = %v\n", f, root.pdom.children)
	dumptree(root)
	ioLocker.Unlock()
}

// buildPostdomTree computes the post-dominator tree of f using the LT algorithm.
// Precondition: all blocks are reachable (e.g. optimizeBlocks has been run).
//
func buildPostdomTree(f *Function) {

	var dummyExit BasicBlock
	dummyExit.Preds = exitBlocks(f)
	root := &dummyExit
	root.Index = 0

	if len(dummyExit.Preds) == 0 {
		return
	}

	// Increase everybody's Index by 1
	for _, b := range f.Blocks {
		b.Index++
	}
	defer func() {
		// Decrease everybody's Index by 1
		for _, b := range f.Blocks {
			b.Index--
		}
	}()

	var preorder []*BasicBlock
	var space []*BasicBlock
	n := len(f.Blocks) + 1 // +1 for a dummy exit node we create
	var lt ltState

	noPathToExit := map[*BasicBlock]struct{}{}
	for {
		// Clear any previous pdomInfo.
		for _, b := range f.Blocks {
			b.pdom = domInfo{}
		}
		root.pdom = domInfo{}

		// Allocate space for 5 contiguous [n]*BasicBlock arrays:
		// sdom, parent, ancestor, preorder, buckets.
		space = make([]*BasicBlock, 5*n)
		lt = ltState{
			isPostdom: true,
			sdom:      space[0:n],
			parent:    space[n : 2*n],
			ancestor:  space[2*n : 3*n],
		}
		// Step 1.  Number vertices by depth-first preorder on the reverse CGF.
		preorder = space[3*n : 4*n]
		prenum := lt.dfs(root, 0, preorder)

		if prenum == int32(len(preorder)) {
			break
		}

		reachable := map[*BasicBlock]struct{}{}
		for _, b := range preorder[0:prenum] {
			reachable[b] = struct{}{}
		}
		for _, b := range f.Blocks {
			if _, ok := reachable[b]; !ok {
				noPathToExit[b] = struct{}{}
				dummyExit.Preds = append(dummyExit.Preds, b)
				break
			}
		}
	}

	buckets := space[4*n : 5*n]
	copy(buckets, preorder)

	// In reverse preorder...
	for i := int32(n) - 1; i > 0; i-- {
		w := preorder[i]

		// Step 3. Implicitly define the immediate dominator of each node.
		for v := buckets[i]; v != w; v = buckets[v.pdom.pre] {
			u := lt.eval(v)
			if lt.sdom[u.Index].pdom.pre < i {
				v.pdom.idom = u
			} else {
				v.pdom.idom = w
			}
		}
		// Step 2. Compute the semidominators of all nodes.

		lt.sdom[w.Index] = lt.parent[w.Index]

		for _, v := range w.Succs {
			u := lt.eval(v)
			if lt.sdom[u.Index].pdom.pre < lt.sdom[w.Index].pdom.pre {
				lt.sdom[w.Index] = lt.sdom[u.Index]
			}
		}
		// One special case! If we had a added a path to dummyExit, iterate over it
		if _, ok := noPathToExit[w]; ok {
			v := root
			u := lt.eval(v)
			if lt.sdom[u.Index].pdom.pre < lt.sdom[w.Index].pdom.pre {
				lt.sdom[w.Index] = lt.sdom[u.Index]
			}
		}

		lt.link(lt.parent[w.Index], w)

		if lt.parent[w.Index] == lt.sdom[w.Index] {
			w.pdom.idom = lt.parent[w.Index]
		} else {
			buckets[i] = buckets[lt.sdom[w.Index].pdom.pre]
			buckets[lt.sdom[w.Index].pdom.pre] = w
		}
	}
	// The final 'Step 3' is now outside the loop.
	for v := buckets[0]; v != root; v = buckets[v.pdom.pre] {
		v.pdom.idom = root
	}

	// Step 4. Explicitly define the immediate dominator of each
	// node, in preorder.
	for _, w := range preorder[1:] {
		if w == root /* TODO when we support recobver || w == recover */ {
			w.pdom.idom = nil
		} else {
			if w.pdom.idom != lt.sdom[w.Index] {
				w.pdom.idom = w.pdom.idom.pdom.idom
			}
			// Calculate Children relation as inverse of Idom.
			w.pdom.idom.pdom.children = append(w.pdom.idom.pdom.children, w)
		}
	}

	numberPostdomTree(root, 0, 0)

	// Final Hackery
	// 1. Set ipdom of all nodes that have root as thier ipdom as nil because root will not exist in reality
	for _, b := range f.Blocks {
		if b.pdom.idom == root {
			b.pdom.idom = nil
		}
	}
	// 2. Reduce pre order by 1 and increase the post order by 1
	for _, b := range f.Blocks {
		if b.pdom.idom == root {
			b.pdom.pre--
			b.pdom.post++
		}
	}
	// Debugging
	// dumpPDomTree(f, root)
}

// numberDomTree sets the pre- and post-order numbers of a depth-first
// traversal of the dominator tree rooted at v.  These are used to
// answer dominance queries in constant time.
//
func numberDomTree(v *BasicBlock, pre, post int32) (int32, int32) {
	v.dom.pre = pre
	pre++
	for _, child := range v.dom.children {
		pre, post = numberDomTree(child, pre, post)
	}
	v.dom.post = post
	post++
	return pre, post
}

// numberPostdomTree sets the pre- and post-order numbers of a depth-first
// traversal of the post-dominator tree rooted at v.  These are used to
// answer dominance queries in constant time.
//
func numberPostdomTree(v *BasicBlock, pre, post int32) (int32, int32) {
	v.pdom.pre = pre
	pre++
	for _, child := range v.pdom.children {
		pre, post = numberPostdomTree(child, pre, post)
	}
	v.pdom.post = post
	post++
	return pre, post
}

// Testing utilities ----------------------------------------

// sanityCheckDomTree checks the correctness of the dominator tree
// computed by the LT algorithm by comparing against the dominance
// relation computed by a naive Kildall-style forward dataflow
// analysis (Algorithm 10.16 from the "Dragon" book).
//
func sanityCheckDomTree(f *Function) {
	n := len(f.Blocks)

	// D[i] is the set of blocks that dominate f.Blocks[i],
	// represented as a bit-set of block indices.
	D := make([]big.Int, n)

	one := big.NewInt(1)

	// all is the set of all blocks; constant.
	var all big.Int
	all.Set(one).Lsh(&all, uint(n)).Sub(&all, one)

	// Initialization.
	for i, b := range f.Blocks {
		if i == 0 || b == f.Recover {
			// A root is dominated only by itself.
			D[i].SetBit(&D[0], 0, 1)
		} else {
			// All other blocks are (initially) dominated
			// by every block.
			D[i].Set(&all)
		}
	}

	// Iteration until fixed point.
	for changed := true; changed; {
		changed = false
		for i, b := range f.Blocks {
			if i == 0 || b == f.Recover {
				continue
			}
			// Compute intersection across predecessors.
			var x big.Int
			x.Set(&all)
			for _, pred := range b.Preds {
				x.And(&x, &D[pred.Index])
			}
			x.SetBit(&x, i, 1) // a block always dominates itself.
			if D[i].Cmp(&x) != 0 {
				D[i].Set(&x)
				changed = true
			}
		}
	}

	// Check the entire relation.  O(n^2).
	// The Recover block (if any) must be treated specially so we skip it.
	ok := true
	for i := 0; i < n; i++ {
		for j := 0; j < n; j++ {
			b, c := f.Blocks[i], f.Blocks[j]
			if c == f.Recover {
				continue
			}
			actual := b.Dominates(c)
			expected := D[j].Bit(i) == 1
			if actual != expected {
				fmt.Fprintf(os.Stderr, "dominates(%s, %s)==%t, want %t\n", b, c, actual, expected)
				ok = false
			}
		}
	}

	preorder := f.DomPreorder()
	for _, b := range f.Blocks {
		if got := preorder[b.dom.pre]; got != b {
			fmt.Fprintf(os.Stderr, "preorder[%d]==%s, want %s\n", b.dom.pre, got, b)
			ok = false
		}
	}

	if !ok {
		panic("sanityCheckDomTree failed for " + f.String())
	}

}

// Printing functions ----------------------------------------

// printDomTree prints the dominator tree as text, using indentation.
func printDomTreeText(buf *bytes.Buffer, v *BasicBlock, indent int) {
	fmt.Fprintf(buf, "%*s%s\n", 4*indent, "", v)
	for _, child := range v.dom.children {
		printDomTreeText(buf, child, indent+1)
	}
}

// printDomTreeDot prints the dominator tree of f in AT&T GraphViz
// (.dot) format.
func printDomTreeDot(buf *bytes.Buffer, f *Function) {
	fmt.Fprintln(buf, "//", f)
	fmt.Fprintln(buf, "digraph domtree {")
	for i, b := range f.Blocks {
		v := b.dom
		fmt.Fprintf(buf, "\tn%d [label=\"%s (%d, %d)\",shape=\"rectangle\"];\n", v.pre, b, v.pre, v.post)
		// TODO(adonovan): improve appearance of edges
		// belonging to both dominator tree and CFG.

		// Dominator tree edge.
		if i != 0 {
			fmt.Fprintf(buf, "\tn%d -> n%d [style=\"solid\",weight=100];\n", v.idom.dom.pre, v.pre)
		}
		// CFG edges.
		for _, pred := range b.Preds {
			fmt.Fprintf(buf, "\tn%d -> n%d [style=\"dotted\",weight=0];\n", pred.dom.pre, v.pre)
		}
	}
	fmt.Fprintln(buf, "}")
}

// printPdomTreeDot prints the dominator tree of f in AT&T GraphViz
// (.dot) format.
func printPdomTreeDot(buf *bytes.Buffer, f *Function) {
	fmt.Fprintln(buf, "//", f)
	fmt.Fprintln(buf, "digraph domtree {")
	for i, b := range f.Blocks {
		v := b.pdom
		fmt.Fprintf(buf, "\tn%d [label=\"%s (%d, %d)\",shape=\"rectangle\"];\n", v.pre, b, v.pre, v.post)
		// TODO(adonovan): improve appearance of edges
		// belonging to both dominator tree and CFG.

		// Dominator tree edge.
		if i != 0 {
			fmt.Fprintf(buf, "\tn%d -> n%d [style=\"solid\",weight=100];\n", v.idom.pdom.pre, v.pre)
		}
		// CFG edges.
		for _, succ := range b.Succs {
			fmt.Fprintf(buf, "\tn%d -> n%d [style=\"dotted\",weight=0];\n", succ.pdom.pre, v.pre)
		}
	}
	fmt.Fprintln(buf, "}")
}
