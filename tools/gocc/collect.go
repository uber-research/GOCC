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
	"golang.org/x/tools/go/ssa"
)

func (g *gocc) maybeLUoint(f *ssa.Function) luPointType {

	switch f.String() {
	case "(*sync.Mutex).Lock":
		return LOCK
	case "(*sync.Mutex).Unlock":
		return UNLOCK
	case "(*sync.RWMutex).Lock":
		return WLOCK
	case "(*sync.RWMutex).RLock":
		return RLOCK
	case "(*sync.RWMutex).Unlock":
		return WUNLOCK
	case "(*sync.RWMutex).RUnlock":
		return RUNLOCK
	}
	return UNKNOWN
}

// getLUPoint retuns an luPoint if the call is acting on a Mutex/RWMutex type.
func (g *gocc) getLUPoint(call ssa.CallCommon, ins ssa.Instruction, f *ssa.Function, isADefer bool) *luPoint {
	if call.IsInvoke() {
		return nil
	}
	function := call.StaticCallee()
	receiver := call.Value

	if receiver == nil {
		panic("call.Value nil!")
		return nil
	}

	if function != nil {
		name := receiver.String()

		switch name {
		case "(*sync.Mutex).Lock":
			return &luPoint{kind: LOCK, callC: call, mutexVal: call.Args[0]}
		case "(*sync.Mutex).Unlock":
			if isADefer {
				return &luPoint{kind: DEFER_UNLOCK, callC: call, mutexVal: call.Args[0]}
			}
			return &luPoint{kind: UNLOCK, callC: call, mutexVal: call.Args[0]}
		case "(*sync.RWMutex).Lock":
			return &luPoint{kind: WLOCK, callC: call, mutexVal: call.Args[0]}
		case "(*sync.RWMutex).RLock":
			return &luPoint{kind: RLOCK, callC: call, mutexVal: call.Args[0]}
		case "(*sync.RWMutex).Unlock":
			if isADefer {
				return &luPoint{kind: DEFER_WUNLOCK, callC: call, mutexVal: call.Args[0]}
			}
			return &luPoint{kind: WUNLOCK, callC: call, mutexVal: call.Args[0]}
		case "(*sync.RWMutex).RUnlock":
			if isADefer {
				return &luPoint{kind: DEFER_RUNLOCK, callC: call, mutexVal: call.Args[0]}
			}
			return &luPoint{kind: RUNLOCK, callC: call, mutexVal: call.Args[0]}
		}
		return nil
	}
	// it may be an indirect call: e.g.,
	/*
	 var m sync.Mutex
	 u := m.Unlock
	 m.Lock()
	 u()
	*/

	node, ok := g.cg.Nodes[f]
	if !ok {
		return nil
	}

	calleeKind := map[luPointType]int{}
	for _, callee := range node.Out {
		if callee.Site != ins {
			continue
		}
		if kind := g.maybeLUoint(callee.Callee.Func); kind != UNKNOWN {
			if _, ok := calleeKind[kind]; ok {
				calleeKind[kind]++
			} else {
				calleeKind[kind] = 1
			}
		}
	}

	if len(calleeKind) == 0 {
		return nil
	}

	// TODO: sometime infinitely in the future, we may be able to transform these indirect calls also.
	// But currently, we cannot because `u()` cannot be rewritten to `optilock.Unlock(which object?)`
	return &luPoint{kind: MULTIPLE, callC: call, mutexVal: nil /*DONT KNOW, but will fill the pointsto set*/, isDynamic: true}
}

func (g *gocc) createLUPoint(ins ssa.Instruction, f *ssa.Function) *luPoint {
	if call, ok := ins.(*ssa.Call); ok {
		return g.getLUPoint(call.Call, ins, f, false)
	} else if call, ok := ins.(*ssa.Defer); ok {
		return g.getLUPoint(call.Call, ins, f, true)
	}
	return nil
}

// gatherLUPoints fetches all LUPoints in a function.
func (g *gocc) gatherLUPoints(f *ssa.Function) (map[*luPoint]ssa.Instruction, map[ssa.Instruction]*luPoint) {
	if f == nil {
		return nil, nil
	}
	pts := map[*luPoint]ssa.Instruction{}
	insMap := map[ssa.Instruction]*luPoint{}

	for _, blk := range f.Blocks {
		for idx, ins := range blk.Instrs {
			if pt := g.createLUPoint(ins, f); pt != nil {
				pt.insIdx = idx
				pts[pt] = ins
				pt.ssaIns = ins
				insMap[ins] = pt
			}
		}
	}
	return pts, insMap
}
