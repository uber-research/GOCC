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

func HoH() {
	var m sync.Mutex
	var n sync.Mutex
	m.Lock()
	n.Lock()
	m.Unlock()
	n.Unlock()
}

// test different types of Locks
func main() {
	HoH()
}
