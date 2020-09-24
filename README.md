# Go Optimistic Concurrency Control (GOCC)

## Goal
  The goal of this project is to reduce the overhead of mutex lock in Go by replacing it with Hardware Transactional Memory (HTM).
  It will generate a patch for the given source code with HTM replacement after static analysis on the call graph.

## How to get it
`go get -u github.com/uber-research/GOCC/...`

## How to run
`cd $GOPATH/GOCC/tools/gocc`.

`go run main.go -input <go package>` then the source code will be rewritten.
  optional command line flags:
1. `-synthetic`: if you use the second step to generate the synthetic main function, you shold also pass "-synthetic" in to the command above.
2. `-dryrun`: this will not actually rewrite the file, instead, it will print out the stats like number of locks can be rewritten.
3. `-profile listOfHotFunction.txt`: this will only rewrite the hot function, if you obtain the list from the first step using profiler.
4. `-stats`: will dump out the lock info stats. You may need to create "lockcount" and "lockDistribution" folder first.

  Example: `go run main.go -input ~/gocode/src/github.com/uber-go/tally/` to rewrite [Tally](https://github.com/uber-go/tally).

### Feeding profiles
 If the profile.out is provided, you can go to profiler/ repo, run `go run profiler.go -input nameOfTheProfile` to obtain the list of hot functions.

### Synthesizing a main for libraries
 If the library does not contain a main package and function to test, we can generate a synthetic main function. In order to do that,go to synthesizer/ folder, run `go run main.go -input pathToTheLibrary` to generate the file. Once created, you can move the file (named as test.go) into the libarary that will be analyzed. Make sure not put in the root directory and CREATE a new subdirectory, otherwise Go may complain the multiple packages...
  Example: 
1. `go run synthesizer/main.go -input ~/gocode/src/github.com/uber-go/tally/`
2. `mkdir ~/gocode/src/github.com/uber-go/tally/test/`
3. `mv synthesizer/test.go ~/gocode/src/github.com/uber-go/tally/test/`
4. `go run main.go -synthetic -input ~/gocode/src/github.com/uber-go/tally/` 

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
