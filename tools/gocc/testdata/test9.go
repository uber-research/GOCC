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

// Both lock and unlock are in dom and pdom frontiers
func main(arg int) {
	if arg == 42 {
		m.Lock()
	} else {
		m.Lock()
	}

	global++

	if arg == 42 {
		global += 42
		m.Unlock()
	} else {
		global += 84
		m.Unlock()
	}
}
