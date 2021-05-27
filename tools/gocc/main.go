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
	"go/token"
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
var unReachableLambdas = 0

type metrics struct {
	mLock        int
	mUnlock      int
	mDeferUnlock int

	wLock        int
	wUnlock      int
	wDeferUnlock int

	rLock        int
	rUnlock      int
	rDeferUnlock int

	pairedMlock int
	pairedWlock int
	pairedRlock int

	pairedMDeferlock int
	pairedWDeferlock int
	pairedRDeferlock int

	candidateMutex  int
	candidateWMutex int
	candidateRMutex int

	intraRegionIO    int
	interRegionIO    int
	intraRegionAlias int
	interRegionAlias int
	dominaceRelation int
}

func (m *metrics) sum(a metrics) {
	m.mLock += a.mLock
	m.mUnlock += a.mUnlock
	m.mDeferUnlock += a.mDeferUnlock

	m.wLock += a.wLock
	m.wUnlock += a.wUnlock
	m.wDeferUnlock += a.wDeferUnlock

	m.rLock += a.rLock
	m.rUnlock += a.rUnlock
	m.rDeferUnlock += a.rDeferUnlock

	m.pairedMlock += a.pairedMlock
	m.pairedWlock += a.pairedWlock
	m.pairedRlock += a.pairedRlock

	m.pairedMDeferlock += a.pairedMDeferlock
	m.pairedWDeferlock += a.pairedWDeferlock
	m.pairedRDeferlock += a.pairedRDeferlock

	m.candidateMutex += a.candidateMutex
	m.candidateWMutex += a.candidateWMutex
	m.candidateRMutex += a.candidateRMutex

	m.intraRegionIO += a.intraRegionIO
	m.interRegionIO += a.interRegionIO
	m.intraRegionAlias += a.intraRegionAlias
	m.interRegionAlias += a.interRegionAlias
	m.dominaceRelation += a.dominaceRelation
}

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
	outputPath      string
	inputFile       string
	profilePath     string
	isSingleFile    bool
	profileProvided bool
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
		profileProvided: false,
		rewriteTestFile: rewriteTestFile,
		synthetic:       synthetic,
		verbose:         verbose,
		dryrun:          dryrun,
		stat:            stat,
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

func dumpMetrics(s map[*ssa.Function]*functionSummary) {
	var totalMetric metrics
	packageMetrics := map[string]*metrics{}

	for k, v := range s {
		if k.Pkg == nil {
			continue
		}

		mtr, ok := packageMetrics[k.Pkg.Pkg.Name()]
		if !ok {
			mtr = &metrics{}
			packageMetrics[k.Pkg.Pkg.Name()] = mtr
		}
		mtr.sum(v.metric)
		totalMetric.sum(v.metric)
	}

	var totalMutex, totalW, totalR, totralRW, nonDeferPair, deferPair, totalPair int
	fmt.Printf("\npkg,mLock,mUnlock,mDeferUnlock,sync.Mutex,wLock,wUnlock,wDeferUnlock,RWMutex+W,rLock,rUnlock,rDeferUnlock,RWMutex+R,RWMutex,candidateMutex,candidateWMutex,candidateRutex,pairedMlock,pairedWlock,pairedRlock,Paired(nondefer),pairedMDeferlock,pairedWDeferlock,pairedRDeferlock,Paired(defer),Paired(total),intraRegionIO,interRegionIO,intraRegionAlias,interRegionAlias,dominaceRelation\n")
	for k, m := range packageMetrics {
		mutex := m.mLock + m.mUnlock + m.mDeferUnlock
		totalMutex += mutex
		wMutex := m.wLock + m.wUnlock + m.wDeferUnlock
		totalW += wMutex
		rMutex := m.rLock + m.rUnlock + m.rDeferUnlock
		totalR += rMutex
		totralRW += wMutex + rMutex

		lnonDeferPair := m.pairedMlock + m.pairedWlock + m.pairedRlock
		nonDeferPair += lnonDeferPair
		lDefPair := m.pairedMDeferlock + m.pairedWDeferlock + m.pairedRDeferlock
		deferPair += lDefPair
		totalPair += lDefPair + lnonDeferPair
		fmt.Printf("%s, %d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d\n", k,
			m.mLock, m.mUnlock, m.mDeferUnlock, mutex,
			m.wLock, m.wUnlock, m.wDeferUnlock, wMutex,
			m.rLock, m.rUnlock, m.rDeferUnlock, rMutex, rMutex+wMutex,
			m.candidateMutex, m.candidateWMutex, m.candidateRMutex,
			m.pairedMlock, m.pairedWlock, m.pairedRlock, lnonDeferPair,
			m.pairedMDeferlock, m.pairedWDeferlock, m.pairedRDeferlock, lDefPair, lDefPair+lnonDeferPair,
			m.intraRegionIO, m.interRegionIO, m.intraRegionAlias, m.interRegionAlias, m.dominaceRelation)
	}

	m := totalMetric
	fmt.Printf("%s,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d\n", "total",
		m.mLock, m.mUnlock, m.mDeferUnlock, totalMutex,
		m.wLock, m.wUnlock, m.wDeferUnlock, totalW,
		m.rLock, m.rUnlock, m.rDeferUnlock, totalR, totralRW,
		m.candidateMutex, m.candidateWMutex, m.candidateRMutex,
		m.pairedMlock, m.pairedWlock, m.pairedRlock, nonDeferPair,
		m.pairedMDeferlock, m.pairedWDeferlock, m.pairedRDeferlock, deferPair, totalPair,
		m.intraRegionIO, m.interRegionIO, m.intraRegionAlias, m.interRegionAlias, m.dominaceRelation)

}

