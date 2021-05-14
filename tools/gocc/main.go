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
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"go/ast"
	"go/format"
	"go/printer"
	"go/token"
	"go/types"
	"io"
	"log"
	"os"
	"strconv"
	"strings"

	libbuilder "github.com/uber-research/GOCC/lib/builder"
	libcg "github.com/uber-research/GOCC/lib/callgraph"
	postDom "github.com/uber-research/GOCC/tools/gocc/cfg"
	"golang.org/x/tools/go/ast/astutil"
	"golang.org/x/tools/go/callgraph"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
)

// TODO: manual SSA to detect a unique name for the majic lock
var majicLockName = "optiLock"
var majicLockID = 0

var usesMap map[*ast.Ident]types.Object
var typesMap map[ast.Expr]types.TypeAndValue

// isSingleFile differentiate a single file and a package
var isSingleFile bool

// this flag indicates whether profile is provided or not
// and a map to record the name if profile exists
var profileProvided bool = false
var hotFuncMap map[string]bool

// this map will mark which lock position and paths are in lambda function
// since it will have different namescope
var lockInLambdaFunc map[token.Pos]bool

// this map will keep the all blkstmt position whose parent is funclit
var blkstmtMap map[token.Pos]bool

// for any lock, lockInfo stores the positions where it locks and (defer) unlocks
// isValue indicates whether this lock is lock object or pointer in source code
type lockInfo struct {
	lockPosition   []token.Pos
	unlockPosition []token.Pos
	isValue        bool
	isRWMutex      bool
}

// statistics
var numLock = 0
var numUnlock = 0
var numDeferUnlock = 0
var lockUnlockSameBB = 0
var lockDeferUnlockSameBB = 0
var lockUnlockPairDifferentBB = 0
var lockDeferUnlockPairDifferentBB = 0
var unsafeLock = 0
var unpaired = 0
var paired = 0

var mPkg map[string]int
var mBBSafety map[int]bool
var blockList []string
var mapFuncSafety map[*ssa.Function]bool

var allLockVals map[ssa.Value]bool
var allUnlockVals map[ssa.Value]bool
var lockAliasMap map[ssa.Value][]ssa.Value
var pkgName map[string]bool
var tokenToName map[token.Pos]string
var pathToEndNodePos map[ast.Node]token.Pos

var writeOutput bool = true
var mCallGraph *callgraph.Graph
var curNode *ssa.Function
var outputPath string

// generate the name of the packages that violates HTM
func initBlockList() []string {
	// return []string{"sync", "os", "io", "fmt", "runtime"}
	// remove sync to allow nested lock
	return []string{"os", "io", "fmt", "runtime"}
}

func isMutexValue(s string) bool {
	mutexTypes := []string{"*sync.Mutex", "*sync.RWMutex"}
	for _, v := range mutexTypes {
		if v == s {
			return true
		}
	}
	return false
}

// Go allows both value and object to call lock function
// therefore, we need to differentiate two types for code transformation
func isLockPointer(rcv ssa.Value) bool {
	isValue := false
	switch rcv.(type) {
	case *ssa.FieldAddr:
		isValue = true
	case *ssa.Alloc:
		allcTmp := rcv.(*ssa.Alloc)
		if isMutexValue(allcTmp.Type().String()) {
			// Value type
			isValue = true
		} else {
			// Pointer type
		}
	case *ssa.Global:
		allcTmp := rcv.(*ssa.Global)
		if isMutexValue(allcTmp.Type().String()) {
			// is a value
			isValue = true
		} else {
			// is a pointer
		}
	case *ssa.FreeVar:
		allcTmp := rcv.(*ssa.FreeVar)
		if isMutexValue(allcTmp.Type().String()) {
			// is a value
			isValue = true
		} else {
			// is a pointer
		}
	default:
		// Pointer type
	}
	return isValue
}

// count lock, unlock, and defer unlock number
func countLockNumber(ssaF *ssa.Function, lockType string, lock string, unlock string) bool {
	if ssaF != nil && ssaF.Blocks != nil {
		for _, blk := range ssaF.Blocks {
			for _, ins := range blk.Instrs {
				if call, ok := ins.(*ssa.Call); ok {
					if !call.Call.IsInvoke() && call.Call.StaticCallee() != nil {
						calleeName := call.Call.StaticCallee().Name()
						callRcv := call.Call.Value
						if callRcv != nil && strings.Contains(callRcv.String(), lockType) && calleeName == lock {
							allLockVals[call.Call.Args[0]] = true
						} else if callRcv != nil && strings.Contains(callRcv.String(), lockType) && call.Call.StaticCallee().Name() == unlock {
							allUnlockVals[call.Call.Args[0]] = true
						}
					}
				} else if call, ok := ins.(*ssa.Defer); ok {
					if call.Call.StaticCallee() != nil {
						callRcv := call.Call.Value
						if callRcv != nil && strings.Contains(callRcv.String(), lockType) && call.Call.StaticCallee().Name() == unlock {
							allUnlockVals[call.Call.Args[0]] = true
						}
					}
				}
			}
		}
	}
	return localCheck(ssaF.Blocks)
}

// check if any instructions in the critical section is voilated HTM
// placeholder for now
func checkInstructionBetween(b *ssa.BasicBlock, startIdx, endIdx int) bool {
	for i := startIdx + 1; i < endIdx; i++ {
		if checkInst(b.Instrs[i]) == false {
			return false
		}
	}
	return true
}

// check if all the given bb is safe to transform
func checkBasicBlockInCriticalSection(blks []*ssa.BasicBlock) bool {
	for _, blk := range blks {
		// if we know this BB is unsafe, then entire critical section is not safe
		if val, ok := mBBSafety[blk.Index]; ok {
			if val == false {
				return false
			}
		}
		// check every instruction in current BB
		for _, ins := range blk.Instrs {
			if checkInst(ins) == false {
				// mark this block as unsafe and return
				mBBSafety[blk.Index] = false
				return false
			}
		}
		// if we reach to this point, this block is safe
		mBBSafety[blk.Index] = true
	}
	return true
}

func checkBlockList(rcv *ssa.Value) bool {
	for _, name := range blockList {
		if strings.Contains((*rcv).String(), name) {
			return true
		}
	}
	return false
}

// check all ins in the basic block except callee
func localCheck(blks []*ssa.BasicBlock) bool {
	for _, blk := range blks {
		for _, ins := range blk.Instrs {
			if _, ok := ins.(*ssa.Call); ok {
				if checkInst(ins) {
					return false
				}
			} else if _, ok := ins.(*ssa.Defer); ok {
				if checkInst(ins) {
					return false
				}
			}
		}
	}
	return true
}

