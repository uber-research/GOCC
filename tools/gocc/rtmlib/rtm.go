//Copyright (c) 2020 Uber Technologies, Inc.
//
//Licensed under the Uber Non-Commercial License (the "License");
//you may not use this file except in compliance with the License.
//You may obtain a copy of the License at the root directory of this project.
//
//See the License for the specific language governing permissions and
//limitations under the License.
package rtmlib

import (
	"sync"
	"sync/atomic"
	"unsafe"

	rdtsc "github.com/uber-research/GOCC/tools/gocc/gordtsc"
	"github.com/intel-go/cpuid"
)

// indicate the CPU support RTM or not
var hasRTM bool = false

// auxilary data structure for lock spinning
var dummySpinLoad [64]int32
var spinLimit int = 100

var weightLowerBound int32 = -16
var weightUpperBound int32 = 15
var confidenceThreshold int32 = 1

const perceptronEntry = (1 << 12) // Must be a power of 2
const perceptronEntryMask = (perceptronEntry - 1)
const weightThreshold = 2
const slowpathRepeatThreshold = 20 // if we are using the slow path for a long time, retry HTM

const cacheLinePadSize = 128

const notSameLockError = 0x10

// OptiLock has a boolean variable to indicate if the lock uses mutex or HTM
// TODO: change OptiLock into interface so that stats counter is optional
type OptiLock struct {
	isSlowPath        bool
	Commit            int
	HTMtake           int
	Abort             int
	SlowLock          int
	SlowUnlock        int
	tsc               uint64
	rtmFails          bool
	underlyingMutex   *sync.Mutex
	underlyingRWMutex *sync.RWMutex
}

type cell struct {
	_              [cacheLinePadSize]byte // Prevents false sharing.
	weightIP       int32
	weightLock     int32
	trial          uint8 // Max 256 trials. It's the trial number allowed
	remainingTrial uint8
	duration       uint32
	success        int32
	failure        int32
	sleep          int32
	_              [cacheLinePadSize]byte // Prevents false sharing.
}

const (
	_MUTEXT_WEIGHT int = iota
	_RLOCK_WEIGHT
	_WLOCK_WEIGHT
	_MAX_WEIGHTS
)

var weight [_MAX_WEIGHTS][perceptronEntry]cell

var trailNum = []uint8{5, 10, 1}

func init() {
	hasRTM = cpuid.HasExtendedFeature(cpuid.RTM)
	for j := 0; j < _MAX_WEIGHTS; j++ {
		for i := 0; i < perceptronEntry; i++ {
			weight[j][i].weightIP = 3
			weight[j][i].weightLock = 3
			weight[j][i].trial = trailNum[j]
			weight[j][i].remainingTrial = trailNum[j]
			weight[j][i].duration = 10000
			weight[j][i].success = 0
			weight[j][i].failure = 0
			weight[j][i].sleep = 0
		}
	}
}

// go:nosplit
func reInit(c *cell) {
	c.weightIP = 2
	c.weightLock = 2
	c.trial = 3
	c.remainingTrial = 3
	c.duration = 5000
	c.success = 0
	c.failure = 0
	c.sleep = 0
}

// the memory layout of Go struct (at least on some Intel machine) is using little endian
// the declared order is
// type Mutex struct {
// 	state int32
// 	sema  uint32
// }
// therefore, state is using the first 4 bytes and sema is using next 4.

func getStateMutex(lock *sync.Mutex) *int32 {
	return (*int32)(unsafe.Pointer(lock))
}

func getStateRWMutex(lock *sync.RWMutex) *int32 {
	return (*int32)(unsafe.Pointer(lock))
}

func getSemaMutex(lock *sync.Mutex) *uint32 {
	return (*uint32)(unsafe.Pointer(uintptr(unsafe.Pointer(lock)) + unsafe.Sizeof(int32(0))))
}

func getSemaRWMutex(lock *sync.RWMutex) *uint32 {
	return (*uint32)(unsafe.Pointer(uintptr(unsafe.Pointer(lock)) + unsafe.Sizeof(int32(0))))
}

func getMutexInfo(lock *sync.Mutex) (uint32, uint32) {
	return *(*uint32)(unsafe.Pointer(lock)), *(*uint32)(unsafe.Pointer(uintptr(unsafe.Pointer(lock)) + unsafe.Sizeof(int32(0))))
}

func getRWMutexInfo(lock *sync.RWMutex) (uint32, uint32) {
	return *(*uint32)(unsafe.Pointer(lock)), *(*uint32)(unsafe.Pointer(uintptr(unsafe.Pointer(lock)) + unsafe.Sizeof(int32(0))))
}

