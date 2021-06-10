#Copyright (c) 2020 Uber Technologies, Inc.
#
#Licensed under the Uber Non-Commercial License (the "License");
#you may not use this file except in compliance with the License.
#You may obtain a copy of the License at the root directory of this project. 
#
#See the License for the specific language governing permissions and
#limitations under the License.
#!/bin/bash 
GO=go
$GO fmt
$GO build

## test
GO111MODULE=off go test . -v 
