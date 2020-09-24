package main

import (
	"sync"
)

func main() {
	m := &sync.Mutex{}
	n := &sync.Mutex{}

	m.Lock()
	n.Unlock()
}
