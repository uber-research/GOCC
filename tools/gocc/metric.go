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
	"fmt"

	"golang.org/x/tools/go/ssa"
)

type metrics struct {
	mLock        int
	mUnlock      int
	mDeferUnlock int

	wLock        int
	wUnlock      int
	wDeferUnlock int

	rLock        int
	rUnlock      int
	rDeferUnlock int

	pairedMlock int
	pairedWlock int
	pairedRlock int

	pairedMDeferlock int
	pairedWDeferlock int
	pairedRDeferlock int

	candidateMutex  int
	candidateWMutex int
	candidateRMutex int

	intraRegionIO    int
	interRegionIO    int
	intraRegionAlias int
	interRegionAlias int
	dominaceRelation int
}

func (m *metrics) sum(a metrics) {
	m.mLock += a.mLock
	m.mUnlock += a.mUnlock
	m.mDeferUnlock += a.mDeferUnlock

	m.wLock += a.wLock
	m.wUnlock += a.wUnlock
	m.wDeferUnlock += a.wDeferUnlock

	m.rLock += a.rLock
	m.rUnlock += a.rUnlock
	m.rDeferUnlock += a.rDeferUnlock

	m.pairedMlock += a.pairedMlock
	m.pairedWlock += a.pairedWlock
	m.pairedRlock += a.pairedRlock

	m.pairedMDeferlock += a.pairedMDeferlock
	m.pairedWDeferlock += a.pairedWDeferlock
	m.pairedRDeferlock += a.pairedRDeferlock

	m.candidateMutex += a.candidateMutex
	m.candidateWMutex += a.candidateWMutex
	m.candidateRMutex += a.candidateRMutex

	m.intraRegionIO += a.intraRegionIO
	m.interRegionIO += a.interRegionIO
	m.intraRegionAlias += a.intraRegionAlias
	m.interRegionAlias += a.interRegionAlias
	m.dominaceRelation += a.dominaceRelation
}

func dumpMetrics(s map[*ssa.Function]*functionSummary) {
	var totalMetric metrics
	packageMetrics := map[string]*metrics{}

	for k, v := range s {
		if k.Pkg == nil {
			continue
		}

		mtr, ok := packageMetrics[k.Pkg.Pkg.Name()]
		if !ok {
			mtr = &metrics{}
			packageMetrics[k.Pkg.Pkg.Name()] = mtr
		}
		mtr.sum(v.metric)
		totalMetric.sum(v.metric)
	}

	var totalMutex, totalW, totalR, totralRW, nonDeferPair, deferPair, totalPair int
	fmt.Printf("\npkg,mLock,mUnlock,mDeferUnlock,sync.Mutex,wLock,wUnlock,wDeferUnlock,RWMutex+W,rLock,rUnlock,rDeferUnlock,RWMutex+R,RWMutex,candidateMutex,candidateWMutex,candidateRutex,pairedMlock,pairedWlock,pairedRlock,Paired(nondefer),pairedMDeferlock,pairedWDeferlock,pairedRDeferlock,Paired(defer),Paired(total),intraRegionIO,interRegionIO,intraRegionAlias,interRegionAlias,dominaceRelation\n")
	for k, m := range packageMetrics {
		mutex := m.mLock + m.mUnlock + m.mDeferUnlock
		totalMutex += mutex
		wMutex := m.wLock + m.wUnlock + m.wDeferUnlock
		totalW += wMutex
		rMutex := m.rLock + m.rUnlock + m.rDeferUnlock
		totalR += rMutex
		totralRW += wMutex + rMutex

		lnonDeferPair := m.pairedMlock + m.pairedWlock + m.pairedRlock
		nonDeferPair += lnonDeferPair
		lDefPair := m.pairedMDeferlock + m.pairedWDeferlock + m.pairedRDeferlock
		deferPair += lDefPair
		totalPair += lDefPair + lnonDeferPair
		fmt.Printf("%s, %d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d\n", k,
			m.mLock, m.mUnlock, m.mDeferUnlock, mutex,
			m.wLock, m.wUnlock, m.wDeferUnlock, wMutex,
			m.rLock, m.rUnlock, m.rDeferUnlock, rMutex, rMutex+wMutex,
			m.candidateMutex, m.candidateWMutex, m.candidateRMutex,
			m.pairedMlock, m.pairedWlock, m.pairedRlock, lnonDeferPair,
			m.pairedMDeferlock, m.pairedWDeferlock, m.pairedRDeferlock, lDefPair, lDefPair+lnonDeferPair,
			m.intraRegionIO, m.interRegionIO, m.intraRegionAlias, m.interRegionAlias, m.dominaceRelation)
	}

	m := totalMetric
	fmt.Printf("%s,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d\n", "total",
		m.mLock, m.mUnlock, m.mDeferUnlock, totalMutex,
		m.wLock, m.wUnlock, m.wDeferUnlock, totalW,
		m.rLock, m.rUnlock, m.rDeferUnlock, totalR, totralRW,
		m.candidateMutex, m.candidateWMutex, m.candidateRMutex,
		m.pairedMlock, m.pairedWlock, m.pairedRlock, nonDeferPair,
		m.pairedMDeferlock, m.pairedWDeferlock, m.pairedRDeferlock, deferPair, totalPair,
		m.intraRegionIO, m.interRegionIO, m.intraRegionAlias, m.interRegionAlias, m.dominaceRelation)

}