// check if single insturction is violating HTM or not
func checkInst(ins ssa.Instruction) bool {
	// no go func() in the critical section
	if _, ok := ins.(*ssa.Go); ok {
		return false
	}
	if call, ok := ins.(*ssa.Call); ok {
		callRcv := call.Call.Value
		if checkBlockList(&callRcv) {
			return false
		}
		if mCallGraph != nil {
			nodeInGraph, ok := mCallGraph.Nodes[curNode]
			if ok {
				for _, edge := range nodeInGraph.Out {
					if edge.Site.Common().Value.String() == callRcv.String() {
						if mapFuncSafety[edge.Callee.Func] == false {
							return false
						}
					}
				}
			}
		}
	} else if call, ok := ins.(*ssa.Defer); ok {
		callRcv := call.Call.Value
		if checkBlockList(&callRcv) {
			return false
		}
		if mCallGraph != nil {
			nodeInGraph, ok := mCallGraph.Nodes[curNode]
			if ok {
				for _, edge := range nodeInGraph.Out {
					if edge.Site.Common().Value.String() == callRcv.String() {
						if mapFuncSafety[edge.Callee.Func] == false {
							return false
						}
					}
				}
			}
		}
	}
	return true
}

func dfsBlkHelper(blk *ssa.BasicBlock, res *[]*ssa.BasicBlock, visitedBB map[int]bool) {
	if blk == nil {
		return
	}
	// return if the block is visited
	visited, ok := visitedBB[blk.Index]
	if ok && visited == true {
		return
	}
	*res = append(*res, blk)
	visitedBB[blk.Index] = true
	for _, succ := range blk.Succs {
		dfsBlkHelper(succ, res, visitedBB)
	}
}

func dfsBlkHelper2(cur, end *ssa.BasicBlock, res *[]*ssa.BasicBlock, visitedBB map[int]bool) {
	if cur.Index == end.Index {
		return
	}
	// return if the block is visited
	visited, ok := visitedBB[cur.Index]
	if ok && visited == true {
		return
	}
	*res = append(*res, cur)
	visitedBB[cur.Index] = true
	for _, succ := range cur.Succs {
		dfsBlkHelper2(succ, end, res, visitedBB)
	}
}

// return all reachable bb of the given block
func reachableBlks(blk *ssa.BasicBlock) []*ssa.BasicBlock {
	res := make([]*ssa.BasicBlock, 0)
	visitedBB := make(map[int]bool)
	for _, succ := range blk.Succs {
		dfsBlkHelper(succ, &res, visitedBB)
	}
	return res
}

// return all bb between two blocks
func basicBlockInBetween(startBlk, endBlk *ssa.BasicBlock) []*ssa.BasicBlock {
	res := make([]*ssa.BasicBlock, 0)
	visitedBB := make(map[int]bool)
	for _, succ := range startBlk.Succs {
		dfsBlkHelper2(succ, endBlk, &res, visitedBB)
	}
	return res
}

func domContains(array []int, item int) bool {
	for _, val := range array {
		if val == item {
			return true
		}
	}
	return false
}

// add paired lock/unlock position in lockInfo
func addLockPairToLockInfo(lockMap map[string]lockInfo, lRcv string, l, ul *ssa.CallCommon, ssaF *ssa.Function) {
	pkgPath := ssaF.Pkg.Pkg.Path()
	splits := strings.Split(normalizeFunctionName(pkgPath), "/")
	canonicalName := splits[len(splits)-1]
	funcName := normalizeFunctionName(ssaF.RelString(ssaF.Pkg.Pkg))
	fullFuncName := canonicalName + "." + funcName

	if _, ok := hotFuncMap[fullFuncName]; !ok && profileProvided {
		return
	}
	if lkInfo, ok := lockMap[lRcv]; ok {
		lkInfo.lockPosition = append(lkInfo.lockPosition, l.Pos())
		lkInfo.unlockPosition = append(lkInfo.unlockPosition, ul.Pos())
		if isLockPointer(l.Args[0]) {
			lkInfo.isValue = true
		}
	} else {
		a := lockInfo{}
		a.lockPosition = append(a.lockPosition, l.Pos())
		a.unlockPosition = append(a.unlockPosition, ul.Pos())
		if isLockPointer(l.Args[0]) {
			a.isValue = true
		}
		lockMap[lRcv] = a
	}
}

// check if two lock receivers are the same, including aliasing analysis
func isSameLock(lockVal, unlockVal ssa.Value) bool {
	if lockVal.String() == unlockVal.String() {
		return true
	}
	if aliasSet, ok := lockAliasMap[lockVal]; ok {
		for _, val := range aliasSet {
			if val.String() == unlockVal.String() {
				return true
			}
		}
	}
	return false
}

func normalizeFunctionName(name string) string {
	// Normalize function names as follows:
	//    A.B.(*C).f -> A.B.C.f
	//    (A.B.C).f -> A.B.C.f
	fName := strings.TrimSpace(name)
	idx := strings.Index(fName, "(*")
	if idx != -1 {
		fName = fName[:idx] + fName[idx+2:]
	}
	idx = strings.Index(fName, "(")
	if idx != -1 {
		fName = fName[:idx] + fName[idx+1:]
	}
	idx = strings.Index(fName, ")")
	if idx != -1 {
		fName = fName[:idx] + fName[idx+1:]
	}
	return fName
}

// return if the ssa function is in the given input package so that we can transform
func inFile(ssaF *ssa.Function) bool {
	if _, ok := pkgName[ssaF.Pkg.Pkg.Name()]; ok {
		return true
	}
	return false
}

