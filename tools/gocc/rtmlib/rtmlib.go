//go:build amd64
// +build amd64

// Copyright (c) 2020 Uber Technologies, Inc.
//
// Licensed under the Uber Non-Commercial License (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at the root directory of this project.
//
// See the License for the specific language governing permissions and
// limitations under the License.
package rtmlib

// TxBegin is the start of transaction. It will return TxBeginStarted
// if transaction works, otherwise it returns different status code.
func TxBegin() (status uint32)

// TxAbort aborts transaction.
func TxAbort()

// TxAbortOnDifferentLock aborts transaction if the lock is not the same.
func TxAbortOnDifferentLock()

// TxEnd marks the end of transaction.
func TxEnd()
