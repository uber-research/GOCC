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
	"go/ast"
	"go/token"
	"log"
	"sort"

	"golang.org/x/tools/go/callgraph"
	"golang.org/x/tools/go/pointer"
	"golang.org/x/tools/go/ssa"
)

type luPointType int

const (
	LOCK          luPointType = iota
	UNLOCK                    = iota
	DEFER_UNLOCK              = iota
	WLOCK                     = iota
	WUNLOCK                   = iota
	DEFER_WUNLOCK             = iota
	RLOCK                     = iota
	RUNLOCK                   = iota
	DEFER_RUNLOCK             = iota
	UNKNOWN                   = iota
	TYPE_MAX                  = iota
)
const _intMax = int(^uint(0) >> 1)

type luPoint struct {
	astPath     []ast.Node
	ssaIns      ssa.Instruction
	callC       ssa.CallCommon
	insIdx      int
	pointerInfo pointer.Pointer
	kind        luPointType
	mutexVal    ssa.Value
	isPointer   bool
	idInFunc    int
	isLambda    bool
}

type luPair struct {
	l *luPoint
	u *luPoint
	r map[*ssa.BasicBlock]empty
	f *ssa.Function
}

func (lup *luPair) lPoint() *luPoint {
	return lup.l
}

func (lup *luPair) uPoint() *luPoint {
	return lup.u
}

func (lup *luPair) region() map[*ssa.BasicBlock]empty {
	return lup.r
}

func dfsRechableFunctions(root *ssa.Function, reachable map[*ssa.Function]empty, cg map[*ssa.Function]*callgraph.Node) {
	if _, ok := reachable[root]; ok {
		return
	}
	reachable[root] = emptyStruct
	node, ok := cg[root]
	if !ok {
		return
	}
	for _, e := range node.Out {
		dfsRechableFunctions(e.Callee.Func, reachable, cg)
	}
}

func (lup *luPair) checkRegionSafety(f *ssa.Function, summary map[*ssa.Function]*functionSummary, cg map[*ssa.Function]*callgraph.Node) bool {
	sefSummary := summary[f]
	cgNode, ok := cg[f]
	if !ok {
		panic("cg[f] nil!")
	}

	callSites := map[ssa.CallInstruction]empty{}
	for b, _ := range lup.r {
		// first, the block should have NO block-listed calls
		// second, the block should contain NO LUpoint that intersects with this LU-pair.
		for _, i := range b.Instrs {
			if unsafeInst(i, cgNode) {
				sefSummary.metric.intraRegionIO++
				return false
			}
			if pt, ok := sefSummary.insToPt[i]; ok {
				if pt.mayBeSameMutex(lup.l) || pt.mayBeSameMutex(lup.u) {
					sefSummary.metric.intraRegionAlias++
					return false
				}
			}

			if call, ok := i.(ssa.CallInstruction); ok {
				callSites[call] = emptyStruct
			}
		}
	}

	// Now, walk all reachable functions. None of them should have unsafeInst or mayAlias pt.l or pt.u.
	// collect all reachable functions.
	reachable := map[*ssa.Function]empty{}
	if cgNode != nil {
		for _, edge := range cgNode.Out {
			if _, ok := callSites[edge.Site]; ok {
				dfsRechableFunctions(edge.Callee.Func, reachable, cg)
			}
		}
	}
	for callee, _ := range reachable {
		if fSummary, ok := summary[callee]; !ok {
			panic("summary[callee] empty!")
		} else {
			if !fSummary.safeToCallFlat(lup.l, sefSummary) || !fSummary.safeToCallFlat(lup.u, sefSummary) {
				return false
			}
		}
	}
	return true
}

func blocksDomBy(b *ssa.BasicBlock, dominee map[*ssa.BasicBlock]empty) {
	if b == nil {
		return
	}
	for _, d := range b.Dominees() {
		dominee[d] = emptyStruct
		blocksDomBy(d, dominee)
	}
}

func blocksPDomBy(b *ssa.BasicBlock, pdominee map[*ssa.BasicBlock]empty) {
	if b == nil {
		return
	}
	for _, d := range b.PostDominees() {
		pdominee[d] = emptyStruct
		blocksPDomBy(d, pdominee)
	}
}

func blockIntersection(a, b map[*ssa.BasicBlock]empty) map[*ssa.BasicBlock]empty {
	result := map[*ssa.BasicBlock]empty{}
	for k, _ := range a {
		if _, ok := b[k]; ok {
			result[k] = emptyStruct
		}
	}
	return result
}

