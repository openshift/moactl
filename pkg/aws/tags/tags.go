/*
Copyright (c) 2020 Red Hat, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

  http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// This file contains the tags that are used to store additional information in objects created
// in AWS.

package tags

// Prefix used by all the tag names:
const prefix = "rosa_"

// ClusterName is the name of the tag that will contain the name of the cluster.
const ClusterName = prefix + "cluster_name"

// ClusterID is the name of the tag that will contain the identifier of the cluster.
const ClusterID = prefix + "cluster_id"

// ClusterID is the name of the tag that will contain the identifier of the cluster.
const ClusterRegion = prefix + "region"

// OpenShiftVersion is the name of the tag that will contain
// the version of OpenShift that the resources are used for
const OpenShiftVersion = prefix + "openshift_version"

// RoleType is the name of the tag that will contain the purpose of the role (installer, support, etc.)
const RoleType = prefix + "role_type"

// RolePrefix is the name of the tag that will contain the user-set prefix of the role (installer, support, etc.)
const RolePrefix = prefix + "role_prefix"