func getPerceptronIndex(funcAddr, lockAddr uintptr) uint64 {
	return ((uint64)((funcAddr^lockAddr)>>3) & (perceptronEntryMask))
}

func bounded(weight int32, inc int8) int32 {
	retVal := weight + int32(inc)
	if retVal >= weightUpperBound {
		return weightUpperBound
	}

	if retVal <= weightLowerBound {
		return weightLowerBound
	}

	return retVal
}

func checkConfidence(success, failure int32) bool {
	return success*confidenceThreshold > failure
}

func goOnSlowPath(c *cell) bool {
	return c.weightIP+c.weightLock < weightThreshold
}

func isUnpairedUnlock(status uint32) bool {
	return GetImm(status) == notSameLockError
}

// Lock tries to use RTM and mark the boolean variable
// if failed, it will roll back to mutex
//go:nosplit
func (ml *OptiLock) Lock(lock *sync.Mutex) {
	// don't do anything without RTM support
	if hasRTM == false {
		lock.Lock()
		return
	}

	ml.isSlowPath = false
	ml.rtmFails = false
	ml.underlyingMutex = lock
	funcAddr := (uintptr)(unsafe.Pointer(ml))
	lockAddr := (uintptr)(unsafe.Pointer(lock))
	idx := getPerceptronIndex(funcAddr, lockAddr)
	entry := &weight[_MUTEXT_WEIGHT][idx]
	entry.remainingTrial = entry.trial
	// fast path

	// there are two ways to control the decay, one is giving a large value and decrement by a little bit
	// for instance trial = 1000, then trial += 10. but when we use the trial we will do use (trial/100) as the real counter
	// The other is using the threshold here
	if entry.sleep > slowpathRepeatThreshold {
		reInit(entry)
	}
	ml.tsc = rdtsc.Counter()
	// perceptron says to use HTM
	if goOnSlowPath(entry) {
		// perceptron says not to use HTM
		goto _SLOWPATH
	}

	for {
		//TODO: can use a switch on the status, if that does not hurt performance due to branch prediction.
		status := TxBegin()
		if status == TxBeginStarted {
			// check if lock is taken, if yes abort the HTM
			if *getStateMutex(lock) != 0 {
				TxAbort()
			}
			return
		}

		// Transaction failed for some reason.
		// Lets investigate.
		// abort because lock and unlock do not pair
		if isUnpairedUnlock(status) || entry.remainingTrial == 0 {
			ml.rtmFails = true
			goto _SLOWPATH
		}

		// Decrement number of trials
		entry.remainingTrial--
		if status&(TxAbortRetry|TxAbortConflict|TxAbortExplicit) != 0 {

			// Spin a bit and then retry
			loopCnt := int(entry.duration)
			for i := 0; i < loopCnt; i++ {
				atomic.LoadInt32(&dummySpinLoad[32])
			}
		}
	}

_SLOWPATH:
	entry.sleep++ // Racey update is OK.
	ml.isSlowPath = true
	ml.SlowLock++ // TODO: remove?

	loopCnt := int(entry.duration)
	for i := 0; i < loopCnt; i++ {
		atomic.LoadInt32(&dummySpinLoad[32])
	}

	lock.Lock()
	return
}

// Unlock will check if the lock is mutex or
// then commit or unlock accordingly
//go:nosplit
func (ml *OptiLock) Unlock(lock *sync.Mutex) {
	// don't do anything without RTM support
	if hasRTM == false {
		lock.Unlock()
		return
	}
	// if the not unlock on the same lock, use custormized abort
	if lock != ml.underlyingMutex {
		TxAbortOnDifferentLock()
	}
	funcAddr := (uintptr)(unsafe.Pointer(ml))
	lockAddr := (uintptr)(unsafe.Pointer(lock))
	idx := getPerceptronIndex(funcAddr, lockAddr)
	entry := &weight[_MUTEXT_WEIGHT][idx]

	if ml.isSlowPath == true {
		// it is a mutex
		lock.Unlock()
		ml.SlowUnlock++ // TODO: remove later

		timeElapse := rdtsc.Counter() - ml.tsc
		// update the weight since they are not working

		// we enter here since we used up all the trials
		if ml.rtmFails == true {
			entry.weightIP = bounded(entry.weightIP, -1)
			entry.weightLock = bounded(entry.weightLock, -1)
			entry.failure++
			if entry.trial > 0 {
				// allows more trial only if the spin time is much shorter than time elapsed.
				if timeElapse > uint64(entry.duration*uint32(entry.trial)*2) {
					entry.duration = uint32(timeElapse)
				} else {
					entry.trial--
				}
			}
		}

	} else {
		// it is a rtm
		TxEnd()

		timeElapse := rdtsc.Counter() - ml.tsc

		// HTM works, update the weight
		entry.weightIP = bounded(entry.weightIP, int8(entry.trial))
		entry.weightLock = bounded(entry.weightLock, int8(entry.trial))

		if entry.remainingTrial <= 1 {
			entry.duration = uint32(timeElapse)
			entry.trial--
		} else if entry.trial-entry.remainingTrial <= 1 && entry.duration > 2*uint32(timeElapse) {
			entry.duration = uint32(timeElapse)
		}

		entry.success++
	}
}

