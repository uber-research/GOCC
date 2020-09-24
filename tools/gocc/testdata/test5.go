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

func wrapper(l *sync.Mutex) {
	l.Unlock()
}

// defer wrapped unlock
func main() {
	m.Lock()
	defer wrapper(&m)
	global++
}