func (g *gocc) Process() {
	if info, err := os.Stat(g.profilePath); err == nil && !info.IsDir() {
		g.initProfile(g.profilePath)
	} else {
		log.Println("No valid profiles to apply")
	}

	if g.dryrun {
		fmt.Println("Running in dryrun mode - modified ASTs will not be written out.")
	}

	if info, err := os.Stat(g.inputFile); err == nil && info.IsDir() {
		g.inputFile += "/..."
	}

	var pkgs []*packages.Package

	pkgConfig := &packages.Config{Mode: packages.LoadAllSyntax, Tests: true}

	pkgs, err := packages.Load(pkgConfig, g.inputFile)
	if err != nil {
		panic("something wrong during loading!")
	}

	for _, pkg := range pkgs {
		g.pkgName[pkg.Name] = emptyStruct
	}

	prog, ssapkgs := ssautil.AllPackages(pkgs /*ssa.BuilderMode(0)*/, ssa.NaiveForm|ssa.GlobalDebug)
	// prog, ssapkgs := ssautil.AllPackages(pkgs, ssa.GlobalDebug)
	libbuilder.BuildPackages(prog, ssapkgs, true, true)
	mCallGraph := libcg.BuildRtaCG(prog, true)

	// TODO: first pass on optimized form and second pass on naive form to check if it is a value or object

	funcSummaryMap := map[*ssa.Function]*functionSummary{}
	globalLUPoints := map[*luPoint]ssa.Instruction{}
	globalLUFunc := map[*luPoint]*ssa.Function{}

	pkgLpoints := map[string]int{}

	totW := 0
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
		// if node.Pkg.Pkg.Name() == "cache" {
		// 	fmt.Printf("pkg = %s, func = %v, m=%d, r = %d, w = %d\n", node.Pkg.Pkg.Name(), node.Name(), len(m.d)+len(m.l)+len(m.u), len(r.d)+len(r.l)+len(r.u), len(w.d)+len(w.l)+len(w.u))
		// }
		totW += len(w.d) + len(w.l) + len(w.u)
		for k, v := range ptToIns {
			globalLUPoints[k] = v
			globalLUFunc[k] = node
		}

		if node.Pkg == nil {
			continue
		}
		pkg := node.Pkg.Pkg.Name()

		if _, ok := pkgLpoints[pkg]; !ok {
			pkgLpoints[pkg] = len(ptToIns)
		} else {
			pkgLpoints[pkg] += len(ptToIns)
		}

		//We'll compute the remaining summary after alias analysis
	}
	fmt.Printf("totw = %d\n", totW)

	// Do alias analysis
	g.collectPointsToSet(ssapkgs, globalLUPoints)
	if g.verbose {
		log.Printf("globalLUPoints = %d", len(globalLUPoints))
	}

	// Greedily compute function summaries (can be done on need basis also)
	for f, n := range mCallGraph.Nodes {
		if f.Pkg != nil && f.Pkg.Pkg.Name() == "cache" {
			fmt.Println("..")
		}
		funcSummaryMap[f].compute(n)
	}

	// Set isLambda
	for f, _ := range funcSummaryMap {
		for _, c := range f.AnonFuncs {
			if _, ok := funcSummaryMap[c]; ok {
				funcSummaryMap[c].isLambda = true
			} else {
				unReachableLambdas++
				log.Printf("lambda %v not found in funcSummaryMap\n", c)
			}
		}
	}

	// per function
	luPairs := []*luPair{}
	for f, _ := range funcSummaryMap {
		if !g.isHotFunction(f) {
			continue
		}
		//		if f.Name() == "Counter" /*|| f.Name() == "foo" || f.Name() == "bax" */ {
		//			fmt.Println("..")
		//		}
		pairs := collectLUPairs(f, funcSummaryMap, mCallGraph.Nodes)
		luPairs = append(luPairs, pairs...)
	}

	log.Printf("total luPairs = %d", len(luPairs))
	// order by package
	pkgLupair := map[string][]*luPair{}
	for _, p := range luPairs {
		f, _ := globalLUFunc[p.l]
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

	if g.verbose {
		log.Printf("Num pkgs containing at least one lock =  %d\n", len(pkgLpoints))
		log.Printf("Num pkgs containing at least one paired lock =  %d\n", len(pkgLupair))
		for p, sl := range pkgLupair {
			for _, s := range sl {
				f, _ := globalLUFunc[s.l]
				log.Printf("lupair in pkg %v, func %v\n", p, f)
			}
		}
		for p, num := range pkgLpoints {
			sl, ok := pkgLupair[p]
			lenPairs := 0
			if ok {
				lenPairs = len(sl)
			}
			log.Printf("pkg %v: lupoints = %d, lupairs= %d\n", p, num, lenPairs)
		}
	}
	dumpMetrics(funcSummaryMap)
	g.mapSSAtoAST(prog, pkgs, luPairs)

	if g.stat {
		g.inputFile = strings.Replace(g.inputFile, "/", "_", -1)

		f, err := os.Create("lockcount/" + g.inputFile + ".txt")
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
	// command-line argument
	dryrunPtr := flag.Bool("dryrun", false, "indicates if AST will be written out or not")
	statsPtr := flag.Bool("stats", false, "dump out the stats information of the locks")
	verbose := flag.Bool("verbose", true, "verbose output")

	var inputFile string
	//	flag.StringVar(&inputFile, "input", "testdata/test26.go", "source file to analyze")
	flag.StringVar(&inputFile, "input", _zap, "source file to analyze")

	var profilePath string
	flag.StringVar(&profilePath, "profile", "", "profiling of hot function")

	syntheticPtr := flag.Bool("synthetic", false, "set true if the synthetic main from transformer is used")

	rewriteTestFile := flag.Bool("rewriteTest", false, "set true if you want to change testing file")

	flag.Parse()

	// HACK
	//*syntheticPtr = true
	if inputFile == "" {
		fmt.Println("Please provide the input!")
		flag.PrintDefaults()
		os.Exit(1)
	}

	gizer := NewGOCC(strings.HasSuffix(inputFile, ".go"), *rewriteTestFile, *syntheticPtr, *verbose, *statsPtr, *dryrunPtr, inputFile)
	gizer.Process()

}
