#!/bin/bash
#
# Copyright (c) 2020 Red Hat, Inc.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#   http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
#

# This script checks that the current commit corresponds to a tag and then
# builds the binaries for all the supported platforms. It is itended to
# simplify the release process.

# Get an input tag. Otherwise use the tag that corresponds to the current
# commit. If there is no such tag then there is nothing to do.
head=$(git rev-parse ${1:-HEAD})
tag=$(git describe --exact-match "${head}" 2> /dev/null)
if [ -z "${tag}" ]
then
  echo "Commit '${head}' doesn't correspond to any tag"
  exit 1
else
  echo "Tag is '${tag}'"
fi

# This function builds for the given operating system and architecture
# combination:
function build_cmds {
  # Get the parameters:
  local os="$1"
  local arch="$2"

  # Set the environment variables that tell the Go compiler which operating
  # system and architecture to build for:
  export GOOS="${os}"
  export GOARCH="${arch}"

  # Build the command line tools:
  echo "Building binaries for OS '${os}' and architecture '${arch}'"
  make rosa

  # Rename the generated binaries adding the operating system and architecture
  # name and generate a SHA256 sum:
  echo "Calculating SHA 256 sums"
  if [ -f "rosa.exe" ]
  then
    mv "rosa.exe" "rosa-${os}-${arch}.exe"
    sha256sum "rosa-${os}-${arch}.exe" > "rosa-${os}-${arch}.sha256"
  else
    mv "rosa" "rosa-${os}-${arch}"
    sha256sum "rosa-${os}-${arch}" > "rosa-${os}-${arch}.sha256"
  fi
}

# Build for Linux and macOS:
build_cmds linux amd64
build_cmds darwin amd64
build_cmds windows amd64

# Bye:
exit 0
