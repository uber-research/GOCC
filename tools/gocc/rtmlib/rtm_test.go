package rtmlib

import (
	"fmt"
	"math/rand"
	"reflect"
	"runtime"
	"sync"
	"testing"
	"time"
	"unsafe"
)

func TestSimple(t *testing.T) {
	m := map[string]int{
		"word1": 0,
		"word2": 0,
	}

	majicLock := OptiLock{}
	var wg sync.WaitGroup
	lock := &sync.Mutex{}

	wg.Add(2)
	go func() {
		majicLock.Lock(lock)
		// Action to be done transactionally
		m["word1"] = m["word1"] + 1
		majicLock.Unlock(lock)
		wg.Done()
	}()
	go func() {
		majicLock.Lock(lock)
		// Action to be done transactionally
		m["word1"] = m["word1"] + 1
		majicLock.Unlock(lock)
		wg.Done()
	}()
	wg.Wait()

	fmt.Println("word1 =", m["word1"])
}

var buf1 [64]int
var buf2 [64]int

func MyGoRoutine(wg *sync.WaitGroup) {
	failure := 0
	// produce graph with different values of GOMAXPROC=<2, 4, 8, 16, 32>
	sec := rand.Intn(90) + 10
	majicLock := OptiLock{}
	m := &sync.Mutex{}
	for i := 0; i < 10000000; i++ {
		time.Sleep(time.Duration(sec) * time.Millisecond)
		majicLock.Lock(m)
		buf1[0]++
		if buf1[0] != buf2[0]-1 {
			failure++
		}
		buf2[0]++
		majicLock.Unlock(m)
	}
	// assert failure == 0
	if failure != 0 {
		panic("something is wrong with HTM implementation")
	}
	wg.Done()
}

func TestCorrectness(t *testing.T) {
	coreNum := []int{2, 4, 8, 16, 32, 64}
	var wg sync.WaitGroup
	for _, i := range coreNum {
		numGoRoutines := runtime.GOMAXPROCS(i)
		wg.Add(numGoRoutines)
		for n := 0; n < numGoRoutines; n++ {
			go MyGoRoutine(&wg)
		}
	}
}

func TestWRLockState(t *testing.T) {
	m := &sync.RWMutex{}
	fmt.Println(*(*int32)(unsafe.Pointer(m)))
	fmt.Println(reflect.ValueOf(m).Elem().FieldByName("w"))
	m.Lock()
	fmt.Println(*(*int32)(unsafe.Pointer(m)))
	fmt.Println(reflect.ValueOf(m).Elem().FieldByName("w"))
	m.Unlock()
	fmt.Println(*(*int32)(unsafe.Pointer(m)))
	fmt.Println(reflect.ValueOf(m).Elem().FieldByName("w"))
}
