package gordtsc

import (
	"fmt"
	"testing"
	"time"
)

func BenchmarkBenchStart(b *testing.B) {
	a := Counter()
	time.Sleep(5)
	fmt.Printf("Value is : %v\n", Counter()-a)
}
