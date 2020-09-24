package main

import (
	"sync"
)

var m sync.Mutex

var global int

// Both lock and unlock are in dom and pdom frontiers
func main(arg int) {
	if arg == 42 {
		m.Lock()
	} else {
		m.Lock()
	}

	global++

	if arg == 42 {
		global += 42
		m.Unlock()
	} else {
		global += 84
		m.Unlock()
	}
}