// Find all paired locks in given function.
// return a set of lockInfo for rewrite
// currently it checks 4 patterns:
// 1. lock/unlock in same basic block
// 2. lock/defer unlock in same basic block
// 3. lock/unlock in different basic block
// 4. lock/defer unlock in different basic block
func lockAnalysis(ssaF *ssa.Function, lockType string, lock string, unlock string) map[string]lockInfo {
	// lockMap to return from this function
	var lockMap = map[string]lockInfo{}

	if ssaF == nil || inFile(ssaF) == false {
		return lockMap
	}

	// store all lock/unlocks instructions in sets
	setLock := make(map[*ssa.CallCommon]bool)
	setUnlock := make(map[*ssa.CallCommon]bool)
	setDeferUnlock := make(map[*ssa.CallCommon]bool)

	// map stores which BB is the lock/unlock instruction belongs to
	mapLockToBB := make(map[*ssa.CallCommon]*ssa.BasicBlock)
	mapUnlockToBB := make(map[*ssa.CallCommon]*ssa.BasicBlock)
	mapDeferUnlockToBB := make(map[*ssa.CallCommon]*ssa.BasicBlock)

	// indicate the index of lock/unlock instruction within the BB
	setLockIndex := make(map[*ssa.CallCommon]int)
	setUnlockIndex := make(map[*ssa.CallCommon]int)
	setDeferUnlockIndex := make(map[*ssa.CallCommon]int)

	if ssaF != nil && ssaF.Blocks != nil {
		// detect lock/unlock and its declaration
		for _, blk := range ssaF.Blocks {
			for idx, ins := range blk.Instrs {
				if call, ok := ins.(*ssa.Call); ok {
					if !call.Call.IsInvoke() && call.Call.StaticCallee() != nil {
						calleeName := call.Call.StaticCallee().Name()
						callRcv := call.Call.Value
						// lock/unlock detected
						if callRcv != nil && strings.Contains(callRcv.String(), lockType) && calleeName == lock {
							setLock[&call.Call] = true
							mapLockToBB[&call.Call] = blk
							setLockIndex[&call.Call] = idx
						} else if callRcv != nil && strings.Contains(callRcv.String(), lockType) && call.Call.StaticCallee().Name() == unlock {
							mapUnlockToBB[&call.Call] = blk
							setUnlock[&call.Call] = true
							setUnlockIndex[&call.Call] = idx
						}
					}
				} else if call, ok := ins.(*ssa.Defer); ok {
					if call.Call.StaticCallee() != nil {
						callRcv := call.Call.Value
						if callRcv != nil && strings.Contains(callRcv.String(), lockType) && call.Call.StaticCallee().Name() == unlock {
							mapDeferUnlockToBB[&call.Call] = blk
							setDeferUnlock[&call.Call] = true
							setDeferUnlockIndex[&call.Call] = idx
						}
					}
				}
			}
		}

		// start the lock analysis
		// lock and unlock in the same block, add instruction in between as critical section
		for ul := range setUnlock {
			// ulRcv := ul.Args[0].String()
			for l := range setLock {
				if setLockIndex[l] > setUnlockIndex[ul] {
					continue
				}
				lRcv := l.Args[0].String()
				if isSameLock(l.Args[0], ul.Args[0]) && mapLockToBB[l] == mapUnlockToBB[ul] {
					// check critical section
					paired++
					if checkInstructionBetween(mapLockToBB[l], setLockIndex[l], setUnlockIndex[ul]) {
						lockUnlockSameBB++
						setUnlock[ul] = false
						setLock[l] = false
						addLockPairToLockInfo(lockMap, lRcv, l, ul, ssaF)
					} else {
						unsafeLock++
					}
				}
			}
		}

		// lock and defer unlock in the same block
		for ul := range setDeferUnlock {
			// ulRcv := ul.Args[0].String()
			for l := range setLock {
				lRcv := l.Args[0].String()
				if isSameLock(l.Args[0], ul.Args[0]) && mapLockToBB[l] == mapDeferUnlockToBB[ul] {
					paired++
					// critical section is all reachable blocks from this block plus the remaining instruction after unlock
					lstBlk := reachableBlks(mapLockToBB[l])
					blkInstNum := len(mapLockToBB[l].Instrs)
					safetyInBB := false
					// add this since defer unlock can be used before lock
					if setLockIndex[l] < setDeferUnlockIndex[ul] {
						safetyInBB = checkInstructionBetween(mapLockToBB[l], setLockIndex[l], setDeferUnlockIndex[ul]) && checkInstructionBetween(mapLockToBB[l], setDeferUnlockIndex[ul], blkInstNum)
					} else {
						safetyInBB = checkInstructionBetween(mapLockToBB[l], setLockIndex[l], blkInstNum)
					}
					if safetyInBB && checkBasicBlockInCriticalSection(lstBlk) {
						lockDeferUnlockSameBB++
						setDeferUnlock[ul] = false
						setLock[l] = false
						addLockPairToLockInfo(lockMap, lRcv, l, ul, ssaF)
					} else {
						unsafeLock++
					}
				}
			}
		}

		postDomMap := postDom.PostDominators(ssaF)
		// lock and and unlock in different bb, but dominates and post-dominates each other
		for ul := range setUnlock {
			// ulRcv := ul.Args[0].String()
			for l := range setLock {
				lRcv := l.Args[0].String()
				lockBB, ok := mapLockToBB[l]
				if ok == false {
					break
				}
				unlockBB, ok := mapUnlockToBB[ul]
				if ok == false {
					break
				}
				if isSameLock(l.Args[0], ul.Args[0]) && lockBB.Dominates(unlockBB) {
					paired++
					lkPD := postDomMap[mapLockToBB[l].Index]
					if domContains(lkPD, mapUnlockToBB[ul].Index) {
						// critical section is the bb between lock/unlock plus the remaining instruction in lock and unlock block
						lkBlkInstNum := len(mapLockToBB[l].Instrs)
						lkBlkRemainInst := checkInstructionBetween(mapLockToBB[l], setLockIndex[l], lkBlkInstNum)
						ulkBlkRemainInst := checkInstructionBetween(mapUnlockToBB[ul], -1, setUnlockIndex[ul])
						bbInBetween := basicBlockInBetween(mapLockToBB[l], mapUnlockToBB[ul])
						if lkBlkRemainInst && ulkBlkRemainInst && checkBasicBlockInCriticalSection(bbInBetween) {
							lockUnlockPairDifferentBB++
							setUnlock[ul] = false
							setLock[l] = false
							addLockPairToLockInfo(lockMap, lRcv, l, ul, ssaF)
						} else {
							unsafeLock++
						}
					}
				}
			}
		}

		// lock and defer unlock in different bb, but dominates and post-dominate each other
		for ul := range setDeferUnlock {
			// ulRcv := ul.Args[0].String()
			for l := range setLock {
				lRcv := l.Args[0].String()
				lockBB, ok := mapLockToBB[l]
				if ok == false {
					break
				}
				unlockBB, ok := mapUnlockToBB[ul]
				if ok == false {
					break
				}
				if isSameLock(l.Args[0], ul.Args[0]) && lockBB.Dominates(unlockBB) {
					paired++
					lkPD := postDomMap[mapLockToBB[l].Index]
					if domContains(lkPD, mapDeferUnlockToBB[ul].Index) {
						// critical section is all block that can be reached by defer unlock and everything in between the lock and unlock
						lkBlkInstNum := len(mapLockToBB[l].Instrs)
						ulkBlkInstNumber := len(mapDeferUnlockToBB[ul].Instrs)
						lkBlkRemainInst := checkInstructionBetween(mapLockToBB[l], setLockIndex[l], lkBlkInstNum)
						ulkBlkRemainInst := checkInstructionBetween(mapDeferUnlockToBB[ul], -1, setDeferUnlockIndex[ul]) && checkInstructionBetween(mapDeferUnlockToBB[ul], setDeferUnlockIndex[ul], ulkBlkInstNumber)
						bbInBetween := basicBlockInBetween(mapLockToBB[l], mapUnlockToBB[ul])
						bbReachable := reachableBlks(mapDeferUnlockToBB[ul])
						if lkBlkRemainInst && ulkBlkRemainInst && checkBasicBlockInCriticalSection(bbReachable) && checkBasicBlockInCriticalSection(bbInBetween) {
							lockDeferUnlockPairDifferentBB++
							setDeferUnlock[ul] = false
							setLock[l] = false
							addLockPairToLockInfo(lockMap, lRcv, l, ul, ssaF)
						} else {
							unsafeLock++
						}
					}
				} else if isSameLock(l.Args[0], ul.Args[0]) && unlockBB.Dominates(lockBB) {
					paired++
					// add this since defer unlock can happen before lock
					ulkPD := postDomMap[mapLockToBB[ul].Index]
					if domContains(ulkPD, mapDeferUnlockToBB[l].Index) {
						// critical section is all block that can be reached by defer unlock and everything in between the lock and unlock
						lkBlkInstNum := len(mapLockToBB[l].Instrs)
						lkBlkRemainInst := checkInstructionBetween(mapLockToBB[l], setLockIndex[l], lkBlkInstNum)
						bbReachable := reachableBlks(mapLockToBB[l])
						if lkBlkRemainInst && checkBasicBlockInCriticalSection(bbReachable) {
							lockDeferUnlockPairDifferentBB++
							setDeferUnlock[ul] = false
							setLock[l] = false
							addLockPairToLockInfo(lockMap, lRcv, l, ul, ssaF)
						} else {
							unsafeLock++
						}
					}
				}
			}
		}
	}

	for _, v := range setLock {
		if v == true {
			unpaired++
		}
	}
	for _, v := range setUnlock {
		if v == true {
			unpaired++
		}
	}
	for _, v := range setDeferUnlock {
		if v == true {
			unpaired++
		}
	}
	numLock += len(setLock)
	numUnlock += len(setUnlock)
	numDeferUnlock += len(setDeferUnlock)

	return lockMap
}

