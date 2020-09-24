package main

import "sync"

func main() {
	m := &sync.Mutex{}
	defer m.Unlock()
	m.Lock()
}
