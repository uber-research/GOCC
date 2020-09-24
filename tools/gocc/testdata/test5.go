package main

import (
	"sync"
)

var m sync.Mutex

var global int

func wrapper(l *sync.Mutex) {
	l.Unlock()
}

// defer wrapped unlock
func main() {
	m.Lock()
	defer wrapper(&m)
	global++
}