// RLock tries to use RTM and mark the boolean variable
// if failed, it will roll back to RWMutex
//go:nosplit
func (ml *OptiLock) RLock(lock *sync.RWMutex) {
	// don't do anything without RTM support
	if hasRTM == false {
		lock.RLock()
		return
	}
	ml.isSlowPath = false
	ml.rtmFails = false
	ml.underlyingRWMutex = lock
	funcAddr := (uintptr)(unsafe.Pointer(ml))
	lockAddr := (uintptr)(unsafe.Pointer(lock))
	idx := getPerceptronIndex(funcAddr, lockAddr)
	entry := &weight[_RLOCK_WEIGHT][idx]
	entry.remainingTrial = entry.trial
	// fast path
	// localTrial := 1 + entry.trial // 1+ to make sure we are never zero

	if entry.sleep > slowpathRepeatThreshold {
		reInit(entry)
		// TODO: give more trial to reader lock only
		entry.trial = 5
		entry.remainingTrial = 5
	}

	// perceptron says to use HTM
	if goOnSlowPath(entry) {
		// perceptron says not to use HTM
		goto _SLOWPATH
	}

	for {
		//TODO: can use a switch on the status, if that does not hurt performance due to branch prediction.
		ml.tsc = rdtsc.Counter()
		status := TxBegin()

		if status == TxBeginStarted {
			// check if lock is taken, if yes abort the HTM
			if *getStateRWMutex(lock) != 0 {
				TxAbort()
			}
			return
		}

		// Transaction failed for some reason.
		// Lets investigate.
		// abort because lock and unlock do not pair
		if isUnpairedUnlock(status) || entry.remainingTrial == 0 {
			ml.rtmFails = true
			goto _SLOWPATH
		}

		entry.remainingTrial--
		if status&(TxAbortRetry|TxAbortConflict|TxAbortExplicit) != 0 {
			// We'll retry
			// try a little bit more if confidence score is high
			// Spin a bit and then retry
			loopCnt := int(entry.duration)
			for i := 0; i < loopCnt; i++ {
				atomic.LoadInt32(&dummySpinLoad[32])
			}
		}
	}

_SLOWPATH:
	entry.sleep++ // Racey update is OK.
	ml.isSlowPath = true
	ml.SlowLock++ // TODO: remove?
	loopCnt := int(entry.duration)
	for i := 0; i < loopCnt; i++ {
		atomic.LoadInt32(&dummySpinLoad[32])
	}
	lock.RLock()
	return
}

// RUnlock will check if the lock is RWMutex or
// then commit or unlock accordingly
//go:nosplit
func (ml *OptiLock) RUnlock(lock *sync.RWMutex) {
	// don't do anything without RTM support
	if hasRTM == false {
		lock.RUnlock()
		return
	}
	if lock != ml.underlyingRWMutex {
		TxAbortOnDifferentLock()
	}
	funcAddr := (uintptr)(unsafe.Pointer(ml))
	lockAddr := (uintptr)(unsafe.Pointer(lock))
	idx := getPerceptronIndex(funcAddr, lockAddr)
	entry := &weight[_RLOCK_WEIGHT][idx]

	if ml.isSlowPath == true {
		// it is a RWMutex
		lock.RUnlock()
		ml.SlowUnlock++
		timeElapse := rdtsc.Counter() - ml.tsc

		if ml.rtmFails == true {
			// update the weight since they are not working
			entry.weightIP = bounded(entry.weightIP, -1)
			entry.weightLock = bounded(entry.weightLock, -1)
			entry.failure++
			if entry.trial > 0 {
				if timeElapse > uint64(entry.duration*uint32(entry.trial)*2) {
					entry.duration = uint32(timeElapse)
				} else {
					entry.trial--
				}
			}
		}
	} else {
		// it is a rtm
		TxEnd()

		timeElapse := rdtsc.Counter() - ml.tsc

		// HTM works, update the weight
		entry.weightIP = bounded(entry.weightIP, int8(entry.trial))
		entry.weightLock = bounded(entry.weightLock, int8(entry.trial))

		// entry.duration = int(float64(entry.duration) * 0.8)
		entry.duration += uint32(float64(timeElapse) * float64(entry.trial-entry.remainingTrial))
		entry.success++
		if entry.remainingTrial <= 1 {
			entry.duration = uint32(timeElapse)
			entry.trial--
		} else if entry.trial-entry.remainingTrial <= 1 && entry.duration > 2*uint32(timeElapse) {
			entry.duration = uint32(timeElapse)
		}

		entry.success++
	}
}

