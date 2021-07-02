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

# This tags the latest release

current=$(git tag --list --sort version:refname | tail -n1)
next="v$(rosa version)"
echo "Tagging release $next"

# Create git release tag
log=$(git log $current..$next --oneline --no-merges --no-decorate --reverse | grep -v $next | sed "s/^\w*/-/")
read -r -p "Create release tag '$next'? [y/N] " response
if [[ "$response" =~ ^([yY][eE][sS]|[yY])$ ]]
then
  git tag --annotate --message "Release $next" --message "$log" $next
else
  echo -e "\tgit tag --annotate $next"
fi

# Push git release tag to upstream GitHub repository
upstream=$(git remote --verbose | grep "github\.com.openshift\/rosa\.git" | tail -n1 | awk '{print $1}')
read -r -p "Push tag '$next' to GitHub? [y/N] " response
if [[ "$response" =~ ^([yY][eE][sS]|[yY])$ ]]
then
  git push $upstream $next
else
  echo -e "\tgit push $upstream $next"
fi
