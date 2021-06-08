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
	"golang.org/x/tools/go/pointer"
	"golang.org/x/tools/go/ssa"
)

func (g *gocc) getMainPkgs() []*ssa.Package {
	mainPkg := []*ssa.Package{}
	if g.isSingleFile || !g.synthetic {
		for _, pkg := range g.ssapkgs {
			if pkg != nil && pkg.Pkg.Name() == "main" {
				mainPkg = append(mainPkg, pkg)
			}
		}
	} else {
		for _, pkg := range g.ssapkgs {
			if pkg != nil && pkg.Pkg.Name() == "main" {
				if pkg.Func("OptiLockSyntheticMain") != nil {
					mainPkg = append(mainPkg, pkg)
				}
			}
		}
	}
	return mainPkg
}

// collectPointsToSet updates the PointsToSet in each of lPoints, uPoints, and dPoints
func (g *gocc) collectPointsToSet() {
	mainPkg := g.getMainPkgs()
	if len(mainPkg) == 0 {
		panic("No main package found!")
	}
	pc := pointer.Config{
		Reflection:      false,
		BuildCallGraph:  false,
		Mains:           mainPkg,
		Queries:         make(map[ssa.Value]struct{}),
		IndirectQueries: make(map[ssa.Value]struct{}),
	}

	// First collect all indirection function call values and identify all the potential "closures" they may be calling.
	// collect all values
	indirectCallToLUPointMap := map[ssa.Value][]*luPoint{}

	for k, _ := range g.allLUPoints {
		if !k.isDynamic {
			continue
		}
		// indirect calls
		val := k.callC.Value
		pc.Queries[val] = struct{}{}
		//	pc.IndirectQueries[val] = struct{}{}
		indirectCallToLUPointMap[val] = append(indirectCallToLUPointMap[val], k)
	}

	// find the functions that these indirection calls invoke.
	calleeResult, err := pointer.Analyze(&pc)
	if err != nil {
		panic("pointer.Analyze( failed")
	}

	// Get the free var in these indirectly invoked functions
	for k, v := range calleeResult.Queries {
		pts, ok := indirectCallToLUPointMap[k]
		if !ok {
			panic("Why is it not present in the map?")
		}
		if len(pts) == 0 {
			panic("Why len(pts) == 0  ?")
		}

		for _, lbl := range v.PointsTo().Labels() {
			if lbl == nil || lbl.Value() == nil {
				continue
			}

			// is the label a function?
			fn, ok := lbl.Value().(*ssa.Function)
			if !ok {
				continue
			}
			// the function should have zero parameters
			if len(fn.Params) != 0 {
				continue
			}
			// the function should have one freevar
			if len(fn.FreeVars) != 1 { // only the sync.Mutex or sync.RWMutex can be captured
				continue
			}

			// the type of the freevar must be "*sync.Mutex" or "*sync.RWMutex"
			switch fn.FreeVars[0].Type().String() {
			case "*sync.Mutex", "*sync.RWMutex":
				for _, pt := range pts {
					pt.indirectMutexVals = append(pt.indirectMutexVals, fn.FreeVars[0])
				}
			}
		}
	}

	// sanity check
	for k, _ := range g.allLUPoints {
		if k.isDynamic {
			// if len(k.indirectMutexVals) == 0 {
			// 	panic("no indect mutex value extracted!")
			// }
			if k.mutexVal != nil {
				panic(" mutexVal value should be nil here!")
			}
		} else if len(k.indirectMutexVals) != 0 {
			panic(" indect mutex value is unexpected here!")
		}
	}

	// Second: collects pointsto set of all LUPoints
	// reset the query
	pc.Queries = make(map[ssa.Value]struct{})
	pc.IndirectQueries = make(map[ssa.Value]struct{})
	valToLUPointMap := map[ssa.Value][]*luPoint{}

	// collect all values
	for k, _ := range g.allLUPoints {
		if !k.isDynamic {
			if val := k.mutexValue(); val != nil {
				pc.Queries[val] = struct{}{}
				valToLUPointMap[val] = append(valToLUPointMap[val], k)
			}
		} else {
			for _, ind := range k.indirectMutexVals {
				pc.Queries[ind] = struct{}{}
				valToLUPointMap[ind] = append(valToLUPointMap[ind], k)
			}
		}
	}

	result, err := pointer.Analyze(&pc)
	if err != nil {
		panic("pointer.Analyze( failed")
	}

	// Now, set the PointToSet of each luPoint
	for k, v := range result.Queries {
		pts, ok := valToLUPointMap[k]
		if !ok {
			panic("Why is it not present in the map?")
		}
		if len(pts) == 0 {
			panic("Why len(pts) == 0  ?")
		}

		for _, pt := range pts {
			pt.appendToAliasingPointer(v)
		}
	}
}
