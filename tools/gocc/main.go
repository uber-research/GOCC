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
	"log"
	"os"
	"strconv"
	"strings"

	libbuilder "github.com/uber-research/GOCC/lib/builder"
	libcg "github.com/uber-research/GOCC/lib/callgraph"
	"golang.org/x/tools/go/callgraph"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
)

// TODO: manual SSA to detect a unique name for the majic lock
const _optiLockName = "optiLock"

// generate the name of the packages that violates HTM
//var _blockList []string = []string{"os.", "io.", "fmt.", "runtime.", "syscall."}
//var _blockedPkg []string = []string{"os", "io", "fmt", "runtime", "syscall"}

var _blockList []string = []string{"os.", "io.", "fmt.", "runtime.", "syscall."}
var _blockedPkg []string = []string{"os", "io", "fmt", "runtime", "syscall"}

type gocc struct {
	mPkg            map[string]int
	lockAliasMap    map[ssa.Value][]ssa.Value
	pkgName         map[string]empty
	hotFuncMap      map[string]empty
	funcSummaryMap  map[*ssa.Function]*functionSummary
	allLUPoints     map[*luPoint]ssa.Instruction
	allLUFunc       map[*luPoint]*ssa.Function
	pkgLpoints      map[string]int
	luPairs         []*luPair
	pkgs            []*packages.Package
	ssapkgs         []*ssa.Package
	prog            *ssa.Program
	cg              *callgraph.Graph
	outputPath      string
	inputFile       string
	profilePath     string
	isSingleFile    bool
	hasProfile      bool
	rewriteTestFile bool
	synthetic       bool
	verbose         bool
	stat            bool
	dryrun          bool
}

