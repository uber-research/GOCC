package main

import (
	"sync"
)

var m sync.Mutex

var global int

// Lock dominance frontier
func main(arg int) {
	if arg == 42 {
		m.Lock()
		global += 42
	} else {
		m.Lock()
		global += 84
	}
	global++
	m.Unlock()
}