func pathContains(replacePath [][]ast.Node, curPos token.Pos) bool {
	for _, path := range replacePath {
		for _, node := range path {
			if node.Pos() == curPos {
				return true
			}
		}
	}
	return false
}

func getPosName(path [][]ast.Node, curPos token.Pos, posToID map[token.Pos]string) string {
	for _, p := range path {
		for _, node := range p {
			if node.Pos() == curPos {
				for _, n := range p {
					if str, ok := posToID[pathToEndNodePos[n]]; ok {
						return str
					}
				}
			}
		}
	}
	return ""
}

func reversePathContains(replacePath [][]ast.Node, curPos token.Pos) bool {
	for _, path := range replacePath {
	_PATHLOOP:
		for i := 0; i < len(path); i++ {
			switch n := path[i].(type) {
			case *ast.BlockStmt:
				if n.Pos() == curPos {
					return true
				} else if _, ok := blkstmtMap[n.Pos()]; ok {
					break _PATHLOOP
				}
			}
		}
	}
	return false
}

func singlePathContains(singlePath []ast.Node, curPos token.Pos) bool {
	for _, node := range singlePath {
		if node.Pos() == curPos {
			return true
		}
	}
	return false
}

// adds context variable definition at the beginning of the function's statement list
func addContextInitStmt(stmtsList *[]ast.Stmt, sigPos token.Pos, count int) {
	for i := 0; i < count; i++ {
		newStmt := ast.AssignStmt{
			Lhs:    []ast.Expr{ast.NewIdent(majicLockName + strconv.Itoa(i))},
			TokPos: sigPos, // use concrete position to avoid being split by a comment leading to syntax error
			Tok:    token.DEFINE,
			Rhs:    []ast.Expr{ast.NewIdent("rtm.OptiLock{}")}}
		var newStmtsList []ast.Stmt
		newStmtsList = append(newStmtsList, &newStmt)
		newStmtsList = append(newStmtsList, (*stmtsList)...)
		*stmtsList = newStmtsList
	}
}

func collectBlkstmt(f ast.Node, pkg *packages.Package) {
	postFunc := func(c *astutil.Cursor) bool {
		node := c.Node()
		switch node.(type) {
		case *ast.BlockStmt:
			{
				if _, ok := c.Parent().(*ast.FuncLit); ok && c.Name() == "Body" {
					blkstmtMap[c.Node().Pos()] = true
				}
			}
		}
		return true
	}
	astutil.Apply(f, nil, postFunc)
}

