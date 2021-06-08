package main

import (
	"sync"
	rtm "github.com/uber-research/GOCC/tools/gocc/rtmlib"
)

func foo(m *sync.Mutex, u func()) {
	optiLock1 := rtm.OptiLock{}
	optiLock1.Lock(m)
	u()
	optiLock1.Unlock(m)
}

func main() {
	var m sync.Mutex
	i := 0
	u := func() { i++ }
	foo(&m, u)
}
