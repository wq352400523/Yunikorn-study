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

package scheduler

import (
    "fmt"
    "github.com/cloudera/yunikorn-core/pkg/cache"
    "github.com/cloudera/yunikorn-core/pkg/common/commonevents"
    "github.com/cloudera/yunikorn-core/pkg/common/resources"
    "github.com/cloudera/yunikorn-scheduler-interface/lib/go/si"
)

// Preemption policy based-on DRF
type DRFPreemptionPolicy struct {
}

func (m *DRFPreemptionPolicy) DoPreemption(scheduler *Scheduler) {
    // First calculate ideal resource
    calculateIdealResources(scheduler)

    // Then go to under utilized queues and search for requests
    scheduler.singleStepSchedule(16, &preemptionParameters{crossQueuePreemption: true, blacklistedRequest: make(map[string]bool)})
}

/*
 * It is possible that, preempt resources from one queue may not make it consumed by another queue.
 * An example is:
 *             root
 *         /    |   \
 *       a      b    c
 *            /  \
 *           b1   b2
 *
 * Let's assume a is underutilized and satisfied (no pending request).
 * b is over-utilized and reaches its maximum limit.
 * c is over-utilized.
 * b1 is demanding and underutilized, b2 is over-utilized.
 * For this case, preempting resources from c cannot help with b1, because b reaches its maximum capacity and cannot consume more resources.
 *
 * So our algorithm should properly answer: if we preempt resources N from queue X (preemptee),
 * can we make sure demanding queue Y (preemptor) or its parent reduces their shortages
 * return true if positive contribution made to headroom shortage.

TODO: An optimization is: calculate contributions first, and sort preemption victims by descend order of contribution to resource-to-preempt.
 */
func headroomShortageUpdate(preemptor *preemptionQueueContext, preemptee *preemptionQueueContext, allocationResource *resources.Resource,
    queueHeadroomShortages map[string]*resources.Resource) bool {
    // When we don't have any resource shortage issue, no positive contribution we can make.
    if len(queueHeadroomShortages) == 0 {
        return false
    }

    // Try to deduct allocation resource from queueHeadroom shortage, and see if it makes positive contribution.
    cur := preemptee
    positiveContribution := false
    for cur != nil {
        if headroomShortage := queueHeadroomShortages[cur.queuePath]; headroomShortage != nil {
            newHeadroomShortage := resources.SubEliminateNegative(headroomShortage, allocationResource)
            if resources.StrictlyGreaterThan(headroomShortage, newHeadroomShortage) {
                // Good, makes positive contribution
                if resources.StrictlyGreaterThanZero(newHeadroomShortage) {
                    queueHeadroomShortages[cur.queuePath] = newHeadroomShortage
                } else {
                    delete(queueHeadroomShortages, cur.queuePath)
                }
                positiveContribution = true
            }
        }
        cur = cur.parent
    }

    return positiveContribution
}

func initHeadroomShortages(preemptorQueue *preemptionQueueContext, allocatedResource *resources.Resource) map[string]*resources.Resource {
    // Get all headroom shortages of preemptor's parent.
    headroomShortages := make(map[string]*resources.Resource)
    cur := preemptorQueue
    for cur != nil {
        // Headroom = max - may_allocated + preempting
        headroom := resources.Sub(cur.resources.max, cur.schedulingQueue.GetAllocatingResource())
        resources.AddTo(headroom, cur.resources.markedPreemptedResource)
        headroomShortage := resources.SubEliminateNegative(allocatedResource, headroom)
        if resources.StrictlyGreaterThanZero(headroomShortage) {
            headroomShortages[cur.queuePath] = headroomShortage
        }
        cur = cur.parent
    }

    return headroomShortages
}

// Can we do surgical preemption on the node?
type singleNodePreemptResult struct {
    node                  *SchedulingNode
    toReleaseAllocations  map[string]*cache.AllocationInfo
    totalReleasedResource *resources.Resource
}