// given the function f from the pkg, replace all the valid lock/unlock with htm library
// currently it supports two types of locks: sync.Mutex and sync.RWMutex
// for each type of lock operations, it can be called on 3 receivers:
// 1. Lock pointer, e.g. m := &sync.Mutex
// 2. Lock object, e.g. m:= sync.Mutex
// 3. Promoted field object. e.g.
// type Foo struct {
// 	sync.Mutex
// }
// foo := Foo{}
// foo.Lock()
// 4. Promoted field pointer
func rewriteAST(f ast.Node, pkg *packages.Package, replacePathRWMutex, insertPathRWMutex, replacePathMutex, insertPathMutex, lambdaPath, normalPath *[][]ast.Node, posToID map[token.Pos]string) ast.Node {
	fmt.Println("  Rewriting field accesses in the file...")
	blkmap := make(map[*ast.BlockStmt]bool)
	addImport := false
	optilockNumber := len(posToID) / 2
	postFunc := func(c *astutil.Cursor) bool {
		node := c.Node()
		switch n := node.(type) {
		case *ast.CallExpr:
			{
				// when the lock is a *sync.Mutex
				switch {
				case pathContains(*replacePathMutex, n.Pos()):
					{
						if se, ok := n.Fun.(*ast.SelectorExpr); ok {
							if se.Sel.Name == "Lock" || se.Sel.Name == "Unlock" {
								lockType := typesMap[se.X].Type.String()
								// branch 1: receiver is lock pointer
								if strings.Contains(lockType, "*sync.Mutex") {
									fun := &ast.SelectorExpr{
										X: &ast.Ident{
											Name:    majicLockName + getPosName(*replacePathMutex, n.Pos(), posToID),
											NamePos: se.X.Pos(),
										},
										Sel: se.Sel,
									}
									call := &ast.CallExpr{
										Fun:      fun,
										Lparen:   token.NoPos,
										Args:     []ast.Expr{se.X},
										Ellipsis: token.NoPos,
										Rparen:   token.NoPos,
									}

									c.Replace(call)
								} else {
									// branch 4: receiver is promoted field pointer
									fun := &ast.SelectorExpr{
										X: &ast.Ident{
											Name:    majicLockName + getPosName(*replacePathMutex, n.Pos(), posToID),
											NamePos: se.X.Pos(),
										},
										Sel: se.Sel,
									}
									newSel := &ast.SelectorExpr{
										X:   se.X,
										Sel: ast.NewIdent("Mutex"),
									}
									call := &ast.CallExpr{
										Fun:      fun,
										Lparen:   token.NoPos,
										Args:     []ast.Expr{newSel},
										Ellipsis: token.NoPos,
										Rparen:   token.NoPos,
									}

									c.Replace(call)
								}
							}
						}
					}
				case pathContains(*insertPathMutex, n.Pos()):
					{
						// when the lock is a sync.Mutex
						if se, ok := n.Fun.(*ast.SelectorExpr); ok {
							if se.Sel.Name == "Lock" || se.Sel.Name == "Unlock" {
								lockType := typesMap[se.X].Type.String()
								if lockType == "sync.Mutex" {
									// branch 2: receiver is lock object, need to take its address
									fun := &ast.SelectorExpr{
										X: &ast.Ident{
											Name:    majicLockName + getPosName(*insertPathMutex, n.Pos(), posToID),
											NamePos: se.X.Pos(),
										},
										Sel: se.Sel,
									}
									newX := &ast.UnaryExpr{
										Op: token.AND,
										X:  se.X,
									}
									call := &ast.CallExpr{
										Fun:      fun,
										Lparen:   token.NoPos,
										Args:     []ast.Expr{newX},
										Ellipsis: token.NoPos,
										Rparen:   token.NoPos,
									}

									c.Replace(call)
								} else {
									// branch 3: receiver is some promoted field object
									fun := &ast.SelectorExpr{
										X: &ast.Ident{
											Name:    majicLockName + getPosName(*insertPathMutex, n.Pos(), posToID),
											NamePos: se.X.Pos(),
										},
										Sel: se.Sel,
									}
									newSel := &ast.SelectorExpr{
										X:   se.X,
										Sel: ast.NewIdent("Mutex"),
									}
									newX := &ast.UnaryExpr{
										Op: token.AND,
										X:  newSel,
									}
									call := &ast.CallExpr{
										Fun:      fun,
										Lparen:   token.NoPos,
										Args:     []ast.Expr{newX},
										Ellipsis: token.NoPos,
										Rparen:   token.NoPos,
									}

									c.Replace(call)
								}
							}
						}
					}
				case pathContains(*replacePathRWMutex, n.Pos()):
					{
						// when the lock is *sync.RWMutex
						if se, ok := n.Fun.(*ast.SelectorExpr); ok {
							// change RWMutex.Lock()/Unlock() to majicLock.WLock()/WUnlock()
							if se.Sel.Name == "Lock" || se.Sel.Name == "Unlock" {
								lockType := typesMap[se.X].Type.String()
								// branch 1: receiver is a lock pointer
								if lockType == "*sync.RWMutex" {
									fun := &ast.SelectorExpr{
										X: &ast.Ident{
											Name:    majicLockName + getPosName(*replacePathRWMutex, n.Pos(), posToID),
											NamePos: se.X.Pos(),
										},
										Sel: &ast.Ident{
											Name:    "W" + se.Sel.Name,
											NamePos: se.Sel.NamePos,
										},
									}

									call := &ast.CallExpr{
										Fun:      fun,
										Lparen:   token.NoPos,
										Args:     []ast.Expr{se.X},
										Ellipsis: token.NoPos,
										Rparen:   token.NoPos,
									}

									c.Replace(call)
								} else {
									// branch 3: promoted field pointer
									fun := &ast.SelectorExpr{
										X: &ast.Ident{
											Name:    majicLockName + getPosName(*replacePathRWMutex, n.Pos(), posToID),
											NamePos: se.X.Pos(),
										},
										Sel: &ast.Ident{
											Name:    "W" + se.Sel.Name,
											NamePos: se.Sel.NamePos,
										},
									}
									newX := &ast.SelectorExpr{
										X:   se.X,
										Sel: ast.NewIdent("RWMutex"),
									}
									call := &ast.CallExpr{
										Fun:      fun,
										Lparen:   token.NoPos,
										Args:     []ast.Expr{newX},
										Ellipsis: token.NoPos,
										Rparen:   token.NoPos,
									}

									c.Replace(call)
								}
							} else if se.Sel.Name == "RLock" || se.Sel.Name == "RUnlock" {
								// this changes RLock/RUnlock of RWMutex
								lockType := typesMap[se.X].Type.String()

								if lockType == "*sync.RWMutex" {
									// branch 1: receiver is lock pointer
									fun := &ast.SelectorExpr{
										X: &ast.Ident{
											Name:    majicLockName + getPosName(*replacePathRWMutex, n.Pos(), posToID),
											NamePos: se.X.Pos(),
										},
										Sel: &ast.Ident{
											Name:    se.Sel.Name,
											NamePos: se.Sel.NamePos,
										},
									}

									call := &ast.CallExpr{
										Fun:      fun,
										Lparen:   token.NoPos,
										Args:     []ast.Expr{se.X},
										Ellipsis: token.NoPos,
										Rparen:   token.NoPos,
									}

									c.Replace(call)
								} else {
									// branch 4: lock is called on promoted field pointer
									fun := &ast.SelectorExpr{
										X: &ast.Ident{
											Name:    majicLockName + getPosName(*replacePathRWMutex, n.Pos(), posToID),
											NamePos: se.X.Pos(),
										},
										Sel: se.Sel,
									}
									newX := &ast.SelectorExpr{
										X:   se.X,
										Sel: ast.NewIdent("RWMutex"),
									}
									call := &ast.CallExpr{
										Fun:      fun,
										Lparen:   token.NoPos,
										Args:     []ast.Expr{newX},
										Ellipsis: token.NoPos,
										Rparen:   token.NoPos,
									}

									c.Replace(call)
								}
							}
						}

					}
				case pathContains(*insertPathRWMutex, n.Pos()):
					{
						// when the lock is sync.RWMutex
						if se, ok := n.Fun.(*ast.SelectorExpr); ok {
							// change RWMutex.Lock()/Unlock() to majicLock.WLock()/WUnlock()
							if se.Sel.Name == "Lock" || se.Sel.Name == "Unlock" {
								lockType := typesMap[se.X].Type.String()
								if lockType == "sync.RWMutex" {
									// branch 2: receiver is a lock value
									fun := &ast.SelectorExpr{
										X: &ast.Ident{
											Name:    majicLockName + getPosName(*insertPathRWMutex, n.Pos(), posToID),
											NamePos: se.X.Pos(),
										},
										Sel: &ast.Ident{
											Name:    "W" + se.Sel.Name,
											NamePos: se.Sel.NamePos,
										},
									}
									newX := &ast.UnaryExpr{
										Op: token.AND,
										X:  se.X,
									}
									call := &ast.CallExpr{
										Fun:      fun,
										Lparen:   token.NoPos,
										Args:     []ast.Expr{newX},
										Ellipsis: token.NoPos,
										Rparen:   token.NoPos,
									}

									c.Replace(call)
								} else {
									// branch 3: promoted field object
									fun := &ast.SelectorExpr{
										X: &ast.Ident{
											Name:    majicLockName + getPosName(*insertPathRWMutex, n.Pos(), posToID),
											NamePos: se.X.Pos(),
										},
										Sel: &ast.Ident{
											Name:    "W" + se.Sel.Name,
											NamePos: se.Sel.NamePos,
										},
									}
									newSel := &ast.SelectorExpr{
										X:   se.X,
										Sel: ast.NewIdent("RWMutex"),
									}
									newX := &ast.UnaryExpr{
										Op: token.AND,
										X:  newSel,
									}
									call := &ast.CallExpr{
										Fun:      fun,
										Lparen:   token.NoPos,
										Args:     []ast.Expr{newX},
										Ellipsis: token.NoPos,
										Rparen:   token.NoPos,
									}

									c.Replace(call)
								}
							} else if se.Sel.Name == "RLock" || se.Sel.Name == "RUnlock" {
								// this changes RLock/RUnlock of RWMutex
								lockType := typesMap[se.X].Type.String()
								if lockType == "sync.RWMutex" {
									// branch 2, lock is called on lock value
									fun := &ast.SelectorExpr{
										X: &ast.Ident{
											Name:    majicLockName + getPosName(*insertPathRWMutex, n.Pos(), posToID),
											NamePos: se.X.Pos(),
										},
										Sel: se.Sel,
									}
									newX := &ast.UnaryExpr{
										Op: token.AND,
										X:  se.X,
									}
									call := &ast.CallExpr{
										Fun:      fun,
										Lparen:   token.NoPos,
										Args:     []ast.Expr{newX},
										Ellipsis: token.NoPos,
										Rparen:   token.NoPos,
									}

									c.Replace(call)
								} else {
									// branch 3: lock is called on promoted field value
									fun := &ast.SelectorExpr{
										X: &ast.Ident{
											Name:    majicLockName + getPosName(*insertPathRWMutex, n.Pos(), posToID),
											NamePos: se.X.Pos(),
										},
										Sel: se.Sel,
									}
									newSel := &ast.SelectorExpr{
										X:   se.X,
										Sel: ast.NewIdent("RWMutex"),
									}
									newX := &ast.UnaryExpr{
										Op: token.AND,
										X:  newSel,
									}
									call := &ast.CallExpr{
										Fun:      fun,
										Lparen:   token.NoPos,
										Args:     []ast.Expr{newX},
										Ellipsis: token.NoPos,
										Rparen:   token.NoPos,
									}

									c.Replace(call)
								}
							}
						}
					}
				}
			}
		case *ast.ImportSpec:
			{
				if addImport == false {
					newImport := &ast.ImportSpec{
						Doc:  n.Doc,
						Name: ast.NewIdent("rtm"),
						Path: &ast.BasicLit{
							ValuePos: n.Path.ValuePos,
							Kind:     n.Path.Kind,
							Value:    strconv.Quote("github.com/lollllcat/GOCC/tools/gocc/rtmlib"),
						},
						Comment: n.Comment,
						EndPos:  n.EndPos,
					}
					c.InsertAfter(newImport)
					addImport = true
				}
			}
		case *ast.BlockStmt:
			{
				// purpose of this part is to add the majicLock declarations to the funtions that we transform
				blkStmt := c.Node().(*ast.BlockStmt)
				// not added before
				if _, ok := blkmap[blkStmt]; !ok {
					if fd, ok := c.Parent().(*ast.FuncDecl); ok && c.Name() == "Body" {
						// containLocks := pathContains(*replacePathMutex, n.Pos()) || pathContains(*replacePathRWMutex, n.Pos()) || pathContains(*insertPathRWMutex, n.Pos()) || pathContains(*insertPathMutex, n.Pos())
						containLocks := pathContains(*normalPath, n.Pos())
						if containLocks {
							addContextInitStmt(&(fd.Body.List), fd.Name.NamePos, optilockNumber)
							blkmap[blkStmt] = true
						}
					} else if fl, ok := c.Parent().(*ast.FuncLit); ok && c.Name() == "Body" {
						// containLocks := pathContains(*replacePathMutex, n.Pos()) || pathContains(*replacePathRWMutex, n.Pos()) || pathContains(*insertPathRWMutex, n.Pos()) || pathContains(*insertPathMutex, n.Pos())
						containLocks := reversePathContains(*lambdaPath, c.Node().Pos())
						if containLocks {
							addContextInitStmt(&(fl.Body.List), fl.Body.Lbrace, optilockNumber)
							blkmap[blkStmt] = true
						}
					}
				}
			}
		}
		return true
	}
	return astutil.Apply(f, nil, postFunc)
}

