package main

import "sync"

func main() {
	var m sync.Mutex
	u := m.Unlock
	m.Lock()
	u()
}