// WLock tries to use RTM and mark the boolean variable
// if failed, it will roll back to RWMutex
//go:nosplit
func (ml *OptiLock) WLock(lock *sync.RWMutex) {
	// don't do anything without RTM support
	if hasRTM == false {
		lock.Lock()
		return
	}
	ml.isSlowPath = false
	ml.rtmFails = false
	ml.underlyingRWMutex = lock
	funcAddr := (uintptr)(unsafe.Pointer(ml))
	lockAddr := (uintptr)(unsafe.Pointer(lock))
	idx := getPerceptronIndex(funcAddr, lockAddr)
	entry := &weight[_WLOCK_WEIGHT][idx]
	// fast path
	// localTrial := 1 + entry.trial // 1+ to make sure we are never zero

	if entry.sleep > slowpathRepeatThreshold {
		reInit(entry)
		// retry only once on write lock
		entry.trial = 1
		entry.remainingTrial = 1
	}

	// perceptron says to use HTM
	if goOnSlowPath(entry) {
		// perceptron says not to use HTM
		goto _SLOWPATH
	}

	for {
		//TODO: can use a switch on the status, if that does not hurt performance due to branch prediction.
		ml.tsc = rdtsc.Counter()
		status := TxBegin()

		if status == TxBeginStarted {
			// check if lock is taken, if yes abort the HTM
			if *getStateRWMutex(lock) != 0 {
				TxAbort()
			}
			return
		}

		// Transaction failed for some reason.
		// Lets investigate.
		// abort because lock and unlock do not pair
		if isUnpairedUnlock(status) || entry.remainingTrial == 0 {
			ml.rtmFails = true
			goto _SLOWPATH
		}
		entry.remainingTrial--

		if status&(TxAbortRetry|TxAbortConflict|TxAbortExplicit) != 0 {
			// Spin a bit and then retry
			loopCnt := int(entry.duration)
			for i := 0; i < loopCnt; i++ {
				atomic.LoadInt32(&dummySpinLoad[32])
			}
		}
	}

_SLOWPATH:
	entry.sleep++ // Racey update is OK.
	ml.isSlowPath = true
	ml.SlowLock++ // TODO: remove?
	loopCnt := int(entry.duration)
	for i := 0; i < loopCnt; i++ {
		atomic.LoadInt32(&dummySpinLoad[32])
	}
	lock.Lock()
	return
}

// WUnlock will check if the lock is RWMutex or
// then commit or unlock accordingly
//go:nosplit
func (ml *OptiLock) WUnlock(lock *sync.RWMutex) {
	// don't do anything without RTM support
	if hasRTM == false {
		lock.Unlock()
		return
	}
	if lock != ml.underlyingRWMutex {
		TxAbortOnDifferentLock()
	}
	funcAddr := (uintptr)(unsafe.Pointer(ml))
	lockAddr := (uintptr)(unsafe.Pointer(lock))
	idx := getPerceptronIndex(funcAddr, lockAddr)
	entry := &weight[_WLOCK_WEIGHT][idx]

	if ml.isSlowPath == true {
		// it is a RWMutex
		lock.Unlock()
		ml.SlowUnlock++
		timeElapse := rdtsc.Counter() - ml.tsc
		// update the weight since they are not working

		// we enter here since we used up all the trials
		if ml.rtmFails == true {
			entry.weightIP = bounded(entry.weightIP, -1)
			entry.weightLock = bounded(entry.weightLock, -1)
			entry.failure++
			if entry.trial > 0 {
				// allows more trial only if the spin time is much shorter than time elapsed.
				if timeElapse > uint64(entry.duration*uint32(entry.trial)*2) {
					entry.duration = uint32(timeElapse)
				} else {
					entry.trial--
				}
			}
		}
	} else {
		// it is a rtm
		TxEnd()

		// HTM works, update the weight

		timeElapse := rdtsc.Counter() - ml.tsc

		entry.weightIP = bounded(entry.weightIP, int8(entry.trial))
		entry.weightLock = bounded(entry.weightLock, int8(entry.trial))
		if entry.remainingTrial <= 1 {
			entry.duration = uint32(timeElapse)
			entry.trial--
		} else if entry.trial-entry.remainingTrial <= 1 && entry.duration > 2*uint32(timeElapse) {
			entry.duration = uint32(timeElapse)
		}

		entry.success++
	}
}
