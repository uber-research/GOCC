package main

import (
	"sync"
)

var m sync.Mutex

var global int

func callee(l *sync.Mutex) {
	l.Unlock()
}

// Callee Unlocks
func main() {
	m.Lock()
	global++
	callee(&m)
}
