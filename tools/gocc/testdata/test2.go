package main

import (
	"sync"
)

var m sync.Mutex
var n sync.Mutex

var global int

// Lock m but unlock n
func main() {
	m.Lock()
	global++
	n.Unlock()
}
