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

package cache

import (
	"fmt"
	"github.com/cloudera/yunikorn-core/pkg/api"
	"github.com/cloudera/yunikorn-k8shim/pkg/client"
	"github.com/cloudera/yunikorn-k8shim/pkg/common"
	"github.com/cloudera/yunikorn-k8shim/pkg/common/events"
	"github.com/cloudera/yunikorn-k8shim/pkg/dispatcher"
	"github.com/cloudera/yunikorn-k8shim/pkg/log"
	"github.com/cloudera/yunikorn-scheduler-interface/lib/go/si"
	"go.uber.org/zap"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sync"

	"github.com/looplab/fsm"
	"k8s.io/api/core/v1"
)

//一个pod对应一个Task，pod的UID对应taskId，pod的labels中的applicationId对应这里的applicationId
type Task struct {
	taskId         string
	applicationId  string
	application    *Application   //applicationId对应的app
	allocationUuid string
	resource       *si.Resource  //pod中定义的resource
	pod            *v1.Pod
	kubeClient     client.KubeClient
	schedulerApi   api.SchedulerApi  //core的api
	nodeName       string         //pod中用户直接指定的nodename？？
	sm             *fsm.FSM
	lock           *sync.RWMutex
}

func newTask(tid string, app *Application, client client.KubeClient, schedulerApi api.SchedulerApi, pod *v1.Pod) Task {
	taskResource := common.GetPodResource(pod)
	return createTaskInternal(tid, app, taskResource, pod, client, schedulerApi)
}

// test only
func CreateTaskForTest(tid string, app *Application, resource *si.Resource,
	client client.KubeClient, schedulerApi api.SchedulerApi) Task {
	// for testing purpose, the pod name is same as the taskId
	taskPod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: tid,
		},
	}
	return createTaskInternal(tid, app, resource, taskPod, client, schedulerApi)
}

func createTaskInternal(tid string, app *Application, resource *si.Resource,
	pod *v1.Pod, client client.KubeClient, schedulerApi api.SchedulerApi) Task {
	task := Task{
		taskId:        tid,
		applicationId: app.GetApplicationId(),
		application:   app,
		pod:           pod,
		resource:      resource,
		kubeClient:    client,
		schedulerApi:  schedulerApi,
		lock:          &sync.RWMutex{},
	}

	var states = events.States().Task
	task.sm = fsm.NewFSM(
		states.Pending,
		fsm.Events{
			{Name: string(events.SubmitTask),
				Src: []string{states.Pending},
				Dst: states.Scheduling},
			{Name: string(events.TaskAllocated),
				Src: []string{states.Scheduling},
				Dst: states.Allocated},
			{Name: string(events.TaskBound),
				Src: []string{states.Allocated},
				Dst: states.Bound},
			{Name: string(events.CompleteTask),
				Src: []string{states.Bound},
				Dst: states.Completed},
			{Name: string(events.KillTask),
				Src: []string{states.Pending, states.Scheduling, states.Allocated, states.Bound},
				Dst: states.Killing},
			{Name: string(events.TaskKilled),
				Src: []string{states.Killing},
				Dst: states.Killed},
			{Name: string(events.TaskRejected),
				Src: []string{states.Pending, states.Scheduling},
				Dst: states.Rejected},
			{Name: string(events.TaskFail),
				Src: []string{states.Rejected, states.Allocated},
				Dst: states.Failed},
		},
		fsm.Callbacks{
			string(events.SubmitTask): task.handleSubmitTaskEvent,
			string(events.TaskFail):   task.handleFailEvent,
			states.Allocated:          task.postTaskAllocated,
			states.Rejected:           task.postTaskRejected,
			states.Completed:          task.postTaskCompleted,
			states.Failed:             task.postTaskFailed,
		},
	)

	return task
}

// event handling
func (task *Task) handle(te events.TaskEvent) error {
	log.Logger.Debug("task state transition",
		zap.String("taskId", task.taskId),
		zap.String("preState", task.sm.Current()),
		zap.String("pendingEvent", string(te.GetEvent())))
	err := task.sm.Event(string(te.GetEvent()), te.GetArgs()...)
	// handle the same state transition not nil error (limit of fsm).
	if err != nil && err.Error() != "no transition"{
		return err
	}
	log.Logger.Debug("task state transition",
		zap.String("taskId", task.taskId),
		zap.String("postState", task.sm.Current()))
	return nil
}

func createTaskFromPod(app *Application, client client.KubeClient, scheduler api.SchedulerApi, pod *v1.Pod) *Task {
	task := newTask(string(pod.UID), app, client, scheduler, pod)
	return &task
}

func (task *Task) GetTaskPod() *v1.Pod {
	task.lock.RLock()
	defer task.lock.RUnlock()
	return task.pod
}

func (task *Task) GetTaskState() string {
	// fsm has its own internal lock, we don't need to hold node's lock here
	return task.sm.Current()
}

//由pod中用户指定的nodename，则不需要调度了
func (task *Task) setAllocated(nodeName string) {
	task.lock.Lock()
	defer task.lock.Unlock()
	task.nodeName = nodeName
	task.sm.SetState(events.States().Task.Allocated)
}

