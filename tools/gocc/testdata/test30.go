package main

import "sync"

var u func()

func foo(m *sync.Mutex) {
	m.Lock()
	u()
	m.Unlock()
}

func main() {
	var m sync.Mutex
	u = m.Unlock
	foo(&m)
}
