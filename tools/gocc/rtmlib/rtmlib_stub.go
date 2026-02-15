//go:build !amd64
// +build !amd64

// Copyright (c) 2020 Uber Technologies, Inc.
//
// Licensed under the Uber Non-Commercial License (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at the root directory of this project.
//
// See the License for the specific language governing permissions and
// limitations under the License.
package rtmlib

// TxBegin always reports "transaction unavailable" on non-amd64 targets.
func TxBegin() (status uint32) { return 0 }

// TxAbort is a no-op on non-amd64 targets.
func TxAbort() {}

// TxAbortOnDifferentLock is a no-op on non-amd64 targets.
func TxAbortOnDifferentLock() {}

// TxEnd is a no-op on non-amd64 targets.
func TxEnd() {}
