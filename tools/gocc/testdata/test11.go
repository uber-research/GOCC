package main

import (
	"sync"
)

var global int

// m should not be replaced since it contains nested lock
func main() {
	var m sync.Mutex
	var n sync.Mutex
	m.Lock()
	n.Lock()
	global++
	n.Unlock()
	m.Unlock()
}
