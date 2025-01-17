/*
Copyright 2019 Cloudera, Inc.  All rights reserved.

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

package client

import (
	"k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
)

type KubeClient interface {
	// bind a pod to a specific host
	//将pod与调度计算得到的nodeid绑定，完成调度！
	Bind(pod *v1.Pod, hostId string) error

	// Delete a pod from a host
	Delete(pod *v1.Pod) error

	// minimal expose this, only informers factory needs it
	GetClientSet() *kubernetes.Clientset
}

func NewKubeClient(kc string) KubeClient {
	//return newRestClient("127.0.0.1:8001")
	return newSchedulerKubeClient(kc)
}
