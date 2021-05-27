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

// collectPointsToSet updates the PointsToSet in each of lPoints, uPoints, and dPoints
func (g *gocc) collectPointsToSet(ssapkgs []*ssa.Package, pts map[*luPoint]ssa.Instruction) {
	mainPkg := make([]*ssa.Package, 1)
	if g.isSingleFile || !g.synthetic {
		for _, pkg := range ssapkgs {
			if pkg != nil && pkg.Pkg.Name() == "main" {
				mainPkg[0] = pkg
				break
			}
		}
	} else {
		for _, pkg := range ssapkgs {
			if pkg != nil && pkg.Pkg.Name() == "main" {
				if pkg.Func("OptiLockSyntheticMain") != nil {
					mainPkg[0] = pkg
					break
				}
			}
		}
	}
	if mainPkg[0] == nil {
		panic("No main package found!")
	}
	pc := pointer.Config{
		Reflection:      false,
		BuildCallGraph:  false,
		Mains:           mainPkg,
		Queries:         make(map[ssa.Value]struct{}),
		IndirectQueries: make(map[ssa.Value]struct{}),
	}

	valToLUPointMap := map[ssa.Value][]*luPoint{}

	// collect all values
	for k, _ := range pts {
		val := k.mutexValue()
		pc.Queries[val] = struct{}{}
		//	pc.IndirectQueries[val] = struct{}{}
		valToLUPointMap[val] = append(valToLUPointMap[val], k)
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
			pt.setAliasingPointer(v)
		}
	}
	/*
		for k, v := range result.IndirectQueries {
			pts, ok := valToLUPointMap[k]
			if !ok {
				panic("Why is it not present in the map?")
			}
			if len(pts) == 0 {
				panic("Why len(pts) == 0  ?")
			}

			for _, pt := range pts {
				pt.setIndirectPointsToSet(v.PointsTo())
			}
		}*/
}
