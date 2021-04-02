//Copyright (c) 2020 Uber Technologies, Inc.
//
//Licensed under the Uber Non-Commercial License (the "License");
//you may not use this file except in compliance with the License.
//You may obtain a copy of the License at the root directory of this project.
//
//See the License for the specific language governing permissions and
//limitations under the License.
package callgraph

import (
	"fmt"
	"log"

	"golang.org/x/tools/go/callgraph"
	"golang.org/x/tools/go/callgraph/cha"
	"golang.org/x/tools/go/callgraph/rta"
	"golang.org/x/tools/go/pointer"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
)

func BuildChaCG(prog *ssa.Program) *callgraph.Graph {
	log.Println("Building callgraph using CHA...")
	graph := cha.CallGraph(prog)
	log.Println("          done")
	log.Println("  # nodes in callgraph before deleting synthetic nodes = ", len(graph.Nodes))
	graph.DeleteSyntheticNodes()
	log.Println("  # nodes in callgraph = ", len(graph.Nodes))
	return graph
}

func BuildRtaCG(prog *ssa.Program, useAllRoots bool) *callgraph.Graph {
	log.Println("Building callgraph using RTA...")
	var cgroots []*ssa.Function
	if useAllRoots {
		for f, _ := range ssautil.AllFunctions(prog) {
			cgroots = append(cgroots, f)
		}
	} else {
		// mainPkgs := ssautil.MainPackages(prog.AllPackages())
		mainPkgs := make([]*ssa.Package, 1)
		for _, pkg := range prog.AllPackages() {
			if pkg != nil && pkg.Pkg.Name() == "main" {
				mainPkgs[0] = pkg
				break
			}
		}
		if len(mainPkgs) == 0 {
			panic("No main packages found.")
		}
		for _, mainPkg := range mainPkgs {
			cgroots = append(cgroots, mainPkg.Func("init"), mainPkg.Func("main"))
		}
	}
	log.Println("  num cgroots = ", len(cgroots))
	graph := rta.Analyze(cgroots, true).CallGraph
	log.Println("  # nodes in callgraph before deleting synthetic nodes = ", len(graph.Nodes))
	graph.DeleteSyntheticNodes()
	log.Println("  # nodes in callgraph = ", len(graph.Nodes))
	return graph
}

func BuildPtaCG(prog *ssa.Program) *callgraph.Graph {
	log.Println("Building callgraph using Points-To analysis...")
	var ptrConfig pointer.Config
	mainPkgs := ssautil.MainPackages(prog.AllPackages())
	log.Println("  # of main packages = ", len(mainPkgs))

	ptrConfig.Mains = mainPkgs
	ptrConfig.BuildCallGraph = true
	ptrConfig.Reflection = false

	log.Println("  Pointer analysis running...")
	ptares, err := pointer.Analyze(&ptrConfig)
	if err != nil {
		log.Println("  Pointer analysis failed: %s", err)
	}
	log.Println("          done")
	graph := ptares.CallGraph
	log.Println("  # nodes in callgraph before deleting synthetic nodes = ", len(graph.Nodes))
	graph.DeleteSyntheticNodes()
	log.Println("  # nodes in callgraph = ", len(graph.Nodes))
	return graph
}

func IsNodeInGraph(g *callgraph.Graph, n *callgraph.Node) bool {
	_, ok := g.Nodes[n.Func]
	return ok
}

func IsEdgeInGraph(g *callgraph.Graph, e *callgraph.Edge) bool {
	callerInG := g.Nodes[e.Caller.Func]
	calleeInG := g.Nodes[e.Callee.Func]
	if callerInG == nil || calleeInG == nil {
		return false
	}
	for _, out := range callerInG.Out {
		if out.Callee.ID == calleeInG.ID {
			if out.Site.Pos() == e.Site.Pos() {
				return true
			}
		}
	}
	return false
}

func IsRootNode(n *callgraph.Node) bool {
	return n.Func.String() == "<root>"
}

func HasNoEdges(n *callgraph.Node) bool {
	return len(n.In)+len(n.Out) == 0
}

func PrintCGStats(g *callgraph.Graph) {
	fmt.Println("  # of nodes = ", len(g.Nodes))
	edges := 0
	for _, n := range g.Nodes {
		edges += len(n.Out)
	}
	fmt.Println("  # of edges = ", edges)
}
