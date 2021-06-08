package main

import "sync"

func foo(m *sync.Mutex, u func()) {
	m.Lock()
	u()
	m.Unlock()
}

func main() {
	var m sync.Mutex
	u := m.Unlock
	foo(&m, u)
}
