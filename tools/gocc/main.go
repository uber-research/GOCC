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
	"flag"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
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
const _optiLockName = "optiLock"

var majicLockID = 0

var usesMap map[*ast.Ident]types.Object
var typesMap map[ast.Expr]types.TypeAndValue

// _isSingleFile differentiate a single file and a package
var _isSingleFile bool

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

var _mPkg map[string]int = map[string]int{}
var lockAliasMap map[ssa.Value][]ssa.Value
var _pkgName map[string]empty = map[string]empty{}

var _writeOutput bool = true
var _outputPath string

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

func main() {
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

	_outputPath = inputFile + "/"

	if *dryrunPtr {
		_writeOutput = false
		fmt.Println("Running in dryrun mode - modified ASTs will not be written out.")
	} else {
		_writeOutput = true
	}

	if strings.HasSuffix(inputFile, ".go") {
		_isSingleFile = true
	} else {
		_isSingleFile = false
		inputFile += "/..."
	}

	var pkgs []*packages.Package

	pkgConfig := &packages.Config{Mode: packages.LoadAllSyntax, Tests: true}

	pkgs, err := packages.Load(pkgConfig, inputFile)
	if err != nil {
		panic("something wrong during loading!")
	}

	for _, pkg := range pkgs {
		_pkgName[pkg.Name] = emptyStruct
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
	collectPointsToSet(ssapkgs, globalLUPoints, _isSingleFile, *syntheticPtr)

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
		_tokenToLuPair[lu.l.pos()] = lu
		_tokenToLuPair[lu.u.pos()] = lu
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
			if len(filteredPoints) > 0 && _writeOutput {
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
		for key, value := range _mPkg {
			fmt.Fprintln(w2, key+":"+strconv.Itoa(value))
		}
		w2.Flush()
	}
}
