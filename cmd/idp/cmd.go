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

package idp

import (
	"github.com/spf13/cobra"

	"gitlab.cee.redhat.com/service/moactl/cmd/idp/add"
	"gitlab.cee.redhat.com/service/moactl/cmd/idp/dlt"
	"gitlab.cee.redhat.com/service/moactl/cmd/idp/list"
)

var Cmd = &cobra.Command{
	Use:   "idp COMMAND",
	Short: "Configure IDP for cluster",
	Long:  "Identity providers determine how users log into the cluster.",
}

func init() {
	Cmd.AddCommand(add.Cmd)
	Cmd.AddCommand(dlt.Cmd)
	Cmd.AddCommand(list.Cmd)
}