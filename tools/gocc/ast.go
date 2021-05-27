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
	"go/types"
	"log"
	"os"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/tools/go/ast/astutil"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
)

// this map will mark which lock position and paths are in lambda function
// since it will have different namescope
//var _lockInLambdaFunc map[token.Pos]bool

const _rtmLibPath = "github.com/uber-research/GOCC/tools/gocc/rtmlib"

// this map will keep the all blkstmt position whose parent is funclit
var _blkstmtMap map[token.Pos]bool = map[token.Pos]bool{}

func isPromotedField(e ast.Expr, typesMap map[ast.Expr]types.TypeAndValue) bool {
	if v, ok := typesMap[e]; ok {
		switch v.Type.String() {
		case "*sync.Mutex", "sync.Mutex", "*sync.RWMutex", "sync.RWMutex":
			return false
		default:
			return true
		}
	}
	panic("Not present in _typesMap!!")
}

type luConsts struct {
	method         string
	receiver       string
	optiMethodName string
	promotedId     string
}

var luTypeToStr [TYPE_MAX]luConsts = [TYPE_MAX]luConsts{
	LOCK:          luConsts{"Lock", "sync.Mutex", "Lock", "Mutex"},
	UNLOCK:        luConsts{"Unlock", "sync.Mutex", "Unlock", "Mutex"},
	DEFER_UNLOCK:  luConsts{"Unlock", "sync.Mutex", "Unlock", "Mutex"},
	WLOCK:         luConsts{"Lock", "sync.RWMutex", "WLock", "RWMutex"},
	WUNLOCK:       luConsts{"Unlock", "sync.RWMutex", "WUnlock", "RWMutex"},
	DEFER_WUNLOCK: luConsts{"Unlock", "sync.RWMutex", "WUnlock", "RWMutex"},
	RLOCK:         luConsts{"RLock", "sync.RWMutex", "RLock", "RWMutex"},
	RUNLOCK:       luConsts{"RUnlock", "sync.RWMutex", "RUnlock", "RWMutex"},
	DEFER_RUNLOCK: luConsts{"RUnlock", "sync.RWMutex", "RUnlock", "RWMutex"},
	UNKNOWN:       luConsts{},
}

func ensureMethodMatch(sel *ast.SelectorExpr, lup *luPoint) bool {
	if sel.Sel.Name == luTypeToStr[lup.luType()].method {
		return true
	}
	return false
}

