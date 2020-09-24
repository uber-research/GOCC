package main

import (
	"sync"
)

var m sync.Mutex
var n sync.Mutex

var global int

// Two locks taking different paths
func main(arg int) {
	if arg == 42 {
		m.Lock()
	} else {
		n.Lock()
	}

	global++

	if arg == 42 {
		global += 42
		m.Unlock()
	} else {
		global += 84
		n.Unlock()
	}
}
