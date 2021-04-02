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
	"runtime"
	"sync"
	"unsafe"

	"github.com/intel-go/cpuid"
	rdtsc "github.com/uber-research/GOCC/tools/gocc/gordtsc"
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
const weightThreshold = 3
const trialNumber = 5
const slowpathRepeatThreshold = 50000 // if we are using the slow path for a long time, retry HTM

const cacheLinePadSize = 128

const notSameLockError = 0x10

// OptiLock has a boolean variable to indicate if the lock uses mutex or HTM
// TODO: change OptiLock into interface so that stats counter is optional
type OptiLock struct {
	isSlowPath        bool
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

var trailNum = []uint8{trialNumber, trialNumber, trialNumber}

func init() {
	hasRTM = cpuid.HasExtendedFeature(cpuid.RTM)
	// if we know the program is executed in single thread, then don't use HTM at all
	procsNum := runtime.GOMAXPROCS(0)
	if procsNum == 1 {
		hasRTM = false
	}
	for j := 0; j < _MAX_WEIGHTS; j++ {
		for i := 0; i < perceptronEntry; i++ {
			weight[j][i].weightIP = 3
			weight[j][i].weightLock = 3
			weight[j][i].trial = trailNum[j]
			weight[j][i].remainingTrial = trailNum[j]
			weight[j][i].duration = 100
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
	c.duration = 50
	c.trial = trialNumber
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

func getPerceptronIndex(funcAddr uintptr) uint64 {
	return ((uint64)((funcAddr)>>3) & (perceptronEntryMask))
}

func getComposedIndex(funcAddr, lockAddr uintptr) uint64 {
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

func goOnSlowPath(c1, c2 *cell) bool {
	return c1.weightIP+c2.weightLock < weightThreshold
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
	idx := getPerceptronIndex(funcAddr)
	id2 := getComposedIndex(funcAddr, lockAddr)
	entry := &weight[_MUTEXT_WEIGHT][idx]
	entry2 := &weight[_MUTEXT_WEIGHT][id2]
	entry.remainingTrial = entry.trial
	noRetry := false
	spinTime := spinLimit
	// fast path

	// there are two ways to control the decay, one is giving a large value and decrement by a little bit
	// for instance trial = 1000, then trial += 10. but when we use the trial we will do use (trial/100) as the real counter
	// The other is using the threshold here
	if entry.sleep > slowpathRepeatThreshold {
		reInit(entry)
	}
	// ml.tsc = rdtsc.Counter()
	// perceptron says to use HTM
	if goOnSlowPath(entry, entry2) {
		// perceptron says not to use HTM
		noRetry = true
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
			noRetry = true
			goto _SLOWPATH
		}
		entry.remainingTrial--
		// Decrement number of trials
		if (status & (TxAbortExplicit | TxAbortRetry | TxAbortConflict)) != 0 {
			// Spin a bit and then retry
			for i := 0; i < spinTime; i++ {
				// atomic.LoadInt32(&dummySpinLoad[32])
			}
			// spinTime <<= 1
		} else {
			noRetry = true
			ml.rtmFails = true
			goto _SLOWPATH
		}
	}

_SLOWPATH:
	entry.sleep++ // Racey update is OK.
	ml.isSlowPath = true

	if noRetry == false {
		for i := 0; i < spinTime; i++ {
			// atomic.LoadInt32(&dummySpinLoad[32])
		}
	}
	// TODO: set this direct jump
	// _SLOWPATH_NOWAIT:
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
	idx := getPerceptronIndex(funcAddr)
	id2 := getComposedIndex(funcAddr, lockAddr)
	entry := &weight[_MUTEXT_WEIGHT][idx]
	entry2 := &weight[_MUTEXT_WEIGHT][id2]

	if ml.isSlowPath == true {
		// it is a mutex
		lock.Unlock()
		// update the weight since they are not working

		// we enter here since we used up all the trials
		if ml.rtmFails == true {
			entry.weightIP = bounded(entry.weightIP, -2)
			entry2.weightLock = bounded(entry2.weightLock, -2)
			if entry.trial > 0 {
				// entry.trial--
			}
		}

	} else {
		// it is a rtm
		TxEnd()
		// HTM works, update the weight
		entry.weightIP = bounded(entry.weightIP, 1)
		entry2.weightLock = bounded(entry2.weightLock, 1)
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
	idx := getPerceptronIndex(funcAddr)
	id2 := getComposedIndex(funcAddr, lockAddr)
	entry := &weight[_RLOCK_WEIGHT][idx]
	entry2 := &weight[_RLOCK_WEIGHT][id2]
	entry.remainingTrial = entry.trial
	noRetry := false
	spinTime := spinLimit
	// fast path
	// localTrial := 1 + entry.trial // 1+ to make sure we are never zero

	if entry.sleep > slowpathRepeatThreshold {
		reInit(entry)
	}

	// perceptron says to use HTM
	if goOnSlowPath(entry, entry2) {
		// perceptron says not to use HTM
		noRetry = true
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
			noRetry = true
			goto _SLOWPATH
		}
		entry.remainingTrial--
		if (status & (TxAbortExplicit | TxAbortRetry | TxAbortConflict)) != 0 {
			// We'll retry
			// try a little bit more if confidence score is high
			// Spin a bit and then retry
			for i := 0; i < spinTime; i++ {
				// atomic.LoadInt32(&dummySpinLoad[32])
			}
			// spinTime <<= 1
		} else {
			noRetry = true
			ml.rtmFails = true
			goto _SLOWPATH
		}
	}

_SLOWPATH:
	entry.sleep++ // Racey update is OK.
	ml.isSlowPath = true

	if noRetry == false {
		for i := 0; i < spinTime; i++ {
			// atomic.LoadInt32(&dummySpinLoad[32])
		}
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
	idx := getPerceptronIndex(funcAddr)
	id2 := getComposedIndex(funcAddr, lockAddr)
	entry := &weight[_RLOCK_WEIGHT][idx]
	entry2 := &weight[_RLOCK_WEIGHT][id2]
	if ml.isSlowPath == true {
		// it is a RWMutex
		lock.RUnlock()

		if ml.rtmFails == true {
			// update the weight since they are not working
			entry.weightIP = bounded(entry.weightIP, -2)
			entry2.weightLock = bounded(entry2.weightLock, -2)
			if entry.trial > 0 {
				// entry.trial--
			}
		}
	} else {
		// it is a rtm
		TxEnd()
		// HTM works, update the weight
		entry.weightIP = bounded(entry.weightIP, 1)
		entry2.weightLock = bounded(entry2.weightLock, 1)
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
	idx := getPerceptronIndex(funcAddr)
	id2 := getComposedIndex(funcAddr, lockAddr)
	entry := &weight[_WLOCK_WEIGHT][idx]
	entry2 := &weight[_WLOCK_WEIGHT][id2]
	entry.remainingTrial = entry.trial
	noRetry := false
	spinTime := spinLimit
	// fast path
	// localTrial := 1 + entry.trial // 1+ to make sure we are never zero

	if entry.sleep > slowpathRepeatThreshold {
		reInit(entry)
	}

	// perceptron says to use HTM
	if goOnSlowPath(entry, entry2) {
		// perceptron says not to use HTM
		noRetry = true
		goto _SLOWPATH
	}

	for {
		//TODO: can use a switch on the status, if that does not hurt performance due to branch prediction.
		// ml.tsc = rdtsc.Counter()
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
			noRetry = true
			goto _SLOWPATH
		}
		// if entry.remainingTrial > 0 {
		entry.remainingTrial--
		// }
		if (status & (TxAbortExplicit | TxAbortRetry | TxAbortConflict)) != 0 {
			// Spin a bit and then retry
			for i := 0; i < spinTime; i++ {
				// atomic.LoadInt32(&dummySpinLoad[32])
			}
			// spinTime <<= 1
		} else {
			noRetry = true
			ml.rtmFails = true
			goto _SLOWPATH
		}
	}

_SLOWPATH:
	entry.sleep++ // Racey update is OK.
	ml.isSlowPath = true

	if noRetry == false {
		for i := 0; i < spinTime; i++ {
			// atomic.LoadInt32(&dummySpinLoad[32])
		}
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
	idx := getPerceptronIndex(funcAddr)
	id2 := getComposedIndex(funcAddr, lockAddr)
	entry := &weight[_WLOCK_WEIGHT][idx]
	entry2 := &weight[_WLOCK_WEIGHT][id2]

	if ml.isSlowPath == true {
		// it is a RWMutex
		lock.Unlock()
		// timeElapse := rdtsc.Counter() - ml.tsc
		// update the weight since they are not working

		// we enter here since we used up all the trials
		if ml.rtmFails == true {
			entry.weightIP = bounded(entry.weightIP, -2)
			entry2.weightLock = bounded(entry2.weightLock, -2)
			if entry.trial > 0 {
				// entry.trial--
			}
		}
	} else {
		// it is a rtm
		TxEnd()

		// HTM works, update the weight

		// timeElapse := rdtsc.Counter() - ml.tsc

		entry.weightIP = bounded(entry.weightIP, 1)
		entry2.weightLock = bounded(entry2.weightLock, 1)
	}
}
