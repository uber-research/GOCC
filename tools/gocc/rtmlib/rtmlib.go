package rtmlib

// TxBegin is the start of transaction. It will return TxBeginStarted
// if transaction works, otherwise it returns different status code
func TxBegin() (status uint32)

// TxAbort aborts transaction
func TxAbort()

// TxAbortOnDifferentLock aborts transaction if the lock is not the same
func TxAbortOnDifferentLock()

// TxEnd marks the end of transaction
func TxEnd()

// GetImm returns customized status code from higher 8 bits.
func GetImm(status uint32) uint8 {
	return uint8(((status) >> 24) & 0xff)
}

// refer to Intel manual
const (
	TxBeginStarted  uint32 = ^uint32(0)
	TxAbortExplicit uint32 = (1 << 0)
	TxAbortRetry    uint32 = (1 << 1)
	TxAbortConflict uint32 = (1 << 2)
	TxAbortCapacity uint32 = (1 << 3)
	TxAbortDebug    uint32 = (1 << 4)
	TxAbortNested   uint32 = (1 << 5)
)
