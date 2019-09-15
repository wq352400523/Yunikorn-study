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
    "github.com/cloudera/yunikorn-core/pkg/log"
    "go.uber.org/zap"
    "sync"
)

type ClusterSchedulingContext struct {
    partitions map[string]*PartitionSchedulingContext

    needPreemption bool

    lock sync.RWMutex
}

func NewClusterSchedulingContext() *ClusterSchedulingContext {
    return &ClusterSchedulingContext{
        partitions: make(map[string]*PartitionSchedulingContext),
    }
}

func (csc *ClusterSchedulingContext) getPartitionMapClone() map[string]*PartitionSchedulingContext {
    csc.lock.RLock()
    defer csc.lock.RUnlock()

    newMap := make(map[string]*PartitionSchedulingContext)
    for k,v := range csc.partitions {
        newMap[k] = v
    }
    return newMap
}

func (csc *ClusterSchedulingContext) GetSchedulingApplication(appId string, partitionName string) *SchedulingApplication {
    csc.lock.RLock()
    defer csc.lock.RUnlock()

    if partition := csc.partitions[partitionName]; partition != nil {
        return partition.getApplication(appId)
    }

    return nil
}

// Visible by tests
func (csc *ClusterSchedulingContext) GetSchedulingQueue(queueName string, partitionName string) *SchedulingQueue {
    csc.lock.RLock()
    defer csc.lock.RUnlock()

    if partition := csc.partitions[partitionName]; partition != nil {
        return partition.GetQueue(queueName)
    }

    return nil
}

func (csc *ClusterSchedulingContext) AddSchedulingApplication(schedulingApp *SchedulingApplication) error {
    partitionName := schedulingApp.ApplicationInfo.Partition
    appId := schedulingApp.ApplicationInfo.ApplicationId

    csc.lock.Lock()
    defer csc.lock.Unlock()

    if partition := csc.partitions[partitionName]; partition != nil {
        if err := partition.AddSchedulingApplication(schedulingApp); err != nil {
            return err
        }
    } else {
        return fmt.Errorf("failed to find partition=%s while adding app=%s", partitionName, appId)
    }

    return nil
}

func (csc *ClusterSchedulingContext) RemoveSchedulingApplication(appId string, partitionName string) (*SchedulingApplication, error) {
    csc.lock.Lock()
    defer csc.lock.Unlock()

    if partition := csc.partitions[partitionName]; partition != nil {
        schedulingApp, err := partition.RemoveSchedulingApplication(appId)
        if err != nil {
            return nil, err
        }
        return schedulingApp, nil
    } else {
        return nil, fmt.Errorf("failed to find partition=%s while remove app=%s", partitionName, appId)
    }
}

// Update the scheduler's partition list based on the processed config
// - updates existing partitions and the queues linked
// - add new partitions including queues
// updates and add internally are processed differently outside of this method they are the same.
func (csc *ClusterSchedulingContext) updateSchedulingPartitions(partitions []*cache.PartitionInfo) error {
    csc.lock.Lock()
    defer csc.lock.Unlock()
    log.Logger().Info("updating scheduler context",
        zap.Int("numOfPartitionsUpdated", len(partitions)))

    // Walk over the updated partitions
    for _, updatedPartition := range partitions {
        csc.needPreemption = csc.needPreemption || updatedPartition.NeedPreemption()

        partition := csc.partitions[updatedPartition.Name]
        if partition != nil {
            log.Logger().Info("updating scheduling partition",
                zap.String("partitionName", updatedPartition.Name))
            // the partition details don't need updating just the queues
            partition.updatePartitionSchedulingContext(updatedPartition)
        } else {
            log.Logger().Info("creating scheduling partition",
                zap.String("partitionName", updatedPartition.Name))
            // create a new partition and add the queues
            root := NewSchedulingQueueInfo(updatedPartition.Root, nil)
            newPartition := newPartitionSchedulingContext(updatedPartition, root)
            newPartition.partitionManager = &PartitionManager{
                psc: newPartition,
                csc: csc,
            }
            go newPartition.partitionManager.Run()

            csc.partitions[updatedPartition.Name] = newPartition
        }
    }
    return nil
}

func (csc *ClusterSchedulingContext) RemoveSchedulingPartitionsByRMId(rmId string) {
    csc.lock.Lock()
    defer csc.lock.Unlock()
    partitionToRemove := make(map[string]bool)

    // Just remove corresponding partitions
    for k, partition := range csc.partitions {
        if partition.RmId == rmId {
            partition.partitionManager.stop = true
            partitionToRemove[k] = true
        }
    }

    for partitionName := range partitionToRemove {
        delete(csc.partitions, partitionName)
    }
}

// Remove the partition from the scheduler based on a configuration change
// No resources can be used and the underlying partition should not be running
func (csc *ClusterSchedulingContext) deleteSchedulingPartitions(partitions []*cache.PartitionInfo) error {
    csc.lock.Lock()
    defer csc.lock.Unlock()

    var err error
    // Walk over the deleted partitions
    for _, deletedPartition := range partitions {
        partition := csc.partitions[deletedPartition.Name]
        if partition != nil {
            log.Logger().Info("marking scheduling partition for deletion",
                zap.String("partitionName", deletedPartition.Name))
            partition.partitionManager.Stop()
        } else {
            // collect all errors and keep processing
            if err == nil {
                err = fmt.Errorf("failed to find partition that should have been deleted: %s", deletedPartition.Name)
            } else {
                err = fmt.Errorf("%v, %s", err, deletedPartition.Name)
            }
        }
    }
    return err
}

func (csc* ClusterSchedulingContext) NeedPreemption() bool {
    csc.lock.RLock()
    defer csc.lock.RUnlock()

    return csc.needPreemption
}

// Callback from the partition manager to finalise the removal of the partition
func (csc *ClusterSchedulingContext) removeSchedulingPartition(partitionName string) {
    csc.lock.RLock()
    defer csc.lock.RUnlock()

    delete(csc.partitions, partitionName)
}