func blocksReachable(b *ssa.BasicBlock, reachable map[*ssa.BasicBlock]empty) {
	if b == nil {
		return
	}
	for _, succ := range b.Succs {
		if _, ok := reachable[succ]; !ok {
			reachable[succ] = emptyStruct
			blocksReachable(succ, reachable)
		}
	}
}

func enclosedRegion(l *luPoint, u *luPoint) map[*ssa.BasicBlock]empty {
	dummyBB := ssa.BasicBlock{}
	if u.isDefer() {
		for i, ins := range l.block().Instrs {
			if i > l.index() {
				dummyBB.Instrs = append(dummyBB.Instrs, ins)
			}
		}

		region := map[*ssa.BasicBlock]empty{}
		blocksReachable(l.block(), region)
		return region
	}

	// non-defer case

	// all instructions in l.block() from luPoint till instructions in u.block() before upoint belong to the protected region.
	// u ought to be a NON-defer for this
	if u.block() == l.block() {
		for i, ins := range u.block().Instrs {
			if i > l.index() && i < u.index() {
				dummyBB.Instrs = append(dummyBB.Instrs, ins)
			}
		}
	} else {
		for i, ins := range l.block().Instrs {
			if i > l.index() {
				dummyBB.Instrs = append(dummyBB.Instrs, ins)
			}
		}
		for i, ins := range u.block().Instrs {
			if i < u.index() {
				dummyBB.Instrs = append(dummyBB.Instrs, ins)
			}
		}
	}

	dominee := map[*ssa.BasicBlock]empty{}
	pdominee := map[*ssa.BasicBlock]empty{}
	blocksDomBy(l.block(), dominee)
	blocksPDomBy(u.block(), pdominee)
	region := blockIntersection(dominee, pdominee)
	if len(dummyBB.Instrs) > 0 {
		region[&dummyBB] = emptyStruct
	}
	return region

}

func (lup *luPair) calculateRegion() {
	// all basic block dominated by l and post-dominated by u are in the region.
	// Some extra instructions also fall in.

	// However, if the lock is a defer unlock, all blocks reachable by l are in the region.
	lup.r = enclosedRegion(lup.l, lup.u)
}

type empty struct{}

var emptyStruct = empty{}

func (l *luPoint) luType() luPointType {
	return l.kind
}

func (l *luPoint) ins() ssa.Instruction {
	return l.ssaIns
}

func (l *luPoint) pos() token.Pos {
	return l.callC.Pos()
}
func (p *luPoint) objName() string {
	return p.callC.Args[0].String()
}

func (p *luPoint) mutexValue() ssa.Value {
	//	fmt.Printf("mutexValue = %v, %v\n", p.callC.Args[0].Type(), p.callC.Args[0])
	return p.callC.Args[0]
}

func (l *luPoint) pointsToSet() pointer.PointsToSet {
	return l.pointerInfo.PointsTo()
}

func (l *luPoint) setAliasingPointer(p pointer.Pointer) {
	l.pointerInfo = p
}

func (l *luPoint) call() ssa.CallCommon {
	return l.callC
}

func (l *luPoint) nilLabels(u *luPoint) bool {
	a := l.pointerInfo.PointsTo().Labels()
	b := u.pointerInfo.PointsTo().Labels()
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return false
}

// TODO: investigate why some pointers have NIL labels.
// Sometimes, when two points even though point to the same lock, their lables are nil and end up being reported as NON intersecting.
/*
For example this tally code:
68 func (s *scope) Counter(name string) Counter {
269         name = s.sanitizer.Name(name)
270         if c, ok := s.counter(name); ok {
271                 return c
272         }
273
274         s.cm.Lock()
275         defer s.cm.Unlock()

To match more locks, we use this trick to match any two NIL labels set to be same.
However, dont use this logic when matching (rejecting) aliases in a region or between functions since it will reject more pairs.
*/
func (l *luPoint) mayBeSameMutexNilsAreSame(u *luPoint) bool {
	if l.mayBeSameMutex(u) {
		return true
	}
	return l.nilLabels(u)
}

func (l *luPoint) mayBeSameMutex(u *luPoint) bool {
	return l.pointerInfo.PointsTo().Intersects(u.pointerInfo.PointsTo())
}

func (l *luPoint) getOptiMethod() string {
	return luTypeToStr[l.luType()].optiMethodName
}

func (l *luPoint) getPromotedIdentifier() string {
	return luTypeToStr[l.luType()].promotedId
}

func (l *luPoint) isDefer() bool {
	return l.kind == DEFER_RUNLOCK || l.kind == DEFER_UNLOCK || l.kind == DEFER_WUNLOCK
}