// Do surgical preemption on node, if able to preempt, returns
func trySurgicalPreemptionOnNode(preemptionPartitionCtx *preemptionPartitionContext, preemptorQueue *preemptionQueueContext, node *SchedulingNode, candidate *SchedulingAllocationAsk,
    headroomShortages map[string]*resources.Resource) *singleNodePreemptResult {
    // To preempt resource = (allocating + candidate.asked) - (preempting + available)
    resourceToPreempt := resources.Add(node.AllocatingResource, candidate.AllocatedResource)
    resources.SubFrom(resourceToPreempt, node.PreemptingResource)
    resources.SubFrom(resourceToPreempt, node.CachedAvailableResource)
    resourceToPreempt = resources.ComponentWiseMax(resourceToPreempt, resources.Zero)

    // If allocated resource can fit in the node, and no headroom shortage of preemptor queue, we can directly get it allocated. (lucky!)
    if node.CheckAndAllocateResource(candidate.AllocatedResource, true /* preemptionPhase */) {
        return &singleNodePreemptResult{
            node:                  node,
            toReleaseAllocations:  make(map[string]*cache.AllocationInfo),
            totalReleasedResource: resources.Zero,
        }
    }

    toReleaseAllocations := make(map[string]*cache.AllocationInfo)
    totalReleasedResource := resources.NewResource()

    // Otherwise, try to do preemption, list all allocations on the node.
    // Fixme: this operation has too many copies, should avoid for better perf
    for _, alloc := range node.NodeInfo.GetAllAllocations() {
        queueName := alloc.AllocationProto.QueueName
        // Try to do preemption.
        preemptQueue := preemptionPartitionCtx.leafQueues[queueName]
        if nil == preemptQueue {
            continue
        }

        // Skip when the queue has <= 0 preempt-able resource
        if resources.Comp(preemptionPartitionCtx.partitionTotalResource, preemptQueue.resources.preemptable, resources.Zero) <= 0 {
            continue
        }

        postPreemption := resources.SubEliminateNegative(preemptQueue.resources.preemptable, candidate.AllocatedResource)

        // Make sure this preemption has positive impact of preemptable values. A corner case is:
        // A queue has preemptable resource = (memory=0, cpu=0, gpu=2), for this case, we should avoid preempt any allocation w/o gpu resource > 0
        if resources.Comp(preemptionPartitionCtx.partitionTotalResource, postPreemption, preemptQueue.resources.preemptable) >= 0 {
            continue
        }

        // Add one more check, to make sure that preempted resource will be used by candidate queue.
        // When this check fails it means preempted container doesn't make a positive contribution towards preemptor queue and its parents' headroom shortages. (
        // How much headroom needed to allocate candidate).
        headroomShortageUpdate(preemptorQueue, preemptQueue, alloc.AllocatedResource, headroomShortages)

        // let's preempt the container.
        toReleaseAllocations[alloc.AllocationProto.Uuid] = alloc
        resources.AddTo(totalReleasedResource, alloc.AllocatedResource)

        // Check if we preempted enough resources.
        if resources.StrictlyGreaterThanOrEquals(totalReleasedResource, resourceToPreempt) {
            return &singleNodePreemptResult{
                node:                  node,
                toReleaseAllocations:  toReleaseAllocations,
                totalReleasedResource: totalReleasedResource,
            }
        }
    }

    return nil
}

func crossQueuePreemptionAllocate(preemptionPartitionContext *preemptionPartitionContext, nodes []*SchedulingNode, candidate *SchedulingAllocationAsk,
    preemptionParam *preemptionParameters) *SchedulingAllocation {
    if preemptionPartitionContext == nil {
        return nil
    }

    preemptorQueue := preemptionPartitionContext.leafQueues[candidate.QueueName]
    if preemptorQueue == nil {
        return nil
    }

    headroomShortages := initHeadroomShortages(preemptorQueue, candidate.AllocatedResource)

    // TODO: do we want to make allocation-within-preemption sorted instead of randomly check nodes one-by-one?
    // First let's sort nodes by available resource
    var preemptResult *singleNodePreemptResult = nil
    for _, node := range nodes {
        if preemptResult = trySurgicalPreemptionOnNode(preemptionPartitionContext, preemptorQueue, node, candidate, headroomShortages); preemptResult != nil {
            break
        }
    }

    if preemptResult == nil {
        return nil
    }

    preemptionResults := make([]*singleNodePreemptResult, 0)
    preemptionResults = append(preemptionResults, preemptResult)
    nodeToAllocate := preemptResult.node

    // Now let's see if we need to reclaim some headroom shortages

    if len(headroomShortages) != 0 {
        // Yeah .. we have some shortages.
        // TODO: Do this shotgun preemption in a separate patch, it should be very similar to CS's shotgun preemption. And we can prioritize to preempt from later launched
        // allocations, allocation with lower priorities, etc.
    }

    preemptorQueue.schedulingQueue.IncAllocatingResource(candidate.AllocatedResource)

    // Finally, let's do preemption proposals
    return createPreemptionAndAllocationProposal(preemptionPartitionContext, nodeToAllocate, candidate, preemptionResults)
}

//抢占
func createPreemptionAndAllocationProposal(preemptionPartitionContext *preemptionPartitionContext, nodeToAllocate *SchedulingNode, candidate *SchedulingAllocationAsk,
    preemptionResults []*singleNodePreemptResult) *SchedulingAllocation {
    // We will get this allocation by preempting resources.
    allocation := NewSchedulingAllocation(candidate, nodeToAllocate.NodeId)
    allocation.Releases = make([]*commonevents.ReleaseAllocation, 0)

    // And add releases
    for _, pr := range preemptionResults {
        for uuid, alloc := range pr.toReleaseAllocations {

            allocation.Releases = append(allocation.Releases, commonevents.NewReleaseAllocation(uuid, alloc.ApplicationId, nodeToAllocate.NodeInfo.Partition,
                fmt.Sprintf("Preempt allocation=%s for ask=%s", alloc, candidate.AskProto.AllocationKey), si.AllocationReleaseResponse_PREEMPTED_BY_SCHEDULER))

            // Update metrics of preempt queue
            preemptQueue := preemptionPartitionContext.leafQueues[alloc.AllocationProto.QueueName]
            resources.AddTo(preemptQueue.resources.markedPreemptedResource, alloc.AllocatedResource)
            preemptQueue.resources.preemptable = resources.SubEliminateNegative(preemptQueue.resources.preemptable, alloc.AllocatedResource)
        }
        resources.AddTo(pr.node.PreemptingResource, pr.totalReleasedResource)
    }

    // Update metrics
    // For node, update allocating and preempting resources
    resources.AddTo(nodeToAllocate.AllocatingResource, candidate.AllocatedResource)

    return allocation
}
