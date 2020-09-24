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
	"sync"
)

type scope struct {
	cm sync.Mutex
}

type ptrScope struct {
	nm *sync.Mutex
}

// test different call semantics
func main() {
	n := sync.Mutex{}
	n.Lock()
	n.Unlock()

	s := &scope{}
	s.cm.Lock()
	s.cm.Unlock()

	aPtr := &ptrScope{}
	aPtr.nm = &sync.Mutex{}
	aPtr.nm.Lock()
	aPtr.nm.Unlock()

	mPtr := &sync.Mutex{}
	mPtr.Lock()
	mPtr.Unlock()

	a := &sync.RWMutex{}
	a.Lock()
	a.Unlock()
	a.RLock()
	a.RUnlock()

	b := &sync.Mutex{}
	b.Lock()
	b.Unlock()
}
