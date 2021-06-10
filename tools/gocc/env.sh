#!/bin/bash
root=`echo "$(cd "$(dirname "$BASH_SOURCE")" && pwd -P)/"`
TESTDATA=${root}/testdata/
THIRDPARTY=${root}/thirdparty/
ORIG_GOPATH=`go env | grep GOPATH | cut -d'=' -f 2 | tr -d '"'`
export GOPATH=${THIRDPARTY}:${TESTDATA}:${ORIG_GOPATH}
