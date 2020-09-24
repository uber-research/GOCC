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

type Set interface {
	Equal(other Set) bool
}

type threadUnsafeSet map[interface{}]struct{}

type threadSafeSet struct {
	s threadUnsafeSet
	sync.RWMutex
}

func newThreadUnsafeSet() threadUnsafeSet {
	return make(threadUnsafeSet)
}

func newThreadSafeSet() threadSafeSet {
	return threadSafeSet{s: newThreadUnsafeSet()}
}

func NewSet(s ...interface{}) Set {
	set := newThreadSafeSet()
	return &set
}

func (set *threadSafeSet) Equal(other Set) bool {
	o := other.(*threadSafeSet)

	set.RLock()
	o.RLock()
	set.RUnlock()
	o.RUnlock()
	return true
}

func (set *threadSafeSet) b(other Set) {
	o := other.(*threadSafeSet)

	set.RLock()
	o.RLock()
	set.RUnlock()
	o.RUnlock()
}

func create() Set {
	return (NewSet())
}

func main() {
	var twoSets [2]Set
	for i := 0; i < 2; i++ {
		twoSets[i] = create()
	}
	//	set := &threadSafeSet{}
	//	other := NewSet()
	twoSets[0].Equal(twoSets[1])
}
