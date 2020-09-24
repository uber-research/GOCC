package main

import "sync"

// test different types of Locks
func main() {
	m := &sync.Mutex{}
	n := &sync.RWMutex{}
	m.Lock()
	m.Unlock()
	n.Lock()
	n.Unlock()
	n.RLock()
	n.RUnlock()
}