func (l *luPoint) block() *ssa.BasicBlock {
	return l.ssaIns.Block()
}

func (l *luPoint) index() int {
	return l.insIdx
}

func blockToLockPointDescending(lPoints map[*luPoint]ssa.Instruction) map[*ssa.BasicBlock][]*luPoint {
	blockToLpt := make(map[*ssa.BasicBlock][]*luPoint)

	for lpt, _ := range lPoints {
		blk := lpt.block()
		blockToLpt[blk] = append(blockToLpt[blk], lpt)
	}

	// Now, order blockToLock slices by their instruction number HIGH TO LOW
	for _, v := range blockToLpt {
		sort.Slice(v, func(i, j int) bool {
			ii := v[i].index()
			jj := v[j].index()
			return ii > jj
		})
	}
	return blockToLpt
}

func blockToUnlockPointMapAscending(uPoints map[*luPoint]ssa.Instruction) map[*ssa.BasicBlock][]*luPoint {
	blockToUpt := make(map[*ssa.BasicBlock][]*luPoint)

	for upt, _ := range uPoints {
		blk := upt.block()
		blockToUpt[blk] = append(blockToUpt[blk], upt)
	}

	// Now, order blockToUpt slices by their instruction number LOW TO HIGH
	for _, v := range blockToUpt {
		sort.Slice(v, func(i, j int) bool {
			ii := v[i].index()
			jj := v[j].index()
			return ii < jj
		})
	}
	return blockToUpt
}

func orderLocksByPostOrderInDomtree(f *ssa.Function, lPoints map[*luPoint]ssa.Instruction) []*luPoint {
	postOrderDomTree := f.DomPostorder()
	blockToLUPt := blockToLockPointDescending(lPoints)

	var postOrderlPoints []*luPoint
	for _, b := range postOrderDomTree {
		if v, ok := blockToLUPt[b]; ok {
			for _, lpt := range v {
				postOrderlPoints = append(postOrderlPoints, lpt)
			}
		}
	}
	return postOrderlPoints
}

func nearestIntersectingUnlock(l *luPoint, pdom *ssa.BasicBlock, ups map[*ssa.BasicBlock][]*luPoint, alreadyMatched map[*luPoint]empty) (*luPoint, bool) {
	uPoints, ok := ups[pdom]
	if !ok {
		return nil, false
	}

	// if l and pdom are the same block, skip the unlock till l.
	skip := -1

	if l.block() == pdom {
		skip = l.index()
	}

	// nearest to farthest
	for i := 0; i < len(uPoints); i++ {
		u := uPoints[i]
		if u.index() < skip {
			continue
		}

		if _, ok := alreadyMatched[u]; ok {
			continue
		}

		// TODO: investigate more match nil Labels
		if l.mayBeSameMutexNilsAreSame(u) {
			return u, true
		}
	}
	return nil, false
}

func nearestIntersectingDeferUnlockBeforeLock(l *luPoint, dom *ssa.BasicBlock, d map[*ssa.BasicBlock][]*luPoint, alreadyMatched map[*luPoint]empty) (*luPoint, bool) {
	uPoints, ok := d[dom]
	if !ok {
		return nil, false
	}

	// if l and dom are the same block, skip the unlock after l.
	skip := _intMax

	if l.block() == dom {
		skip = l.index()
	}

	// nearest to farthest
	// The defer list is sorted in order of instructions, hence do reverse search
	for i := len(uPoints) - 1; i >= 0; i-- {
		if uPoints[i].index() > skip {
			continue
		}

		if _, ok := alreadyMatched[uPoints[i]]; ok {
			continue
		}

		if l.mayBeSameMutexNilsAreSame(uPoints[i]) {
			return uPoints[i], true
		}
	}
	return nil, false
}

