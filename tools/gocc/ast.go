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
	"fmt"
	"go/ast"
	"go/format"
	"go/token"
	"go/types"
	"log"
	"os"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/tools/go/ast/astutil"
	"golang.org/x/tools/go/packages"
)

// this map will mark which lock position and paths are in lambda function
// since it will have different namescope
//var _lockInLambdaFunc map[token.Pos]bool

const _rtmLibPath = "github.com/uber-research/GOCC/tools/gocc/rtmlib"

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

// adds context variable definition at the beginning of the function's statement list
func insertOptiLockDecl(stmtsList *[]ast.Stmt, sigPos token.Pos, count map[int]empty) {
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
			v := nearestFunctionLit(pt)
			if v == nil {
				panic("findNearestFunctionLit not found!!")
			}
			if m, ok := funcLitMap[v]; ok {
				m[pt.id] = emptyStruct
			} else {
				funcLitMap[v] = map[int]empty{pt.id: emptyStruct}
			}
		} else {
			v := nearestFunctionDecl(pt)
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

func nearestFunctionDecl(pt *luPoint) *ast.FuncDecl {
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
func nearestFunctionLit(pt *luPoint) *ast.FuncLit {
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

func (g *gocc) transform() {
	for _, pkg := range g.pkgs {
		for _, file := range pkg.Syntax {
			// don't rewrite testing file by default
			if g.rewriteTestFile == false {
				fName := pkg.Fset.Position(file.Pos()).Filename
				if strings.HasSuffix(fName, "_test.go") {
					// fmt.Printf("%v is skipped since it is a testing file\n", fName)
					continue
				}
			}
			filteredPoints, conversionMap, funcDeclMap, funcLitMap := processASTFile(pkg, file, g.luPairs)

			if len(filteredPoints) > 0 {
				fmt.Printf("%v has %v locks to rewrite\n", g.prog.Fset.Position(file.Pos()).Filename, len(filteredPoints))
			}
			if len(filteredPoints) > 0 && !g.dryrun {
				fmt.Printf("Number of locks to rewrite %v\n", len(filteredPoints))
				ast := mutateAST(file, pkg, conversionMap, funcDeclMap, funcLitMap)
				filename := g.prog.Fset.Position(ast.Pos()).Filename
				g.serializeAST(ast, g.inputFile, pkg, filename)
			}
		}
	}
}

func mutateAST(f ast.Node, pkg *packages.Package, conversionMap map[*ast.CallExpr]*ast.CallExpr, funcDeclMap map[*ast.FuncDecl]map[int]empty, funcLitMap map[*ast.FuncLit]map[int]empty) ast.Node {
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
					insertOptiLockDecl(&(n.Body.List), n.Name.NamePos, v)
				}
			}
		case *ast.FuncLit:
			{
				if v, ok := funcLitMap[n]; ok {
					insertOptiLockDecl(&(n.Body.List), n.Body.Lbrace, v)
				}
			}
		}
		return true
	}
	return astutil.Apply(f, nil, postFunc)
}

func (g *gocc) serializeAST(f ast.Node, sourceFilePath string, pkg *packages.Package, filename string) {
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
