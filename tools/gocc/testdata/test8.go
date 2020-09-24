package main

import (
	"sync"
)

var m sync.Mutex

var global int

// Unlock post dominance frontier
func main(arg int) {
	m.Lock()

	if arg == 42 {
		global += 42
		m.Unlock()
	} else {
		global += 84
		m.Unlock()
	}
}