func getNearestUpoint(l *luPoint, ups map[*ssa.BasicBlock][]*luPoint, dups map[*ssa.BasicBlock][]*luPoint, alreadyMatched map[*luPoint]empty) *luPoint {
	// Following situations arise.
	// 1. a uPoint() post-dominates l or
	// 2. a dPoint() post-dominates l or
	// 3. a dPoint() dominates l or

	// At most one defer is supported currently
	if len(dups) > 1 {
		panic("At most one defer is supported")
	}

	var match *luPoint
	blk := l.block()
	iPdom := blk // start from self

	// interblock
	for {
		if iPdom == nil {
			break
		}
		// 1. does blk DOMINATE iPDom?
		if !blk.Dominates(iPdom) {
			iPdom = iPdom.Ipdom()
			continue
		}

		// is there an unlock in iPdom
		u, oku := nearestIntersectingUnlock(l, iPdom, ups, alreadyMatched)
		// is there an unlock in iPdom
		d, okd := nearestIntersectingUnlock(l, iPdom, dups, alreadyMatched)

		if !oku && !okd {
			iPdom = iPdom.Ipdom()
			continue
		}

		// pick the nearest one
		if oku && okd {
			if u.index() < d.index() {
				match = u
			} else {
				match = d
			}
		} else if oku {
			match = u
		} else {
			match = d
		}
		break
	}

	// only one defer is possible
	if match != nil && (match.isDefer()) {
		return match
	}

	var deferBeforeLock *luPoint

	// Traverse Domtree looking for defer
	for dom := blk; dom != nil; dom = dom.Idom() {
		// 1. does blk POSTDOMINATE dom?
		if !blk.PostDominates(dom) {
			break
		}
		d, ok := nearestIntersectingDeferUnlockBeforeLock(l, dom, dups, alreadyMatched)
		if ok {
			deferBeforeLock = d
			break
		}
	}

	// if both are non nil, we really dont know whom to match. Simply return as no match.
	if match != nil && deferBeforeLock != nil {
		return nil
	}

	if match != nil {
		return match
	}

	if deferBeforeLock != nil {
		return deferBeforeLock
	}
	return nil
}

type groupOps struct {
	l map[*luPoint]ssa.Instruction
	u map[*luPoint]ssa.Instruction
	d map[*luPoint]ssa.Instruction
}

// collect all locks and unlocks in the function
func groupByKind(ptInsMap map[*luPoint]ssa.Instruction) (*groupOps, *groupOps, *groupOps) {

	m := groupOps{
		l: make(map[*luPoint]ssa.Instruction),
		u: make(map[*luPoint]ssa.Instruction),
		d: make(map[*luPoint]ssa.Instruction),
	}

	w := groupOps{
		l: make(map[*luPoint]ssa.Instruction),
		u: make(map[*luPoint]ssa.Instruction),
		d: make(map[*luPoint]ssa.Instruction),
	}

	r := groupOps{
		l: make(map[*luPoint]ssa.Instruction),
		u: make(map[*luPoint]ssa.Instruction),
		d: make(map[*luPoint]ssa.Instruction),
	}

	for k, v := range ptInsMap {
		switch k.luType() {
		case LOCK:
			m.l[k] = v
		case UNLOCK:
			m.u[k] = v
		case DEFER_UNLOCK:
			m.d[k] = v

		case RLOCK:
			r.l[k] = v
		case RUNLOCK:
			r.u[k] = v
		case DEFER_RUNLOCK:
			r.d[k] = v

		case WLOCK:
			w.l[k] = v
		case WUNLOCK:
			w.u[k] = v
		case DEFER_WUNLOCK:
			w.d[k] = v
		default:
			panic("unknown luPointType")
		}
	}
	return &m, &r, &w
}

func collectLUPoints(f *ssa.Function) (*groupOps, *groupOps, *groupOps, map[*luPoint]ssa.Instruction, map[ssa.Instruction]*luPoint) {
	ptInsMap, insPtMap := gatherLUPoints(f)
	m, r, w := groupByKind(ptInsMap)
	return m, r, w, ptInsMap, insPtMap
}

type functionSummary struct {
	f               *ssa.Function
	numDefers       int
	numDeferUnlocks int
	safe            bool
	pointsTo        []pointer.PointsToSet
	ptToIns         map[*luPoint]ssa.Instruction
	insToPt         map[ssa.Instruction]*luPoint
	isLambda        bool
	m               *groupOps
	r               *groupOps
	w               *groupOps
	metric          metrics
}

func (f *functionSummary) mayAlias(l *luPoint) bool {
	for _, v := range f.pointsTo {
		if v.Intersects(l.pointsToSet()) {
			return true
		}
	}
	return false
}

func (f *functionSummary) safeToCallFlat(l *luPoint, caller *functionSummary) bool {
	if f.safe == false {
		caller.metric.interRegionIO++
		return false
	}
	if f.mayAlias(l) {
		caller.metric.interRegionAlias++
		return false
	}
	return true
}

