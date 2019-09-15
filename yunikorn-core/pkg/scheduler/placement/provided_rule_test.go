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
    "github.com/cloudera/yunikorn-core/pkg/cache"
    "github.com/cloudera/yunikorn-core/pkg/common/configs"
    "github.com/cloudera/yunikorn-core/pkg/common/security"
    "testing"
)

func TestProvidedRulePlace(t *testing.T) {
    // Create the structure for the test
    data := `
partitions:
  - name: default
    queues:
      - name: testparent
        queues:
          - name: testchild
`
    partInfo, err := CreatePartitionInfo([]byte(data))
    tags := make(map[string]string, 0)
    user := security.UserGroup{
        User: "test",
        Groups: []string{},
    }

    conf := configs.PlacementRule{
        Name: "provided",
    }
    rule, err := newRule(conf)
    if err != nil || rule == nil {
        t.Errorf("provided rule create failed, err %v", err)
    }
    // queue that does not exists directly under the root
    appInfo := cache.NewApplicationInfo("app1", "default", "unkonwn", user, tags)
    queue, err := rule.placeApplication(appInfo, partInfo)
    if queue != "" || err != nil {
        t.Errorf("provided rule placed app in incorrect queue '%s', err %v", queue, err)
    }
    // trying to place in a qualified queue that does not exist
    appInfo = cache.NewApplicationInfo("app1", "default", "root.unknown", user, tags)
    queue, err = rule.placeApplication(appInfo, partInfo)
    if queue != "" || err != nil {
        t.Errorf("provided rule placed app in incorrect queue '%s', error %v", queue, err)
    }
    // same queue now with create flag
    conf = configs.PlacementRule{
        Name: "provided",
        Create: true,
    }
    rule, err = newRule(conf)
    if err != nil || rule == nil {
        t.Errorf("provided rule create failed, err %v", err)
    }
    queue, err = rule.placeApplication(appInfo, partInfo)
    if queue != "root.unknown" || err != nil {
        t.Errorf("provided rule placed app in incorrect queue '%s', error %v", queue, err)
    }

    conf = configs.PlacementRule{
        Name: "provided",
        Parent: &configs.PlacementRule{
            Name: "fixed",
            Value: "testparent",
        },
    }
    rule, err = newRule(conf)
    if err != nil || rule == nil {
        t.Errorf("provided rule create failed with parent name, err %v", err)
    }

    // unqualified queue with parent rule that exists directly in hierarchy
    appInfo = cache.NewApplicationInfo("app1", "default", "testchild", user, tags)
    queue, err = rule.placeApplication(appInfo, partInfo)
    if queue != "root.testparent.testchild" || err != nil {
        t.Errorf("provided rule failed to place queue in correct queue '%s', err %v", queue, err)
    }

    // qualified queue with parent rule (parent rule ignored)
    appInfo = cache.NewApplicationInfo("app1", "default", "root.testparent", user, tags)

    queue, err = rule.placeApplication(appInfo, partInfo)
    if queue != "root.testparent" || err != nil {
        t.Errorf("provided rule placed in to be created queue with create false '%s', err %v", queue, err)
    }
}