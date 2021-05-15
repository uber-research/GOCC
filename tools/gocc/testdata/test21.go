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

func bar() {
	var n sync.Mutex
	n.Lock()
	foo()
	n.Unlock()
}
func foo() {
	var m sync.Mutex
	m.Lock()
	bar()
	m.Unlock()
}

// test different types of Locks
func main() {
	foo()
}
