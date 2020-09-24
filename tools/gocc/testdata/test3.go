package main

import (
	"sync"
)

var m sync.Mutex
var global int

// Aliasing the same lock object
//go:noinline
func aliasTest(m *sync.Mutex, n *sync.Mutex) {
	m.Lock()
	global++
	n.Unlock()
}

// Aliasing the same lock object
func main() {
	aliasTest(&m, &m)
}
