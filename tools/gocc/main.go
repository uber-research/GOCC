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
var hotFuncMap map[string]empty = map[string]empty{}

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
var tokenToLuPair map[token.Pos]*luPair = map[token.Pos]*luPair{}
var pathToEndNodePos map[ast.Node]token.Pos

var writeOutput bool = true
var mCallGraph *callgraph.Graph
var curNode *ssa.Function
var outputPath string

// generate the name of the packages that violates HTM
var _blockList []string = []string{"os.", "io.", "fmt.", "runtime.", "syscall."}
var _blockedPkg []string = []string{"os", "io", "fmt", "runtime", "syscall"}

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

//TODO(milind): seems incorrect. Probably, we should base it on the Type.
func checkBlockList(rcv ssa.Value) bool {
	for _, name := range _blockList {
		if strings.HasPrefix(rcv.String(), name) {
			return true
		}
	}
	return false
}

func checkBlockListStr(pkg *ssa.Package) bool {
	for _, name := range _blockedPkg {
		if pkg.Pkg.Name() == name {
			return true
		}
	}
	return false
}

// TODO: This is not correct.
// In reality, we should check all callees of f, and disqualify it if we see calls to an unwanted function.
// For example, see lib/callgraph/builtin.go  for the set of build-in functions called, which do NOT emerge from a call instruction.
// For simplicity, we inspect only the call instructions and mark the calls to certain packages (fmt, io, runtime, ...) as unsafe.

func unsafeInst(ins ssa.Instruction, cgNode *callgraph.Node) bool {
	call, ok := ins.(ssa.CallInstruction)
	if !ok {
		return false
	}

	callRcv := call.Common().Value
	if callRcv != nil && checkBlockList(callRcv) {
		return true
	}
	if cgNode != nil {
		for _, edge := range cgNode.Out {
			if edge.Site == ins {
				if checkBlockListStr(edge.Callee.Func.Pkg) {
					return true
				}
			}
		}
	}
	return false
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
func isHotFunction(f *ssa.Function) bool {
	pkgPath := f.Pkg.Pkg.Path()
	splits := strings.Split(normalizeFunctionName(pkgPath), "/")
	canonicalName := splits[len(splits)-1]
	funcName := normalizeFunctionName(f.RelString(f.Pkg.Pkg))
	fullFuncName := canonicalName + "." + funcName

	if !profileProvided {
		return true
	}

	if _, ok := hotFuncMap[fullFuncName]; !ok {
		return false
	}
	return true
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
func rewriteAST(f ast.Node, pkg *packages.Package, points map[ast.Node]*luPoint) ast.Node {
	/*
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
	*/return nil
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

func initProfile(profilePath string) {
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
		hotFuncMap[strings.TrimSpace(line)] = emptyStruct
	}
}

func main() {
	// indicates a function is safe to use HTM or not. Local check only.
	mapFuncSafety = make(map[*ssa.Function]bool)
	lockInLambdaFunc = make(map[token.Pos]bool)
	blkstmtMap = make(map[token.Pos]bool)

	mPkg = make(map[string]int)
	pkgName = make(map[string]bool)
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
	flag.StringVar(&inputFile, "input", "testdata/test24.go", "source file to analyze")

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
		initProfile(profilePath)
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
	mCallGraph := libcg.BuildRtaCG(prog, true)

	// TODO: first pass on optimized form and second pass on naive form to check if it is a value or object

	funcSummaryMap := map[*ssa.Function]*functionSummary{}
	globalLUPoints := map[*luPoint]ssa.Instruction{}

	for node := range mCallGraph.Nodes {
		m, r, w, ptToIns, insToPt := collectLUPoints(node)
		funcSummaryMap[node] = &functionSummary{
			f:       node,
			ptToIns: ptToIns,
			insToPt: insToPt,
			m:       m,
			r:       r,
			w:       w,
		}
		for k, v := range ptToIns {
			globalLUPoints[k] = v
		}
		//We'll compute the remaining summary after alias analysis
	}

	// Do alias analysis
	collectPointsToSet(ssapkgs, globalLUPoints, isSingleFile, *syntheticPtr)

	// Greedily compute function summaries (can be done on need basis also)
	for f, n := range mCallGraph.Nodes {
		funcSummaryMap[f].compute(n)
	}

	// Set isLambda
	for f, _ := range funcSummaryMap {
		for _, c := range f.AnonFuncs {
			funcSummaryMap[c].isLambda = true
		}
	}

	// per function
	luPairs := []*luPair{}
	for f, _ := range funcSummaryMap {
		if !isHotFunction(f) {
			continue
		}
		if f.Name() == "main" || f.Name() == "foo" || f.Name() == "bar" {
			fmt.Println("..")
		}
		pairs := collectLUPairs(f, funcSummaryMap, mCallGraph.Nodes)
		luPairs = append(luPairs, pairs...)
	}

	// maintain position to LU-pair mapping
	for _, lu := range luPairs {
		tokenToLuPair[lu.l.pos()] = lu
		tokenToLuPair[lu.u.pos()] = lu
	}

	fmt.Println(len(replacedMutexVal) + len(replacedMutexPtr) + len(replacedRWMutexVal) + len(replacedRWMutexPtr))
	for _, pkg := range pkgs {
		usesMap = pkg.TypesInfo.Uses
		typesMap = pkg.TypesInfo.Types
		for _, file := range pkg.Syntax {
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

			// find the pairs that are in this file
			filteredPoints := map[ast.Node]*luPoint{}
			for _, lu := range luPairs {

				lPath, ok := astutil.PathEnclosingInterval(file, lu.l.pos(), lu.l.pos())
				if !ok {
					continue
				}
				uPath, ok := astutil.PathEnclosingInterval(file, lu.u.pos(), lu.u.pos())
				if !ok {
					continue
				}
				lu.l.astPath = lPath
				lu.u.astPath = uPath
				filteredPoints[lPath[0]] = lu.l
				filteredPoints[uPath[0]] = lu.u
			}

			if len(filteredPoints) > 0 {
				fmt.Printf("%v has %v locks to rewrite\n", prog.Fset.Position(file.Pos()).Filename, len(filteredPoints))
			}
			if len(filteredPoints) > 0 && writeOutput {
				fmt.Printf("Number of locks to rewrite %v\n", len(filteredPoints))
				collectBlkstmt(file, pkg)
				ast := rewriteAST(file, pkg, filteredPoints)
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