func (s *functionSummary) compute(cgNode *callgraph.Node) {
	s.numDefers = 0
	s.numDeferUnlocks = 0
	s.safe = true
	s.pointsTo = []pointer.PointsToSet{}
	for _, b := range s.f.Blocks {
		for _, i := range b.Instrs {
			if _, ok := i.(*ssa.Defer); ok {
				s.numDefers++
			}

			// TODO: This is not 100% correct.
			// In reality, we should check all callees of f, and disqualify it if we see calls to an unwanted function.
			// For example, see lib/callgraph/builtin.go  for the set of built-in functions called, which do NOT emerge from a call instruction.
			// For simplicity, we inspect only the call instructions and mark the calls to certain packages (fmt, io, runtime, ...) as unsafe.
			if s.safe && unsafeInst(i, cgNode) {
				s.safe = false
			}
		}
	}
	for k, _ := range s.ptToIns {
		if k.isDefer() {
			s.numDeferUnlocks++
		}
		s.pointsTo = append(s.pointsTo, k.pointsToSet())
	}
}

func (s *functionSummary) updateMetrics(candidates [3]int, pairsNonDefer [3]int, pairDefer [3]int) {
	s.metric.mLock = len(s.m.l)
	s.metric.mUnlock = len(s.m.u)
	s.metric.mDeferUnlock = len(s.m.d)
	s.metric.pairedMlock = pairsNonDefer[0]
	s.metric.pairedMDeferlock = pairDefer[0]
	s.metric.candidateMutex = candidates[0]

	s.metric.wLock = len(s.w.l)
	s.metric.wUnlock = len(s.w.u)
	s.metric.wDeferUnlock = len(s.w.d)
	s.metric.pairedWlock = pairsNonDefer[1]
	s.metric.pairedWDeferlock = pairDefer[1]
	s.metric.candidateWMutex = candidates[1]

	s.metric.rLock = len(s.r.l)
	s.metric.rUnlock = len(s.r.u)
	s.metric.rDeferUnlock = len(s.r.d)
	s.metric.pairedRlock = pairsNonDefer[2]
	s.metric.pairedRDeferlock = pairDefer[2]
	s.metric.candidateRMutex = candidates[2]

}

func collectLUPairs(f *ssa.Function, funcSummaryMap map[*ssa.Function]*functionSummary, cg map[*ssa.Function]*callgraph.Node) []*luPair {

	summary, ok := funcSummaryMap[f]
	if !ok {
		panic("funcSummaryMap[f] failed!")
	}

	m, r, w := summary.m, summary.r, summary.w
	legalLUPairs := make([]*luPair, 0)

	// Deal with pairing by type
	numPairsInFunction := 0
	pairsNonDefer := [3]int{0, 0, 0}
	pairsDefer := [3]int{0, 0, 0}
	candidates := [3]int{0, 0, 0}

	for pairIdx, pt := range []*groupOps{m, r, w} {
		if len(pt.d) > 1 {
			log.Printf("Ignoring function %v because two defer unlocks in the same function is not supported\n", f)
			break
		}

		candidateLUPairs := make([]*luPair, 0)
		postOrderLPoint := orderLocksByPostOrderInDomtree(f, pt.l)
		blk2Ulk := blockToUnlockPointMapAscending(pt.u)
		blk2DUlk := blockToUnlockPointMapAscending(pt.d)

		alreadyMatched := map[*luPoint]empty{}

		// for each lPoint in post-order match it with its nearest uPoint
		for _, l := range postOrderLPoint {
			u := getNearestUpoint(l, blk2Ulk, blk2DUlk, alreadyMatched)
			if u == nil {
				summary.metric.dominaceRelation++
				continue
			}
			// take it away from further matching
			alreadyMatched[u] = emptyStruct
			candidateLUPairs = append(candidateLUPairs, &luPair{l: l, u: u, f: f})
		}
		candidates[pairIdx] = len(candidateLUPairs)
		for _, lup := range candidateLUPairs {
			lup.calculateRegion()
			if !lup.checkRegionSafety(f, funcSummaryMap, cg) {
				continue
			}
			// both ought to be pointers or both values.
			// TODO: This is a strict condition, we can allow some freedom if needed.
			lup.l.isPointer = isSSAValueAMutexPointer(lup.l.callC.Args[0])
			lup.u.isPointer = isSSAValueAMutexPointer(lup.u.callC.Args[0])

			if lup.l.isPointer != lup.u.isPointer {
				continue
			}

			legalLUPairs = append(legalLUPairs, lup)

			numPairsInFunction++
			lup.l.idInFunc = numPairsInFunction
			lup.u.idInFunc = numPairsInFunction
			lup.l.isLambda = summary.isLambda
			lup.u.isLambda = summary.isLambda
			if lup.u.isDefer() {
				pairsDefer[pairIdx]++
			} else {
				pairsNonDefer[pairIdx]++
			}

		}
	}

	summary.updateMetrics(candidates, pairsNonDefer, pairsDefer)

	return legalLUPairs
}
