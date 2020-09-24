package main

import "sync"

func main() {
	func() {
		m := &sync.Mutex{}
		m.Lock()
		m.Unlock()
	}()
	m := &sync.Mutex{}
	m.Lock()
	m.Unlock()
}
