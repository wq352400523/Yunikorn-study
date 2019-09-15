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
    "github.com/cloudera/yunikorn-core/pkg/cache"
    "github.com/cloudera/yunikorn-core/pkg/common/resources"
    "github.com/cloudera/yunikorn-core/pkg/common/security"
    "github.com/cloudera/yunikorn-core/pkg/log"
    "go.uber.org/zap"
    "strings"
    "sync"
)

// Represents Queue inside Scheduler
type SchedulingQueue struct {
    Name                string              // Fully qualified path for the queue
    CachedQueueInfo     *cache.QueueInfo    // link back to the queue in the cache
    ProposingResource   *resources.Resource // How much resource added for proposing, this is used by queue sort when do candidate selection
    PartitionResource   *resources.Resource // For fairness calculation
    ApplicationSortType SortType            // How applications are sorted (leaf queue only)
    QueueSortType       SortType            // How sub queues are sorted (parent queue only)

    // Private fields need protection
    childrenQueues     map[string]*SchedulingQueue       // Only for direct children, parent queue only
    applications       map[string]*SchedulingApplication // only for leaf queue
    parent             *SchedulingQueue                  // link back to the parent in the scheduler
    allocatingResource *resources.Resource               // Allocating resource
    pendingResource    *resources.Resource               // Total pending resource   准备使用的资源，但还没确定；每次提交task，这个都会增加
    lock sync.RWMutex
}

func NewSchedulingQueueInfo(cacheQueueInfo *cache.QueueInfo, parent *SchedulingQueue) *SchedulingQueue {
    sq := &SchedulingQueue{}
    sq.Name = cacheQueueInfo.GetQueuePath()
    sq.CachedQueueInfo = cacheQueueInfo
    sq.parent = parent
    sq.ProposingResource = resources.NewResource()
    sq.childrenQueues = make(map[string]*SchedulingQueue)
    sq.applications = make(map[string]*SchedulingApplication)
    sq.pendingResource = resources.NewResource()

    // we can update the parent as we have a lock on the partition or the cluster when we get here
    if parent != nil {
        name := sq.Name[strings.LastIndex(sq.Name, cache.DOT)+1:]
        parent.childrenQueues[name] = sq
    }

    // update the properties
    sq.updateSchedulingQueueProperties(cacheQueueInfo.Properties)

    // initialise the child queues based what is in the cached copy
    for childName, childQueue := range cacheQueueInfo.GetCopyOfChildren() {
        newChildQueue := NewSchedulingQueueInfo(childQueue, sq)
        sq.childrenQueues[childName] = newChildQueue
    }

    return sq
}

func (sq * SchedulingQueue) GetPendingResource() *resources.Resource{
    sq.lock.RLock()
    defer sq.lock.RUnlock()

    return sq.pendingResource
}

// Update the properties for the scheduling queue based on the current cached configuration
func (sq *SchedulingQueue) updateSchedulingQueueProperties(prop map[string]string) {
    // set the defaults, override with what is in the configured properties
    sq.ApplicationSortType = FifoSortPolicy
    sq.QueueSortType = FairSortPolicy
    // walk over all properties and process
    if prop != nil {
        for key, value := range prop {
            if key == cache.ApplicationSortPolicy  && value == "fair" {
                sq.ApplicationSortType = FairSortPolicy
            }
            // for now skip the rest just log them
            log.Logger().Debug("queue property skipped",
                zap.String("key", key),
                zap.String("value", value))
        }
    }
}

// Update the queue properties and the child queues for the queue after a configuration update.
// New child queues will be added.
// Child queues that are removed from the configuration have been changed to a draining state and will not be scheduled.
// They are not removed until the queue is really empty, no action must be taken here.
func (sq *SchedulingQueue) updateSchedulingQueueInfo(info map[string]*cache.QueueInfo, parent *SchedulingQueue) {
    sq.lock.Lock()
    defer sq.lock.Unlock()
    // initialise the child queues based on what is in the cached copy
    for childName, childQueue := range info {
        child := sq.childrenQueues[childName]
        // create a new queue if it does not exist
        if child == nil {
            child = NewSchedulingQueueInfo(childQueue, parent)
        } else {
            child.updateSchedulingQueueProperties(childQueue.Properties)
        }
        child.updateSchedulingQueueInfo(childQueue.GetCopyOfChildren(), child)
    }
}

// Update pending resource of this queue
func (sq *SchedulingQueue) IncPendingResource(delta *resources.Resource) {
    sq.lock.Lock()
    defer sq.lock.Unlock()
    // update the parent
    if sq.parent != nil {
        sq.parent.IncPendingResource(delta)
    }

    sq.pendingResource = resources.Add(sq.pendingResource, delta)
}