func writeAST(f ast.Node, sourceFilePath string, pkg *packages.Package, filename string) {
	if writeOutput {
		fmt.Println("  Writing output to ", filename)
		info, err := os.Stat(filename)
		if err != nil {
			panic(err)
		}
		fSize := info.Size()
		os.Remove(filename)
		output, err := os.Create(filename)
		if err != nil {
			panic(err)
		}
		defer output.Close()

		w := bufio.NewWriterSize(output, int(2*fSize))
		if err := format.Node(w, pkg.Fset, f); err != nil {
			panic(err)
		}
		w.Flush()
	}
}

func argContains(args []string, target string) bool {
	for _, val := range args {
		if val == target {
			return true
		}
	}
	return false
}

func getLockName(f ast.Node, pkg *packages.Package, path []ast.Node) string {
	var str string
	postFunc := func(c *astutil.Cursor) bool {
		node := c.Node()
		switch n := node.(type) {
		case *ast.CallExpr:
			if singlePathContains(path, n.Pos()) {
				if se, ok := n.Fun.(*ast.SelectorExpr); ok {
					if se.Sel.Name == "Lock" || se.Sel.Name == "Unlock" || se.Sel.Name == "RLock" || se.Sel.Name == "RUnlock" {
						var selectorExpr bytes.Buffer
						err := printer.Fprint(&selectorExpr, pkg.Fset, se.X)
						if err != nil {
							log.Fatalf("failed printing %s", err)
						}
						str = selectorExpr.String()
						return true
					}
				}
			}
		}
		return true
	}
	astutil.Apply(f, nil, postFunc)
	return str
}

