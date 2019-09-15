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

package rmevent

import "github.com/cloudera/yunikorn-scheduler-interface/lib/go/si"

type RMNewAllocationsEvent struct {
    RMId        string
    Allocations []*si.Allocation
}

type RMApplicationUpdateEvent struct {
    RMId                 string
    AcceptedApplications []*si.AcceptedApplication
    RejectedApplications []*si.RejectedApplication
}

type RMRejectedAllocationAskEvent struct {
    RMId                   string
    RejectedAllocationAsks []*si.RejectedAllocationAsk
}

type RMReleaseAllocationEvent struct {
    RMId                string
    ReleasedAllocations []*si.AllocationReleaseResponse
}

type RMNodeUpdateEvent struct {
    RMId          string
    AcceptedNodes []*si.AcceptedNode
    RejectedNodes []*si.RejectedNode
}
