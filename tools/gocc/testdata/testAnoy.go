package main

import (
	"sync"
)

type Foo struct {
	sync.Mutex
	valid     bool
	otherLock sync.Mutex
}

type Bar struct {
	m sync.Mutex
}

type Fizz struct {
	*sync.Mutex
}

type Buzz struct {
	Foo
}

func main() {
	foo := Foo{}
	foo.Lock()
	foo.Unlock()

	bar := Bar{}
	bar.m.Lock()
	bar.m.Unlock()

	n := &sync.Mutex{}
	n.Lock()
	n.Unlock()

	f := Fizz{}
	f.Lock()
	f.Unlock()

	b := Buzz{}
	b.Lock()
	b.Unlock()
}

func lol() {

}