// the goal of this function is to check whether the lock/unlock operations are called on the same lock
// the criteria here is the caller name
// TODO: cache the file information so that we don't need to use pathcontain again... which needs a global map to store file and lock relation
func checkSameLock(l lockInfo, pkgs []*packages.Package) bool {
	for _, pkg := range pkgs {
		for _, file := range pkg.Syntax {
			lockNamePath, ok := astutil.PathEnclosingInterval(file, l.lockPosition[0], l.lockPosition[0])
			if !ok {
				continue
			}
			// this file contains the lock and unlock
			lockName := getLockName(file, pkg, lockNamePath)
			// all lock() name should be the same
			for _, l := range l.lockPosition {
				lockPath, ok := astutil.PathEnclosingInterval(file, l, l)
				if ok {
					lName := getLockName(file, pkg, lockPath)
					if lockName != lName {
						return false
					}
				}
			}
			// all unlock() name should be the same
			for _, ul := range l.unlockPosition {
				unlockPath, ok := astutil.PathEnclosingInterval(file, ul, ul)
				if ok {
					ulName := getLockName(file, pkg, unlockPath)
					if ulName != lockName {
						return false
					}
				}
			}
		}
	}
	return true
}

func main() {

	// lockName contains lock type and lock/unlock operation name
	lockName := [3][3]string{
		{"*sync.Mutex", "Lock", "Unlock"},
		{"*sync.RWMutex", "Lock", "Unlock"},
		{"*sync.RWMutex", "RLock", "RUnlock"}}

	// blockList is the collection of package names that violates HTM
	blockList = initBlockList()

	// indicates a function is safe to use HTM or not. Local check only.
	mapFuncSafety = make(map[*ssa.Function]bool)

	// this map stores the hot function from the profiling
	hotFuncMap = make(map[string]bool)

	lockInLambdaFunc = make(map[token.Pos]bool)
	blkstmtMap = make(map[token.Pos]bool)

	mPkg = make(map[string]int)
	pkgName = make(map[string]bool)
	tokenToName = make(map[token.Pos]string)
	pathToEndNodePos = make(map[ast.Node]token.Pos)

	// lock positions to rewrite
	replacedRWMutexPtr := make(map[token.Pos]bool)
	replacedRWMutexVal := make(map[token.Pos]bool)
	replacedMutexPtr := make(map[token.Pos]bool)
	replacedMutexVal := make(map[token.Pos]bool)

	// command-line argument
	dryrunPtr := flag.Bool("dryrun", false, "indicates if AST will be written out or not")
	statsPtr := flag.Bool("stats", false, "dump out the stats information of the locks")

	var inputFile string
	flag.StringVar(&inputFile, "input", "", "source file to analyze")

	var profilePath string
	flag.StringVar(&profilePath, "profile", "", "profiling of hot function")

	syntheticPtr := flag.Bool("synthetic", false, "set true if the synthetic main from transformer is used")

	rewriteTestFile := flag.Bool("rewriteTest", false, "set true if you want to change testing file")

	flag.Parse()

	if inputFile == "" {
		fmt.Println("Please provide the input!")
		flag.PrintDefaults()
		os.Exit(1)
	}

	if profilePath != "" {
		profileProvided = true
		f, err := os.Open(profilePath)
		defer f.Close()
		if err != nil {
			panic("cannot open profile")
		}
		rd := bufio.NewReader(f)
		for {
			line, err := rd.ReadString('\n')
			if err == io.EOF {
				fmt.Print(line)
				break
			}
			if err != nil {
				panic("profile is wrong")
			}
			hotFuncMap[strings.TrimSpace(line)] = true
		}
	}

	outputPath = inputFile + "/"

	if *dryrunPtr {
		writeOutput = false
		fmt.Println("Running in dryrun mode - modified ASTs will not be written out.")
	} else {
		writeOutput = true
	}

	if strings.HasSuffix(inputFile, ".go") {
		isSingleFile = true
	} else {
		isSingleFile = false
		inputFile += "/..."
	}

	var pkgs []*packages.Package

	pkgConfig := &packages.Config{Mode: packages.LoadAllSyntax, Tests: true}

	pkgs, err := packages.Load(pkgConfig, inputFile)
	if err != nil {
		panic("something wrong during loading!")
	}

	for _, pkg := range pkgs {
		pkgName[pkg.Name] = true
	}

	prog, ssapkgs := ssautil.AllPackages(pkgs, ssa.NaiveForm|ssa.GlobalDebug)
	// prog, ssapkgs := ssautil.AllPackages(pkgs, ssa.GlobalDebug)
	libbuilder.BuildPackages(prog, ssapkgs, true, true)
	mCallGraph = libcg.BuildRtaCG(prog, true)

	// TODO: first pass on optimized form and second pass on naive form to check if it is a value or object

	for _, lockCombo := range lockName {
		lockType := lockCombo[0]
		lockFuncName := lockCombo[1]
		unlockFuncName := lockCombo[2]
		allLockVals = make(map[ssa.Value]bool)
		allUnlockVals = make(map[ssa.Value]bool)

		for node := range mCallGraph.Nodes {
			if countLockNumber(node, lockType, lockFuncName, unlockFuncName) == true {
				mapFuncSafety[node] = true
			} else {
				mapFuncSafety[node] = false
			}
		}

		// aliasing analysis on lock
		lockAliasMap = postDom.MatchLockWithUnlock(ssapkgs, allLockVals, allUnlockVals, isSingleFile, *syntheticPtr)

		for node := range mCallGraph.Nodes {
			// reset basic block safety map for each function
			mBBSafety = make(map[int]bool)
			curNode = node
			postDom.GetExit(node)
			lockInfoMap := lockAnalysis(node, lockType, lockFuncName, unlockFuncName)
			isLambda := strings.Contains(node.RelString(node.Pkg.Pkg), "$")
			for lkName, value := range lockInfoMap {
				// if this is not a same lock, don't replace.
				// TODO: fix this
				if checkSameLock(value, pkgs) == false {
					continue
				}
				// we need to differentiate Mutex and RWMutex
				// this is a RWMute
				if lockType == "*sync.RWMutex" {
					if value.isValue == false {
						// a RWMutex pointer not a value
						for _, item := range value.lockPosition {
							replacedRWMutexPtr[item] = true
							tokenToName[item] = lkName
							if isLambda {
								lockInLambdaFunc[item] = true
							}
						}
						for _, item := range value.unlockPosition {
							replacedRWMutexPtr[item] = true
							tokenToName[item] = lkName
							if isLambda {
								lockInLambdaFunc[item] = true
							}
						}
					} else {
						// a RWMutex value
						for _, item := range value.lockPosition {
							replacedRWMutexVal[item] = true
							tokenToName[item] = lkName
							if isLambda {
								lockInLambdaFunc[item] = true
							}
						}
						for _, item := range value.unlockPosition {
							replacedRWMutexVal[item] = true
							tokenToName[item] = lkName
							if isLambda {
								lockInLambdaFunc[item] = true
							}
						}
					}
				} else {
					if value.isValue == false {
						// a Mutex pointer not a value
						for _, item := range value.lockPosition {
							replacedMutexPtr[item] = true
							tokenToName[item] = lkName
							if isLambda {
								lockInLambdaFunc[item] = true
							}
						}
						for _, item := range value.unlockPosition {
							replacedMutexPtr[item] = true
							tokenToName[item] = lkName
							if isLambda {
								lockInLambdaFunc[item] = true
							}
						}
					} else {
						// a Mutex value
						for _, item := range value.lockPosition {
							replacedMutexVal[item] = true
							tokenToName[item] = lkName
							if isLambda {
								lockInLambdaFunc[item] = true
							}
						}
						for _, item := range value.unlockPosition {
							replacedMutexVal[item] = true
							tokenToName[item] = lkName
							if isLambda {
								lockInLambdaFunc[item] = true
							}
						}
					}
				}
			}
		}
	}
	fmt.Println(len(replacedMutexVal) + len(replacedMutexPtr) + len(replacedRWMutexVal) + len(replacedRWMutexPtr))
	for _, pkg := range pkgs {
		usesMap = pkg.TypesInfo.Uses
		typesMap = pkg.TypesInfo.Types
		for _, file := range pkg.Syntax {
			nameToID := make(map[string]string)
			posToID := make(map[token.Pos]string)
			id := 0
			// don't rewrite testing file by default
			if *rewriteTestFile == false {
				fName := pkg.Fset.Position(file.Pos()).Filename
				if strings.HasSuffix(fName, "_test.go") {
					// fmt.Printf("%v is skipped since it is a testing file\n", fName)
					continue
				}
			}

			// replacePath means the lock is a pointer so we can replace it directly
			// insertPath indicates the lock is a value and we need to take the address of it.
			replacePathRWMutex := make([][]ast.Node, 0)
			insertPathRWMutex := make([][]ast.Node, 0)
			replacePathMutex := make([][]ast.Node, 0)
			insertPathMutex := make([][]ast.Node, 0)
			lambdaPath := make([][]ast.Node, 0)
			normalPath := make([][]ast.Node, 0)
			for l := range replacedRWMutexPtr {
				path, ok := astutil.PathEnclosingInterval(file, l, l)
				if ok {
					pathToEndNodePos[path[0]] = l
					replacePathRWMutex = append(replacePathRWMutex, path)
					if _, ok := lockInLambdaFunc[l]; ok {
						lambdaPath = append(lambdaPath, path)
					} else {
						normalPath = append(normalPath, path)
					}
					if val, ok := nameToID[tokenToName[l]]; ok {
						posToID[l] = val
					} else {
						nameToID[tokenToName[l]] = strconv.Itoa(id)
						posToID[l] = strconv.Itoa(id)
						id++
					}
				}
			}
			for l := range replacedRWMutexVal {
				path, ok := astutil.PathEnclosingInterval(file, l, l)
				if ok {
					pathToEndNodePos[path[0]] = l
					insertPathRWMutex = append(insertPathRWMutex, path)
					if _, ok := lockInLambdaFunc[l]; ok {
						lambdaPath = append(lambdaPath, path)
					} else {
						normalPath = append(normalPath, path)
					}
					if val, ok := nameToID[tokenToName[l]]; ok {
						posToID[l] = val
					} else {
						nameToID[tokenToName[l]] = strconv.Itoa(id)
						posToID[l] = strconv.Itoa(id)
						id++
					}
				}
			}

			for l := range replacedMutexPtr {
				path, ok := astutil.PathEnclosingInterval(file, l, l)
				if ok {
					pathToEndNodePos[path[0]] = l
					replacePathMutex = append(replacePathMutex, path)
					if _, ok := lockInLambdaFunc[l]; ok {
						lambdaPath = append(lambdaPath, path)
					} else {
						normalPath = append(normalPath, path)
					}
					if val, ok := nameToID[tokenToName[l]]; ok {
						posToID[l] = val
					} else {
						nameToID[tokenToName[l]] = strconv.Itoa(id)
						posToID[l] = strconv.Itoa(id)
						id++
					}
				}
			}
			for l := range replacedMutexVal {
				path, ok := astutil.PathEnclosingInterval(file, l, l)
				if ok {
					pathToEndNodePos[path[0]] = l
					insertPathMutex = append(insertPathMutex, path)
					if _, ok := lockInLambdaFunc[l]; ok {
						lambdaPath = append(lambdaPath, path)
					} else {
						normalPath = append(normalPath, path)
					}
					if val, ok := nameToID[tokenToName[l]]; ok {
						posToID[l] = val
					} else {
						nameToID[tokenToName[l]] = strconv.Itoa(id)
						posToID[l] = strconv.Itoa(id)
						id++
					}
				}
			}

			numberOfLocksToChange := len(replacePathRWMutex) + len(insertPathRWMutex) + len(replacePathMutex) + len(insertPathMutex)
			if numberOfLocksToChange > 0 {
				fmt.Printf("%v has %v locks to rewrite\n", prog.Fset.Position(file.Pos()).Filename, numberOfLocksToChange)
			}
			if numberOfLocksToChange > 0 && writeOutput {
				fmt.Printf("Number of locks to rewrite %v\n", numberOfLocksToChange)
				collectBlkstmt(file, pkg)
				ast := rewriteAST(file, pkg, &replacePathRWMutex, &insertPathRWMutex, &replacePathMutex, &insertPathMutex, &lambdaPath, &normalPath, posToID)
				filename := prog.Fset.Position(ast.Pos()).Filename
				writeAST(ast, inputFile, pkg, filename)
			}
		}
	}
	if *statsPtr {
		inputFile = strings.Replace(inputFile, "/", "_", -1)

		f, err := os.Create("lockcount/" + inputFile + ".txt")
		if err != nil {
			fmt.Println(err)
			return
		}
		defer f.Close()

		w := bufio.NewWriter(f)

		fmt.Fprintln(w, "Number of locks: ", numLock)
		fmt.Fprintln(w, "Number of Unlocks: ", numUnlock)
		fmt.Fprintln(w, "Number of deferred Unlocks: ", numDeferUnlock)
		fmt.Fprintln(w, "Lock pairs in same BB: ", lockUnlockSameBB)
		fmt.Fprintln(w, "Defer lock pairs in same BB: ", lockDeferUnlockSameBB)
		fmt.Fprintln(w, "Lock pairs dominate each other: ", lockUnlockPairDifferentBB)
		fmt.Fprintln(w, "Defer Lock pairs dominate each other: ", lockDeferUnlockPairDifferentBB)
		fmt.Fprintln(w, "Unsafe lock instructions that are dropped: ", unsafeLock)
		fmt.Fprintln(w, "Unpaired locks: ", unpaired)
		fmt.Fprintln(w, "Paired locks: ", paired)

		w.Flush()

		g, err := os.Create("lockDistribution/" + inputFile + ".txt")
		if err != nil {
			fmt.Println(err)
			return
		}
		defer g.Close()

		w2 := bufio.NewWriter(g)
		for key, value := range mPkg {
			fmt.Fprintln(w2, key+":"+strconv.Itoa(value))
		}
		w2.Flush()
	}
}
