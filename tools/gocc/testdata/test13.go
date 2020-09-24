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
