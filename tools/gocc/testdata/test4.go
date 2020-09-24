package main

import (
	"sync"
)

var global int

// defer unlock
func main() {
	var m sync.Mutex
	m.Lock()
	defer m.Unlock()
	global++
}
