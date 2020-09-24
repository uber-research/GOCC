package main

import (
	"sync"
)

var m sync.Mutex
var global int

func main() {
	m.Lock()
	global++
	m.Unlock()
}
