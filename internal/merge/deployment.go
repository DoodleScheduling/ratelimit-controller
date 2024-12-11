// Copyright 2016 The prometheus-operator Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package merge

import (
	"encoding/json"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"

	"k8s.io/apimachinery/pkg/util/strategicpatch"
)

func MergePatchDeployment(base, patch appsv1.Deployment) (*appsv1.Deployment, error) {
	baseBytes, err := json.Marshal(base)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal JSON for base: %w", err)
	}

	patchBytes, err := json.Marshal(patch)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal JSON for patch: %w", err)
	}

	// Calculate the patch result.
	jsonResult, err := strategicpatch.StrategicMergePatch(baseBytes, patchBytes, v1.Container{})
	if err != nil {
		return nil, fmt.Errorf("failed to generate merge patch: %w", err)
	}

	var patchResult appsv1.Deployment
	if err := json.Unmarshal(jsonResult, &patchResult); err != nil {
		return nil, fmt.Errorf("failed to unmarshal merged deployment: %w", err)
	}

	return &patchResult, nil
}
