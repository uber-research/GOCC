#include "textflag.h"

// func TxBegin() (status uint32)
TEXT ·TxBegin(SB),NOPTR|NOSPLIT,$0
    MOVL $0xffffffff, AX
    XBEGIN fallback
fallback:
    MOVL AX, status+0(FP)
    RET

// func TxEnd()
TEXT ·TxEnd(SB),NOPTR|NOSPLIT,$0
    XEND
    RET

// func TxAbort() - this will return $0xf0 on abort
TEXT ·TxAbort(SB),NOPTR|NOSPLIT,$0
    XABORT $0xf0
    RET

// func TxAbortOnDifferentLock() - this will return 0x10 on abort
TEXT ·TxAbortOnDifferentLock(SB),NOPTR|NOSPLIT,$0
    MOVL AX, 1
    XABORT $0x10
    RET

