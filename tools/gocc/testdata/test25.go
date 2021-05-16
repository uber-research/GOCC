//Copyright (c) 2020 Uber Technologies, Inc.
//
//Licensed under the Uber Non-Commercial License (the "License");
//you may not use this file except in compliance with the License.
//You may obtain a copy of the License at the root directory of this project.
//
//See the License for the specific language governing permissions and
//limitations under the License.
package main

import "sync"

type x struct {
	sync.Mutex
}

type y struct {
	*sync.Mutex
}

func bar() {
	var xx x
	func() {
		func() {
			xx.Lock()
			xx.Unlock()
		}()
	}()

	var yy y
	func() {
		yy.Lock()
		yy.Unlock()
	}()

	xx.Lock()
	xx.Unlock()

	mm := &xx
	nn := &yy

	mm.Lock()
	nn.Lock()
	nn.Unlock()
	mm.Unlock()
}

func baz() {
	var xx sync.Mutex
	func() {
		func() {
			xx.Lock()
			xx.Unlock()
		}()
	}()

	var yy sync.Mutex
	func() {
		yy.Lock()
		yy.Unlock()
	}()

	xx.Lock()
	xx.Unlock()

	mm := &xx
	nn := &yy

	mm.Lock()
	nn.Lock()
	nn.Unlock()
	mm.Unlock()
}

func ban() {
	xx := &sync.Mutex{}
	func() {
		func() {
			xx.Lock()
			xx.Unlock()
		}()
	}()

	yy := &sync.Mutex{}
	func() {
		yy.Lock()
		yy.Unlock()
	}()

	xx.Lock()
	xx.Unlock()

	mm := xx
	nn := yy

	mm.Lock()
	nn.Lock()
	nn.Unlock()
	mm.Unlock()
}
func foo() {
}

// test different types of Locks
func main() {
	bar()
	baz()
	ban()
}
