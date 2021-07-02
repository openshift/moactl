#!/bin/bash
#
# Copyright (c) 2021 Red Hat, Inc.
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

# This generates the commit necessary for release of the next z-stream tag.

current=$(rosa version)
echo "Current version is $current"

base=$(echo $current | grep -o ".*\.")
next_z=$(echo $current | sed -E "s/.*\.([0-9]*)/\1+1/" | bc)
next=$base$next_z
echo "Next version will be $next"

# Update version
read -r -p "Update version to '$next'? [y/N] " response
if [[ "$response" =~ ^([yY][eE][sS]|[yY])$ ]]
then
  sed -i "s/$current/$next/" pkg/info/info.go
fi

# Update changelog
read -r -p "Update changelog? [y/N] " response
if [[ "$response" =~ ^([yY][eE][sS]|[yY])$ ]]
then
  log=$(git log v$current..HEAD --oneline --no-merges --no-decorate --reverse | sed "s/^\w*/-/")
  echo "$log"

  rest=$(awk "/ $current /{found=1} found" CHANGES.adoc)
  header=$(cat << EOM
= Changes

This document describes the relevant changes between releases of the \`rosa\` command line tool.
EOM
  )
  echo -e "$header\n\n== $next $(date "+%b %-d %Y")\n\n$log\n\n$rest" > CHANGES.adoc
fi

# Commit changes
branch="release_$next"
read -r -p "Commit changes to branch '$branch'? [y/N] " response
if [[ "$response" =~ ^([yY][eE][sS]|[yY])$ ]]
then
  git checkout -b $branch
  git commit --all --message "Release v$next" --message "$log"
else
  echo -e "\tgit checkout -b $branch"
  echo -e "\tgit commit --all"
fi

read -r -p "Push branch '$branch' to GitHub? [y/N] " response
if [[ "$response" =~ ^([yY][eE][sS]|[yY])$ ]]
then
  git push --set-upstream origin $branch
else
  echo -e "\tgit push --set-upstream origin $branch"
fi
