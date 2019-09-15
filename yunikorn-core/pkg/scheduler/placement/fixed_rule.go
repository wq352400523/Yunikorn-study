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

package placement

import (
    "fmt"
    "github.com/cloudera/yunikorn-core/pkg/cache"
    "github.com/cloudera/yunikorn-core/pkg/common/configs"
    "github.com/cloudera/yunikorn-core/pkg/log"
    "go.uber.org/zap"
    "strings"
)

type fixedRule struct {
    basicRule
    queue     string
    qualified bool
}

// A rule to place an application based on the queue in the configuration.
// If the queue provided is fully qualified, starts with "root.", the parent rule is skipped and the queue is created as
// configured. If the queue is not qualified all "." characters will be replaced and the parent rule run before making
// the queue name fully qualified.
func (fr *fixedRule) getName() string {
    return "fixed"
}

func (fr *fixedRule) initialise(conf configs.PlacementRule) error {
    fr.queue = normalise(conf.Value)
    if fr.queue == "" {
        return fmt.Errorf("a fixed queue rule must have a queue name set")
    }
    fr.create = conf.Create
    fr.filter = newFilter(conf.Filter)
    // if we have a fully qualified queue name already we should not have a parent
    fr.qualified = strings.HasPrefix(fr.queue, configs.RootQueue)
    if fr.qualified && conf.Parent != nil {
        return fmt.Errorf("cannot have a fixed queue rule with qualified queue getName and a parent rule: %v", conf)
    }
    var err = error(nil)
    if conf.Parent != nil {
        fr.parent, err = newRule(*conf.Parent)
    }
    return err
}

func (fr *fixedRule) placeApplication(app *cache.ApplicationInfo, info *cache.PartitionInfo) (string, error) {
    // before anything run the filter
    if !fr.filter.allowUser(app.GetUser()) {
        log.Logger().Debug("Fixed rule filtered",
            zap.String("application", app.ApplicationId),
            zap.Any("user", app.GetUser()),
            zap.String("queueName", fr.queue))
        return "", nil
    }
    var parentName string
    var err error
    queueName := fr.queue
    // if the fixed queue is already fully qualified skip the parent check
    if !fr.qualified {
        // run the parent rule if set
        if fr.parent != nil {
            parentName, err = fr.parent.placeApplication(app, info)
            // failed parent rule, fail this rule
            if err != nil {
                return "", err
            }
            // rule did not return a parent: this could be filter or create flag related
            if parentName == "" {
                return "", nil
            }
            // check if this is a parent queue and qualify it
            if !strings.HasPrefix(parentName, configs.RootQueue + cache.DOT) {
                parentName = configs.RootQueue + cache.DOT + parentName
            }
            if info.GetQueue(parentName).IsLeafQueue() {
                return "", fmt.Errorf("parent rule returned a leaf queue: %s", parentName)
            }
        }
        // the parent is set from the rule otherwise set it to the root
        if parentName == "" {
            parentName = configs.RootQueue
        }
        queueName = parentName + cache.DOT + fr.queue
    }
    // Log the result before we really create
    log.Logger().Debug("Fixed rule intermediate result",
        zap.String("application", app.ApplicationId),
        zap.String("queue", queueName))
    // get the queue object
    queue := info.GetQueue(queueName)
    // if we cannot create the queue must exist
    if !fr.create && queue == nil {
        return "", nil
    }
    log.Logger().Info("Fixed rule application placed",
        zap.String("application", app.ApplicationId),
        zap.String("queue", queueName))
    return queueName, nil
}
