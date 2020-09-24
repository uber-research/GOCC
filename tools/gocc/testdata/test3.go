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

var m sync.Mutex
var global int

// Aliasing the same lock object
//go:noinline
func aliasTest(m *sync.Mutex, n *sync.Mutex) {
	m.Lock()
	global++
	n.Unlock()
}

// Aliasing the same lock object
func main() {
	aliasTest(&m, &m)
}
