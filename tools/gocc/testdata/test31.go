package main

import "sync"

func foo(m *sync.Mutex, u func()) {
	m.Lock()
	u()
	m.Unlock()
}

func main() {
	var m sync.Mutex
	i := 0
	u := func() { i++ }
	foo(&m, u)
}