// return if the ssa function is in the given input package so that we can transform
func (g *gocc) inFile(ssaF *ssa.Function) bool {
	if _, ok := g.pkgName[ssaF.Pkg.Pkg.Name()]; ok {
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

func singlePathContains(singlePath []ast.Node, curPos token.Pos) bool {
	for _, node := range singlePath {
		if node.Pos() == curPos {
			return true
		}
	}
	return false
}

// adds context variable definition at the beginning of the function's statement list
func addContextInitStmt(stmtsList *[]ast.Stmt, sigPos token.Pos, count map[int]empty) {
	// Get sorted count for stable diff
	countSorted := make([]int, len(count))
	pos := 0
	for i := range count {
		countSorted[pos] = i
		pos++
	}
	sort.Ints(countSorted)

	for _, i := range countSorted {
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

func processASTFile(pkg *packages.Package, file *ast.File, luPairs []*luPair) (map[ast.Node]*luPoint, map[*ast.CallExpr]*ast.CallExpr, map[*ast.FuncDecl]map[int]empty, map[*ast.FuncLit]map[int]empty) {

	log.Printf("AST file %v\n", file.Name.Name)
	typesMap := pkg.TypesInfo.Types
	filteredPoints := map[ast.Node]*luPoint{}
	conversionMap := map[*ast.CallExpr]*ast.CallExpr{}
	funcDeclMap := map[*ast.FuncDecl]map[int]empty{}
	funcLitMap := map[*ast.FuncLit]map[int]empty{}

	for _, lu := range luPairs {
		for _, pt := range []*luPoint{lu.l, lu.u} {
			path, ok := astutil.PathEnclosingInterval(file, pt.pos(), pt.pos())
			if !ok {
				continue
			}

			callExpr, ok := path[0].(*ast.CallExpr)
			if !ok {
				panic("not a call!")
			}
			se, ok := callExpr.Fun.(*ast.SelectorExpr)
			if !ok {
				panic("not a SelectorExpr!")
			}
			if !ensureMethodMatch(se, pt) {
				panic("method mismatch!")
			}

			theSel := &ast.Ident{
				Name:    pt.getOptiMethod(),
				NamePos: se.Sel.NamePos,
			}

			fun := &ast.SelectorExpr{
				X: &ast.Ident{
					Name:    _optiLockName + strconv.Itoa(pt.id),
					NamePos: se.X.Pos(),
				},
				Sel: theSel,
			}

			theExpression := se.X
			if isPromotedField(theExpression, typesMap) {
				theExpression = &ast.SelectorExpr{
					X:   theExpression,
					Sel: ast.NewIdent(pt.getPromotedIdentifier()),
				}
			}

			if pt.isPointer {
				theExpression = &ast.UnaryExpr{
					Op: token.AND,
					X:  theExpression,
				}
			}

			newCall := &ast.CallExpr{
				Fun:      fun,
				Lparen:   token.NoPos,
				Args:     []ast.Expr{theExpression},
				Ellipsis: token.NoPos,
				Rparen:   token.NoPos,
			}

			conversionMap[callExpr] = newCall
			pt.astPath = path
			filteredPoints[path[0]] = pt
		}
	}

	// Now, among the filteredPoints, identify where the declaration should go, and how many

	for _, pt := range filteredPoints {
		if pt.isLambda {
			v := findNearestFunctionLit(pt)
			if v == nil {
				panic("findNearestFunctionLit not found!!")
			}
			if m, ok := funcLitMap[v]; ok {
				m[pt.id] = emptyStruct
			} else {
				funcLitMap[v] = map[int]empty{pt.id: emptyStruct}
			}
		} else {
			v := findNearestFunctionDecl(pt)
			if v == nil {
				panic("findNearestFunctionDecl not found!!")
			}
			if m, ok := funcDeclMap[v]; ok {
				m[pt.id] = emptyStruct
			} else {
				funcDeclMap[v] = map[int]empty{pt.id: emptyStruct}
			}
		}
	}
	return filteredPoints, conversionMap, funcDeclMap, funcLitMap
}

func findNearestFunctionDecl(pt *luPoint) *ast.FuncDecl {
	for i := 0; i < len(pt.astPath); i++ {
		if _, ok := pt.astPath[i].(*ast.BlockStmt); ok {
			/* TODO: can't find Name if blk.Name() != "Body" {
				continue
			} */
			if i+1 < len(pt.astPath) {
				if decl, ok := pt.astPath[i+1].(*ast.FuncDecl); ok {
					return decl
				}
			}
		}
	}
	return nil
}
func findNearestFunctionLit(pt *luPoint) *ast.FuncLit {
	for i := 0; i < len(pt.astPath); i++ {
		if _, ok := pt.astPath[i].(*ast.BlockStmt); ok {
			/* TODO: can't find Name if blk.Name() != "Body" {
				continue
			} */
			if i+1 < len(pt.astPath) {
				if lit, ok := pt.astPath[i+1].(*ast.FuncLit); ok {
					return lit
				}
			}
		}
	}
	return nil
}

func (g *gocc) mapSSAtoAST(prog *ssa.Program, pkgs []*packages.Package, luPairs []*luPair) {
	for _, pkg := range pkgs {
		for _, file := range pkg.Syntax {
			// don't rewrite testing file by default
			if g.rewriteTestFile == false {
				fName := pkg.Fset.Position(file.Pos()).Filename
				if strings.HasSuffix(fName, "_test.go") {
					// fmt.Printf("%v is skipped since it is a testing file\n", fName)
					continue
				}
			}
			filteredPoints, conversionMap, funcDeclMap, funcLitMap := processASTFile(pkg, file, luPairs)

			if len(filteredPoints) > 0 {
				fmt.Printf("%v has %v locks to rewrite\n", prog.Fset.Position(file.Pos()).Filename, len(filteredPoints))
			}
			if len(filteredPoints) > 0 && !g.dryrun {
				fmt.Printf("Number of locks to rewrite %v\n", len(filteredPoints))
				ast := rewriteAST(file, pkg, conversionMap, funcDeclMap, funcLitMap)
				filename := prog.Fset.Position(ast.Pos()).Filename
				g.writeAST(ast, g.inputFile, pkg, filename)
			}
		}
	}
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
func rewriteAST(f ast.Node, pkg *packages.Package, conversionMap map[*ast.CallExpr]*ast.CallExpr, funcDeclMap map[*ast.FuncDecl]map[int]empty, funcLitMap map[*ast.FuncLit]map[int]empty) ast.Node {
	fmt.Println("  Rewriting field accesses in the file...")
	addImport := false
	postFunc := func(c *astutil.Cursor) bool {
		node := c.Node()
		switch n := node.(type) {
		case *ast.CallExpr:
			{
				if v, ok := conversionMap[n]; ok {
					c.Replace(v)
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
							Value:    strconv.Quote(_rtmLibPath),
						},
						Comment: n.Comment,
						EndPos:  n.EndPos,
					}
					c.InsertAfter(newImport)
					addImport = true
				}
			}
		case *ast.FuncDecl:
			{
				if v, ok := funcDeclMap[n]; ok {
					addContextInitStmt(&(n.Body.List), n.Name.NamePos, v)
				}
			}
		case *ast.FuncLit:
			{
				if v, ok := funcLitMap[n]; ok {
					addContextInitStmt(&(n.Body.List), n.Body.Lbrace, v)
				}
			}
		}
		return true
	}
	return astutil.Apply(f, nil, postFunc)
}

func (g *gocc) writeAST(f ast.Node, sourceFilePath string, pkg *packages.Package, filename string) {
	if g.dryrun {
		return
	}
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