// Remove pending resource of this queue
func (sq *SchedulingQueue) DecPendingResource(delta *resources.Resource) {
    sq.lock.Lock()
    defer sq.lock.Unlock()
    // update the parent
    if sq.parent != nil {
        sq.parent.DecPendingResource(delta)
    }

    // TODO we can go negative here, do we really want to do that?
    sq.pendingResource = resources.Sub(sq.pendingResource, delta)
}

func (sq *SchedulingQueue) AddSchedulingApplication(app *SchedulingApplication) {
    sq.lock.Lock()
    defer sq.lock.Unlock()

    sq.applications[app.ApplicationInfo.ApplicationId] = app
}

func (sq *SchedulingQueue) RemoveSchedulingApplication(app *SchedulingApplication) {
    sq.lock.Lock()
    defer sq.lock.Unlock()
    // Update pending resource of this queue and its parents
    totalPending := app.Requests.GetPendingResource()
    if !resources.IsZero(totalPending) {
        sq.pendingResource = resources.Sub(sq.pendingResource, totalPending)
        sq.parent.DecPendingResource(totalPending)
    }

    delete(sq.applications, app.ApplicationInfo.ApplicationId)
}

// Get a copy of the child queues
func (sq *SchedulingQueue) GetCopyOfChildren() map[string]*SchedulingQueue {
    sq.lock.RLock()
    defer sq.lock.RUnlock()

    // add self
    children := make(map[string]*SchedulingQueue)
    for k, v := range sq.childrenQueues {
        children[k] = v
    }
    return children
}

// Remove a child queue from this queue.
// No checks are performed: if the child has been removed already it is a noop.
// This may only be called by the queue removal itself on the registered parent.
// Queue removal is always a bottom up action: leafs first then the parent.
func (sq *SchedulingQueue) removeChildQueue(name string) {
    sq.lock.Lock()
    defer sq.lock.Unlock()

    delete(sq.childrenQueues, name)
}

// Remove the queue from the structure.
// Since nothing is allocated there shouldn't be anything referencing this queue any more.
// The real removal is removing the queue from the parent's child list, use read lock on the queue
func (sq *SchedulingQueue) RemoveQueue() bool {
    sq.lock.RLock()
    defer sq.lock.RUnlock()

    // cannot remove a managed queue that is running
    if sq.isManaged() && sq.isRunning() {
        return false
    }
    // cannot remove a queue that has children or applications assigned
    if len(sq.childrenQueues) > 0 || len(sq.applications) > 0 {
        return false
    }

    // root is always managed and is the only queue with a nil parent: no need to guard
    sq.parent.removeChildQueue(sq.Name)
    return true
}

// Is this queue a leaf or not (i.e parent)
// link back to the underlying queue object to prevent out of sync types

//是否是队列树中的叶子节点
func (sq *SchedulingQueue) isLeafQueue() bool {
    return sq.CachedQueueInfo.IsLeafQueue()
}

// Queue status methods reflecting the underlying queue object state
// link back to the underlying queue object to prevent out of sync states
func (sq *SchedulingQueue) isRunning() bool {
    return sq.CachedQueueInfo.IsRunning()
}

func (sq *SchedulingQueue) isDraining() bool {
    return sq.CachedQueueInfo.IsDraining()
}

func (sq *SchedulingQueue) isStopped() bool {
    return sq.CachedQueueInfo.IsStopped()
}

// Is this queue managed or not.
// link back to the underlying queue object to prevent out of sync types
func (sq *SchedulingQueue) isManaged() bool {
    return sq.CachedQueueInfo.IsManaged()
}

func (sq *SchedulingQueue) isRoot() bool {
    return sq.parent == nil
}

func (sq *SchedulingQueue) GetAllocatingResource() *resources.Resource {
    sq.lock.RLock()
    defer sq.lock.RUnlock()

    return sq.allocatingResource
}

func (sq *SchedulingQueue) IncAllocatingResource(newAlloc *resources.Resource) {
    sq.lock.Lock()
    defer sq.lock.Unlock()

    sq.allocatingResource = resources.Add(sq.allocatingResource, newAlloc)
}

func (sq *SchedulingQueue) SetAllocatingResource(newAlloc *resources.Resource) {
    sq.lock.Lock()
    defer sq.lock.Unlock()

    sq.allocatingResource = newAlloc
}

// Check if the user has access to the queue to submit an application.
// This will check the submit ACL and the admin ACL.
// Calls the cache queue which is doing the real work.
func (sq *SchedulingQueue) CheckSubmitAccess(user security.UserGroup) bool {
    return sq.CachedQueueInfo.CheckSubmitAccess(user)
}

// Check if the user has access to the queue for admin actions.
// Calls the cache queue which is doing the real work.
func (sq *SchedulingQueue) CheckAdminAccess(user security.UserGroup) bool {
    return sq.CachedQueueInfo.CheckAdminAccess(user)
}