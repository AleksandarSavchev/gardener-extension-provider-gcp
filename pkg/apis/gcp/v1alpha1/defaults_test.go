// Copyright (c) 2022 SAP SE or an SAP affiliate company. All rights reserved. This file is licensed under the Apache Software License, v. 2 except as noted otherwise in the LICENSE file
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package v1alpha1_test

import (
	. "github.com/gardener/gardener-extension-provider-gcp/pkg/apis/gcp/v1alpha1"

	v1beta1constants "github.com/gardener/gardener/pkg/apis/core/v1beta1/constants"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Defaults", func() {
	Describe("#SetDefaults_MachineImageVersion", func() {
		It("should default the architecture to amd64", func() {
			obj := &MachineImageVersion{}

			SetDefaults_MachineImageVersion(obj)

			Expect(*obj.Architecture).To(Equal(v1beta1constants.ArchitectureAMD64))
		})
	})
})
