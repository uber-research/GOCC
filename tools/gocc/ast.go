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
	"fmt"
	"go/ast"
	"go/format"
	"go/printer"
	"go/token"
	"log"
	"os"
	"strconv"

	"golang.org/x/tools/go/ast/astutil"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
)

// this map will mark which lock position and paths are in lambda function
// since it will have different namescope
var _lockInLambdaFunc map[token.Pos]bool

// this map will keep the all blkstmt position whose parent is funclit
var _blkstmtMap map[token.Pos]bool = map[token.Pos]bool{}
var _tokenToLuPair map[token.Pos]*luPair = map[token.Pos]*luPair{}
var _pathToEndNodePos map[ast.Node]token.Pos = map[ast.Node]token.Pos{}

// return if the ssa function is in the given input package so that we can transform
func inFile(ssaF *ssa.Function) bool {
	if _, ok := _pkgName[ssaF.Pkg.Pkg.Name()]; ok {
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
					if str, ok := posToID[_pathToEndNodePos[n]]; ok {
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
				} else if _, ok := _blkstmtMap[n.Pos()]; ok {
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
			Lhs:    []ast.Expr{ast.NewIdent(_optiLockName + strconv.Itoa(i))},
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
					_blkstmtMap[c.Node().Pos()] = true
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
												Name:    _optiLockName + getPosName(*replacePathMutex, n.Pos(), posToID),
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
												Name:    _optiLockName + getPosName(*replacePathMutex, n.Pos(), posToID),
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
												Name:    _optiLockName + getPosName(*insertPathMutex, n.Pos(), posToID),
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
												Name:    _optiLockName + getPosName(*insertPathMutex, n.Pos(), posToID),
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
												Name:    _optiLockName + getPosName(*replacePathRWMutex, n.Pos(), posToID),
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
												Name:    _optiLockName + getPosName(*replacePathRWMutex, n.Pos(), posToID),
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
												Name:    _optiLockName + getPosName(*replacePathRWMutex, n.Pos(), posToID),
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
												Name:    _optiLockName + getPosName(*replacePathRWMutex, n.Pos(), posToID),
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
												Name:    _optiLockName + getPosName(*insertPathRWMutex, n.Pos(), posToID),
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
												Name:    _optiLockName + getPosName(*insertPathRWMutex, n.Pos(), posToID),
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
												Name:    _optiLockName + getPosName(*insertPathRWMutex, n.Pos(), posToID),
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
												Name:    _optiLockName + getPosName(*insertPathRWMutex, n.Pos(), posToID),
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
	if _writeOutput {
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
