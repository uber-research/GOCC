# Go Optimistic Concurrency Control (GOCC)

## Goal
  The goal of this project is to reduce the overhead of mutex lock in Go by replacing it with Hardware Transactional Memory (HTM).
  It will generate a patch for the given source code with HTM replacement after static analysis on the call graph.

## How to get it
`git clone https://github.com/uber-research/GOCC.git`

## How to run
From the repo root:

1. `cd tools/gocc`

`go run . -input <go package>` then the source code will be rewritten.
  optional command line flags:
1. `-synthetic`: if you use the second step to generate the synthetic main function, you shold also pass "-synthetic" in to the command above.
2. `-dryrun`: this will not actually rewrite the file, instead, it will print out the stats like number of locks can be rewritten.
3. `-profile listOfHotFunction.txt`: this will only rewrite the hot function, if you obtain the list from the first step using profiler.
4. `-stats`: will dump out the lock info stats. You may need to create "lockcount" and "lockDistribution" folder first.

  Example: `go run . -input ~/gocode/src/github.com/uber-go/tally/` to rewrite [Tally](https://github.com/uber-go/tally).

### Feeding profiles
 If the profile.out is provided, you can go to profiler/ repo, run `go run profiler.go -input nameOfTheProfile` to obtain the list of hot functions.

### Synthesizing a main for libraries
 If the library does not contain a main package and function to test, we can generate a synthetic main function. In order to do that,go to synthesizer/ folder, run `go run . -input pathToTheLibrary` to generate the file. Once created, you can move the file (named as test.go) into the libarary that will be analyzed. Make sure not put in the root directory and CREATE a new subdirectory, otherwise Go may complain the multiple packages...
  Example: 
1. `go run synthesizer/main.go -input ~/gocode/src/github.com/uber-go/tally/`
2. `mkdir ~/gocode/src/github.com/uber-go/tally/test/`
3. `mv synthesizer/test.go ~/gocode/src/github.com/uber-go/tally/test/`
4. `go run . -synthetic -input ~/gocode/src/github.com/uber-go/tally/` 

## Go Version Compatibility
GOCC depends on `golang.org/x/tools/go/pointer`, which is unstable on newer Go toolchains.

- On Go `1.26+`, pointer analysis may panic or fail for some inputs.
- GOCC now guards pointer analysis and falls back to CHA call graph construction instead of crashing.
- If points-to analysis is unavailable, GOCC currently skips rewriting for safety.
- `TestPkgs` golden-parity test is skipped on Go `1.26+` because upstream pointer behavior is not stable there.

If you need strict golden parity with historical outputs, use a Go version older than `1.26` (and still compatible with this module's `go 1.24.0` baseline) where `go/pointer` behavior is stable for your workload.

For development workflows, run `go mod tidy` from the repo root after dependency edits.

## Internal workings
  1. Obtain the call graph of the given code.
  2. Identify the valid pairs of lock and unlock for safe replacement from the call graph.
  	2.1 Find lock pairs by iterating thru the ssa form that satisfies the CFG requirements.
  	2.2 For each pair, analyzing whether its corresponding critical section is safe or not, i.e. there should be no instructions that violate HTM like I/O, sync, and runtime. 
  3. Rewrite the identified lock pair candidates with HTM in AST.
  4. Generate patch from modified AST.

Different CFG pattern with their corresponding critical section (CS) that we currently handle:
  1. Lock and unlock in the same basic block (BB), CS is the instructions between them.
  2. Lock and defer unlock in the same BB, CS is the instructions in between plus the ALL reachable BB.
  3. Lock dominates unlock and unlock post-dominates lock, CS is all BBs between the lock BB and unlock BB.
  4. Lock dominates defer unlock and defer unlock post-dominates lock, CS is all BBs between the lock BB and unlock BB, plus ALL reachable BB from defer unlock BB.
