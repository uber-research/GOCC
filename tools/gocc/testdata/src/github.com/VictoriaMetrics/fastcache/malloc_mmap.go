// +build !appengine,!windows

package fastcache

import (
	"fmt"
	rtm "github.com/uber-research/GOCC/tools/gocc/rtmlib"
	"sync"
	"syscall"
	"unsafe"
)

const chunksPerAlloc = 1024

var (
	freeChunks     []*[chunkSize]byte
	freeChunksLock sync.Mutex
)

func getChunk() []byte {
	freeChunksLock.Lock()
	if len(freeChunks) == 0 {
		// Allocate offheap memory, so GOGC won't take into account cache size.
		// This should reduce free memory waste.
		data, err := syscall.Mmap(-1, 0, chunkSize*chunksPerAlloc, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_ANON|syscall.MAP_PRIVATE)
		if err != nil {
			panic(fmt.Errorf("cannot allocate %d bytes via mmap: %s", chunkSize*chunksPerAlloc, err))
		}
		for len(data) > 0 {
			p := (*[chunkSize]byte)(unsafe.Pointer(&data[0]))
			freeChunks = append(freeChunks, p)
			data = data[chunkSize:]
		}
	}
	n := len(freeChunks) - 1
	p := freeChunks[n]
	freeChunks[n] = nil
	freeChunks = freeChunks[:n]
	freeChunksLock.Unlock()
	return p[:]
}

func putChunk(chunk []byte) {
	optiLock1 := rtm.OptiLock{}
	if chunk == nil {
		return
	}
	chunk = chunk[:chunkSize]
	p := (*[chunkSize]byte)(unsafe.Pointer(&chunk[0]))

	optiLock1.Lock(&freeChunksLock)
	freeChunks = append(freeChunks, p)
	optiLock1.Unlock(&freeChunksLock)
}
