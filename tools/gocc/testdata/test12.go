package main

import (
	"sync"
)

var global int

// no locks should be replaced
func main() {
	var m sync.Mutex
	var n sync.Mutex
	m.Lock()
	n.Lock()
	global++
	m.Unlock()
	n.Unlock()
}
