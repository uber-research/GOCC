//Copyright (c) 2020 Uber Technologies, Inc.
//
//Licensed under the Uber Non-Commercial License (the "License");
//you may not use this file except in compliance with the License.
//You may obtain a copy of the License at the root directory of this project.
//
//See the License for the specific language governing permissions and
//limitations under the License.
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

