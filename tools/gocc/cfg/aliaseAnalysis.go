package cfg

import (
	"golang.org/x/tools/go/pointer"
	"golang.org/x/tools/go/ssa"
)

// MatchLockWithUnlock returns a map of locks that always refer to same object
func MatchLockWithUnlock(ssapkgs []*ssa.Package, lockVals map[ssa.Value]bool, unlockVals map[ssa.Value]bool, isSingleFile, isSynthetic bool) map[ssa.Value][]ssa.Value {
	mainPkg := make([]*ssa.Package, 1)
	if isSingleFile || !isSynthetic {
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
	// fmt.Printf("this is empty? %v\n", ssapkgs == nil)
	// fmt.Println("Dump out the packages!")
	// for _, pkg := range ssapkgs {
	// 	if pkg == nil {
	// 		fmt.Println("It's empty!")
	// 	} else {
	// 		fmt.Println(pkg.String())
	// 	}
	// }
	// fmt.Println("Dump out finished!")
	for k := range lockVals {
		// if !pointer.CanPoint(k) {
		// 	fmt.Errorf("Ignoring %v from PTA analysis because it CanPoint", k)
		// }
		pc.Queries[k] = struct{}{}
	}
	for k := range unlockVals {
		// if !pointer.CanPoint(k) {
		// 	fmt.Errorf("Ignoring %v from PTA analysis because it CanPoint", k)
		// }
		pc.Queries[k] = struct{}{}
	}
	result, err := pointer.Analyze(&pc)
	if err != nil {
		panic("pointer.Analyze( failed")
	}

	ptrToLockMap := make(map[ssa.Value][]ssa.Value)
	for lk := range lockVals {
		for ulk := range unlockVals {
			a := result.Queries[lk].PointsTo().Labels()
			b := result.Queries[ulk].PointsTo().Labels()
			if unorderedEqual(a, b) {
				if _, ok := ptrToLockMap[lk]; !ok {
					ptrToLockMap[lk] = make([]ssa.Value, 0, 1)
				}
				if contains(ptrToLockMap[lk], ulk) == false {
					ptrToLockMap[lk] = append(ptrToLockMap[lk], ulk)
				}
			}
		}
	}

	return ptrToLockMap
}

func unorderedEqual(first, second []*pointer.Label) bool {
	if len(first) != len(second) {
		return false
	}
	exists := make(map[ssa.Value]bool)
	for _, value := range first {
		exists[value.Value()] = true
	}
	for _, value := range second {
		if !exists[value.Value()] {
			return false
		}
	}
	return true
}

func contains(array []ssa.Value, item ssa.Value) bool {
	for _, val := range array {
		if val == item {
			return true
		}
	}
	return false
}