func NewGOCC(isSingleFile bool, rewriteTestFile bool, synthetic bool, verbose bool, stat bool, dryrun bool, inputFile string) *gocc {
	return &gocc{
		mPkg:            make(map[string]int),
		lockAliasMap:    make(map[ssa.Value][]ssa.Value),
		pkgName:         make(map[string]empty),
		hotFuncMap:      make(map[string]empty),
		inputFile:       inputFile,
		isSingleFile:    isSingleFile,
		hasProfile:      false,
		rewriteTestFile: rewriteTestFile,
		synthetic:       synthetic,
		verbose:         verbose,
		stat:            stat,
		dryrun:          dryrun,
	}
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
func isSSAValueAMutexPointer(rcv ssa.Value) bool {
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
	if pkg == nil {
		return false
	}
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
func (g *gocc) isSameLock(lockVal, unlockVal ssa.Value) bool {
	if lockVal.String() == unlockVal.String() {
		return true
	}
	if aliasSet, ok := g.lockAliasMap[lockVal]; ok {
		for _, val := range aliasSet {
			if val.String() == unlockVal.String() {
				return true
			}
		}
	}
	return false
}

const _tally = "/Users/milind//gocode/src/github.com/uber-go/tally/"
const _gocache = "/Users/milind/gocode/src/github.com/patrickmn/go-cache/"
const _fastcache = "/Users/milind/gocode/src/github.com/VictoriaMetrics/fastcache"
const _zap = "/Users/milind/gocode/src/go.uber.org/zap/"

func (g *gocc) collectAllLUPoints() {
	g.funcSummaryMap = map[*ssa.Function]*functionSummary{}
	g.allLUPoints = map[*luPoint]ssa.Instruction{}
	g.allLUFunc = map[*luPoint]*ssa.Function{}
	g.pkgLpoints = map[string]int{}

	totW := 0
	for node := range g.cg.Nodes {
		m, r, w, ptToIns, insToPt := collectLUPoints(node)
		g.funcSummaryMap[node] = &functionSummary{
			f:       node,
			ptToIns: ptToIns,
			insToPt: insToPt,
			m:       m,
			r:       r,
			w:       w,
		}
		// if node.Pkg.Pkg.Name() == "cache" {
		// 	fmt.Printf("pkg = %s, func = %v, m=%d, r = %d, w = %d\n", node.Pkg.Pkg.Name(), node.Name(), len(m.d)+len(m.l)+len(m.u), len(r.d)+len(r.l)+len(r.u), len(w.d)+len(w.l)+len(w.u))
		// }
		totW += len(w.d) + len(w.l) + len(w.u)
		for k, v := range ptToIns {
			g.allLUPoints[k] = v
			g.allLUFunc[k] = node
		}

		if node.Pkg == nil {
			continue
		}
		pkg := node.Pkg.Pkg.Name()

		if _, ok := g.pkgLpoints[pkg]; !ok {
			g.pkgLpoints[pkg] = len(ptToIns)
		} else {
			g.pkgLpoints[pkg] += len(ptToIns)
		}

		//We'll compute the remaining summary after alias analysis
	}
	fmt.Printf("totw = %d\n", totW)
}

func (g *gocc) annotateLambda() {
	for f, _ := range g.funcSummaryMap {
		for _, c := range f.AnonFuncs {
			if _, ok := g.funcSummaryMap[c]; ok {
				g.funcSummaryMap[c].isLambda = true
			} else {
				log.Printf("lambda %v not found in funcSummaryMap\n", c)
			}
		}
	}
}

func (g *gocc) collectAllLUPairs() {
	g.luPairs = []*luPair{}
	for f, _ := range g.funcSummaryMap {
		if !g.isHotFunction(f) {
			continue
		}
		//		if f.Name() == "Counter" /*|| f.Name() == "foo" || f.Name() == "bax" */ {
		//			fmt.Println("..")
		//		}
		pairs := collectLUPairs(f, g.funcSummaryMap, g.cg.Nodes)
		g.luPairs = append(g.luPairs, pairs...)
	}
}

func (g *gocc) buildCG() {
	pkgConfig := &packages.Config{Mode: packages.LoadAllSyntax, Tests: true}
	var err error
	g.pkgs, err = packages.Load(pkgConfig, g.inputFile)
	if err != nil {
		log.Fatalf("something wrong during loading! %v", err)
	}

	for _, pkg := range g.pkgs {
		g.pkgName[pkg.Name] = emptyStruct
	}
	g.prog, g.ssapkgs = ssautil.AllPackages(g.pkgs /*ssa.BuilderMode(0)*/, ssa.NaiveForm|ssa.GlobalDebug)
	libbuilder.BuildPackages(g.prog, g.ssapkgs, true, true)
	g.cg = libcg.BuildRtaCG(g.prog, true)
}

func (g *gocc) dumpInfo() {
	if g.verbose {
		pkgLupair := map[string][]*luPair{}
		for _, p := range g.luPairs {
			f, _ := g.allLUFunc[p.l]
			if f.Pkg == nil {
				continue
			}
			pkg := f.Pkg.Pkg.Name()

			if v, ok := pkgLupair[pkg]; !ok {
				pkgLupair[pkg] = []*luPair{p}
			} else {
				pkgLupair[pkg] = append(v, p)
			}
		}
		log.Printf("Num pkgs containing at least one lock =  %d\n", len(g.pkgLpoints))
		log.Printf("Num pkgs containing at least one paired lock =  %d\n", len(pkgLupair))
		for p, sl := range pkgLupair {
			for _, s := range sl {
				f, _ := g.allLUFunc[s.l]
				log.Printf("lupair in pkg %v, func %v\n", p, f)
			}
		}
		for p, num := range g.pkgLpoints {
			sl, ok := pkgLupair[p]
			lenPairs := 0
			if ok {
				lenPairs = len(sl)
			}
			log.Printf("pkg %v: lupoints = %d, lupairs= %d\n", p, num, lenPairs)
		}
	}
}

func (g *gocc) Process() {
	if info, err := os.Stat(g.profilePath); err == nil && !info.IsDir() {
		g.initProfile(g.profilePath)
	} else {
		log.Println("No valid profiles to apply")
	}

	if g.dryrun {
		log.Println("Running in dryrun mode - modified ASTs will not be written out.")
	}

	if info, err := os.Stat(g.inputFile); err == nil && info.IsDir() {
		g.inputFile += "/..."
	}

	g.buildCG()

	// TODO: first pass on optimized form and second pass on naive form to check if it is a value or object

	g.collectAllLUPoints()
	// Do alias analysis
	g.collectPointsToSet()
	if g.verbose {
		log.Printf("globalLUPoints = %d", len(g.allLUPoints))
	}

	// Greedily compute function summaries (can be done on need basis also)
	for f, n := range g.cg.Nodes {
		g.funcSummaryMap[f].compute(n)
	}
	g.annotateLambda()
	g.collectAllLUPairs()
	log.Printf("total luPairs = %d", len(g.luPairs))
	// order by package

	g.dumpInfo()
	dumpMetrics(g.funcSummaryMap)
	g.transform()

	if g.stat {
		g.inputFile = strings.Replace(g.inputFile, "/", "_", -1)

		f, err := os.Create("lockcount/" + g.inputFile + ".txt")
		if err != nil {
			fmt.Println(err)
			return
		}
		defer f.Close()

		w := bufio.NewWriter(f)
		w.Flush()
		d, err := os.Create("lockDistribution/" + g.inputFile + ".txt")
		if err != nil {
			fmt.Println(err)
			return
		}
		defer d.Close()
		w2 := bufio.NewWriter(d)
		for key, value := range g.mPkg {
			fmt.Fprintln(w2, key+":"+strconv.Itoa(value))
		}
		w2.Flush()
	}
}

func main() {
	var profilePath string
	var inputFile string
	dryrunPtr := flag.Bool("dryrun", false, "run without changes to files")
	statsPtr := flag.Bool("stats", false, "dump out the stats information of the locks")
	verbose := flag.Bool("verbose", true, "verbose output")
	flag.StringVar(&inputFile, "input", _zap, "go source (file/dir) to analyze")
	flag.StringVar(&profilePath, "profile", "", "use this profile file to filter hot functions")
	syntheticPtr := flag.Bool("synthetic", false, "a synthetic main is crated")
	rewriteTestFile := flag.Bool("rewriteTest", false, "rewrite _test.go files")
	flag.Parse()

	if inputFile == "" {
		fmt.Println("Please provide the input!")
		flag.PrintDefaults()
		os.Exit(1)
	}

	gizer := NewGOCC(strings.HasSuffix(inputFile, ".go"), *rewriteTestFile, *syntheticPtr, *verbose, *statsPtr, *dryrunPtr, inputFile)
	gizer.Process()
}