func (task *Task) handleFailEvent(event *fsm.Event) {
	task.lock.Lock()
	defer task.lock.Unlock()

	eventArgs := make([]string, 1)
	if err := events.GetEventArgsAsStrings(eventArgs, event.Args); err != nil {
		dispatcher.Dispatch(NewFailTaskEvent(task.applicationId, task.taskId, err.Error()))
		return
	}

	log.Logger.Error("task failed",
		zap.String("appId", task.applicationId),
		zap.String("taskId", task.taskId),
		zap.String("reason", eventArgs[0]))
}

//提交一个task去调度
func (task *Task) handleSubmitTaskEvent(event *fsm.Event) {
	log.Logger.Debug("scheduling pod",
		zap.String("podName", task.GetTaskPod().Name))
	// convert the request
	rr := common.CreateUpdateRequestForTask(task.applicationId, task.taskId, task.resource)
	log.Logger.Debug("send update request", zap.String("request", rr.String()))
	if err := task.schedulerApi.Update(&rr); err != nil {
		log.Logger.Debug("failed to send scheduling request to scheduler", zap.Error(err))
		return
	}

	//？？记录？？
	events.GetRecorder().Eventf(task.pod,
		v1.EventTypeNormal, "TaskSubmitted",
		"application \"%s\" task \"%s\" is submitted to the scheduler",
		task.applicationId, task.taskId)
}

// this is called after task reaches ALLOCATED state,
// we run this in a go routine to bind pod to the allocated node,
// if successful, we move task to next state BOUND,
// otherwise we fail the task
func (task *Task) postTaskAllocated(event *fsm.Event) {
	go func(event *fsm.Event) {
		task.lock.Lock()
		defer task.lock.Unlock()

		var errorMessage string
		eventArgs := make([]string, 2)
		if err := events.GetEventArgsAsStrings(eventArgs, event.Args); err != nil {
			errorMessage = err.Error()
			log.Logger.Error("error", zap.Error(err))
			dispatcher.Dispatch(NewFailTaskEvent(task.applicationId, task.taskId, errorMessage))
			return
		}

		allocUuid := eventArgs[0]
		nodeId := eventArgs[1]

		// task allocation UID is assigned once we get allocation decision from scheduler core
		task.allocationUuid = allocUuid

		log.Logger.Debug("bind pod",
			zap.String("podName", task.pod.Name),
			zap.String("podUID", string(task.pod.UID)))

		if err := task.kubeClient.Bind(task.pod, nodeId); err != nil {
			errorMessage = fmt.Sprintf("bind pod failed, name: %s, uid: %s, %#v",
				task.pod.Name, task.pod.UID, err)
			log.Logger.Error(errorMessage)
			dispatcher.Dispatch(NewFailTaskEvent(task.applicationId, task.taskId, errorMessage))
			events.GetRecorder().Eventf(task.pod,
				v1.EventTypeWarning, "PodBindFailure", errorMessage)
			return
		}

		log.Logger.Info("successfully bound pod", zap.String("podName", task.pod.Name))
		dispatcher.Dispatch(NewBindTaskEvent(task.applicationId, task.taskId))
		events.GetRecorder().Eventf(task.pod,
			v1.EventTypeNormal, "PodBindSuccessful",
			"pod \"%s\" successfully bound to node \"%s\"", task.pod.Name, nodeId)
	}(event)
}

func (task *Task) postTaskRejected(event *fsm.Event) {
	// currently, once task is rejected by scheduler, we directly move task to failed state.
	// so this function simply triggers the state transition when it is rejected.
	// but further, we can introduce retry mechanism if necessary.
	dispatcher.Dispatch(NewFailTaskEvent(task.applicationId, task.taskId,
		fmt.Sprintf("task %s failed because it is rejected by scheduler", task.taskId)))

	events.GetRecorder().Eventf(task.pod,
		v1.EventTypeWarning, "TaskRejected",
		"application \"%s\" task \"%s\" is rejected by the scheduler", task.applicationId, task.taskId)
}

func (task *Task) postTaskFailed(event *fsm.Event) {
	// when task is failed, we need to do the cleanup,
	// we need to release the allocation from scheduler core
	task.releaseAllocation()

	events.GetRecorder().Eventf(task.pod,
		v1.EventTypeWarning, "TaskFailed",
		"application \"%s\" task \"%s\" is failed", task.applicationId, task.taskId)
}

func (task *Task) postTaskCompleted(event *fsm.Event) {
	// when task is completed, release its allocation from scheduler core
	task.releaseAllocation()

	events.GetRecorder().Eventf(task.pod,
		v1.EventTypeNormal, "TaskCompleted",
		"application \"%s\" task \"%s\" is completed", task.applicationId, task.taskId)
}

func (task *Task) releaseAllocation() {
	// when task is completed, we notify the scheduler to release allocations
	go func() {
		// scheduler api might be nil in some tests
		if task.schedulerApi != nil {
			releaseRequest := common.CreateReleaseAllocationRequestForTask(
				task.applicationId, task.allocationUuid, task.application.partition)

			log.Logger.Debug("send release request",
				zap.String("releaseRequest", releaseRequest.String()))
			if err := task.schedulerApi.Update(&releaseRequest); err != nil {
				log.Logger.Debug("failed to send scheduling request to scheduler", zap.Error(err))
			}
		}
	}()
}
