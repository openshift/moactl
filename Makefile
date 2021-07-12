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

# Ensure go modules are enabled:
export GO111MODULE=on
export GOPROXY=https://proxy.golang.org

# Disable CGO so that we always generate static binaries:
export CGO_ENABLED=0

# Unset GOFLAG for CI and ensure we've got nothing accidently set
unexport GOFLAGS

.PHONY: rosa
rosa:
	go build ./cmd/rosa

.PHONY: test
test:
	go test ./...

.PHONY: install
install:
	go install ./cmd/rosa

.PHONY: fmt
fmt:
	gofmt -s -l -w cmd pkg

.PHONY: lint
lint:
	golangci-lint run --timeout 5m0s

.PHONY: clean
clean:
	rm -rf \
		rosa \
		*-darwin-amd64 \
		*-linux-amd64 \
		*-windows-amd64 \
		*.sha256 \
		$(NULL)

.PHONY: generate
generate:
	which go-bindata || GO111MODULE=off go get -u github.com/go-bindata/go-bindata/...
	go-bindata -nometadata -nocompress -pkg assets -o ./assets/bindata.go ./templates/...

mocks:
	mockgen --build_flags=--mod=mod -package mocks -destination=pkg/aws/mocks/iamapi.go github.com/aws/aws-sdk-go/service/iam/iamiface IAMAPI
	mockgen --build_flags=--mod=mod -package mocks -destination=pkg/aws/mocks/organaztionsapi.go github.com/aws/aws-sdk-go/service/organizations/organizationsiface OrganizationsAPI
	mockgen --build_flags=--mod=mod -package mocks -destination=pkg/aws/mocks/stsapi.go github.com/aws/aws-sdk-go/service/sts/stsiface STSAPI
	mockgen --build_flags=--mod=mod -package mocks -destination=pkg/aws/mocks/cloudformationapi.go github.com/aws/aws-sdk-go/service/cloudformation/cloudformationiface CloudFormationAPI
	mockgen --build_flags=--mod=mod -package mocks -destination=pkg/aws/mocks/ec2api.go github.com/aws/aws-sdk-go/service/ec2/ec2iface EC2API
	mockgen --build_flags=--mod=mod -package mocks -destination=pkg/aws/mocks/servicequotasapi.go github.com/aws/aws-sdk-go/service/servicequotas/servicequotasiface ServiceQuotasAPI
	mockgen --build_flags=--mod=mod -package mocks -destination=cmd/create/idp/mocks/identityprovider.go -source=cmd/create/idp/cmd.go IdentityProvider
