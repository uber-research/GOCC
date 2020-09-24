//Copyright (c) 2020 Uber Technologies, Inc.
//
//Licensed under the Uber Non-Commercial License (the "License");
//you may not use this file except in compliance with the License.
//You may obtain a copy of the License at the root directory of this project.
//
//See the License for the specific language governing permissions and
//limitations under the License.
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
