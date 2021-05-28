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

func getLUPoint(call ssa.CallCommon, isADefer bool) *luPoint {
	if call.IsInvoke() {
		return nil
	}
	function := call.StaticCallee()
	receiver := call.Value

	if receiver == nil || function == nil {
		return nil
	}

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

func createLUPoint(ins ssa.Instruction) *luPoint {
	if call, ok := ins.(*ssa.Call); ok {
		return getLUPoint(call.Call, false)
	} else if call, ok := ins.(*ssa.Defer); ok {
		return getLUPoint(call.Call, true)
	}
	return nil
}

func gatherLUPoints(f *ssa.Function) (map[*luPoint]ssa.Instruction, map[ssa.Instruction]*luPoint) {
	if f == nil {
		return nil, nil
	}
	pts := map[*luPoint]ssa.Instruction{}
	insMap := map[ssa.Instruction]*luPoint{}

	for _, blk := range f.Blocks {
		for idx, ins := range blk.Instrs {
			if pt := createLUPoint(ins); pt != nil {
				pt.insIdx = idx
				pts[pt] = ins
				pt.ssaIns = ins
				insMap[ins] = pt
			}
		}
	}
	return pts, insMap
}
