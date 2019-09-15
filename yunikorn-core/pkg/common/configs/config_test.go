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

package configs

import (
    "encoding/json"
    "fmt"
    "gopkg.in/yaml.v2"
    "io/ioutil"
    "path"
    "testing"
)

func TestConfigSerde(t *testing.T) {
    conf := SchedulerConfig{
        Partitions: []PartitionConfig{
            {
                Name: "default",
                Queues: []QueueConfig{
                    {
                        Name:   "a",
                        Parent: true,
                        Properties: map[string]string{
                            "acl": "abc",
                            "x":   "y",
                        },
                        Resources: Resources{
                            Guaranteed: map[string]string{
                                "a": "100",
                            },
                            Max: map[string]string{},
                        },
                        Queues: []QueueConfig{
                            {
                                Name:       "a1",
                                Properties: map[string]string{},
                                Resources: Resources{
                                    Guaranteed: map[string]string{
                                        "memory": "100",
                                    },
                                    Max: map[string]string{
                                        "cpu": "10",
                                    },
                                },
                                Queues: []QueueConfig{},
                            }, {
                                Name:       "a2",
                                Properties: map[string]string{},
                                Resources: Resources{
                                    Guaranteed: map[string]string{
                                        "memory": "200",
                                    },
                                    Max: map[string]string{
                                        "cpu": "20",
                                    },
                                },
                                Queues: []QueueConfig{},
                            },
                        },
                    },
                    {
                        Name:   "b",
                        Parent: true,
                        Properties: map[string]string{
                            "acl": "abc",
                            "x":   "y",
                        },
                        Resources: Resources{
                            Guaranteed: map[string]string{
                                "a": "100",
                            },
                            Max: map[string]string{},
                        },
                        Queues: []QueueConfig{
                            {
                                Name:       "b1",
                                Properties: map[string]string{},
                                Resources: Resources{
                                    Guaranteed: map[string]string{
                                        "memory": "10",
                                    },
                                    Max: map[string]string{
                                        "cpu": "1",
                                    },
                                },
                                Queues: []QueueConfig{},
                            }, {
                                Name:       "b2",
                                Properties: map[string]string{},
                                Resources: Resources{
                                    Guaranteed: map[string]string{
                                        "memory": "20",
                                    },
                                    Max: map[string]string{
                                        "cpu": "2",
                                    },
                                },
                                Queues: []QueueConfig{},
                            },
                        },
                    },
                },
            },
        },
        Checksum: []byte(""),
    }

    // convert the object to yaml
    yamlConf, err := yaml.Marshal(&conf)
    if err != nil {
        t.Errorf("error marshalling yaml config: %v", err)
    }
    t.Logf(string(yamlConf))

    // unmarshal what we have just created
    newConf := SchedulerConfig{}
    err = yaml.Unmarshal(yamlConf, &newConf)
    if err != nil {
        t.Errorf("error unmarshalling serde yaml: %v", err)
    }

    // marshal as json and we still should get the same objects
    jsonConf, err := json.Marshal(conf)
    t.Logf(string(jsonConf))
    if err != nil {
        t.Errorf("error marshalling json from config: %v", err)
    }

    jsonConf2, err := json.Marshal(newConf)
    if err != nil {
        t.Errorf("error marshalling json config from serde yaml config: %v", err)
    }

    if string(jsonConf) != string(jsonConf2) {
        t.Error("json marshaled strings differ:")
        t.Errorf("original=[%s]", jsonConf)
        t.Errorf("serde   =[%s]", jsonConf2)
    }
}

func CreateConfig(data string) (*SchedulerConfig, error) {
    dir, err := ioutil.TempDir("", "test-scheduler-config")
    if err != nil {
        return nil, fmt.Errorf("failed to create temp dir: %v", err)
    }

    err = ioutil.WriteFile(path.Join(dir, "test-scheduler-config.yaml"), []byte(data), 0644)
    if err != nil {
        return nil, fmt.Errorf("failed to write config file: %v", err)
    }
    // Read the file and build the config
    ConfigMap[SchedulerConfigPath] = dir
    return SchedulerConfigLoader("test-scheduler-config")
}

func TestLoadQueueConfig(t *testing.T) {
    data := `
partitions:
  - name: default
    queues:
      - name: root
        queues:
          - name: production
            resources:
              guaranteed:
                {memory: 1000, vcore: 10}
              max:
                {memory: 10000, vcore: 100}
          - name: test
            properties:
              something: withAvalue
            resources:
              guaranteed:
                memory: 200
                vcore: 2
              max:
                memory: 3000
                vcore: 30
          - name: sandbox
            parent: true
            submitacl: " sandbox"
            resources:
              guaranteed:
                memory: 400
                vcore: 4
              max:
                memory: 5000
                vcore: 50
    placementrules:
      - name: User
        create: true
        parent:
          name: PrimaryGroupName
          create: false
        filter:
          type: allow
          groups:
            - sandbox
      - name: Provided
        create: true
    users:
      - name: user1
        maxresources: {memory: 10000, vcore: 10}
        maxapplications: 7
      - name: user2
        maxapplications: 10
  - name: gpu
    queues:
      - name: production
        adminacl: "admin admin"
        maxapplications: 10
      - name: test
        submitacl: "*"
        resources:
          max: {memory: 1000, vcore: 10}
`

    // create the config and process it
    conf, err := CreateConfig(data)
    if err != nil {
        t.Errorf("loading failed with error: %v", err)
    }

    if conf.Partitions[0].Name != "default" {
        t.Errorf("default partition not found in config: %v", conf)
    }

    // gone through validation: both 1 top level queues
    if len(conf.Partitions[0].Queues) != 1 || len(conf.Partitions[1].Queues) != 1 {
        t.Errorf("failed to load queues from file %v", conf)
    }

    // root.sandbox queue
    if conf.Partitions[0].Queues[0].Queues[2].Name != "sandbox" {
        t.Errorf("failed to load sub queues from file %v", conf)
    }

    // root.production queue
    if conf.Partitions[0].Queues[0].Queues[0].Resources.Guaranteed["memory"] != "1000" {
        t.Errorf("failed to load guranteed resource from file %v", conf)
    }

    // root.test queue
    if conf.Partitions[0].Queues[0].Queues[1].Resources.Max["vcore"] != "30" {
        t.Errorf("failed to load max resource from file %v", conf)
    }

    // root.test queue
    if len(conf.Partitions[0].Queues[0].Queues[1].Properties) == 0 {
        t.Errorf("failed to load properties from file %v", conf)
    }

    // gpu.production
    if conf.Partitions[1].Queues[0].Queues[0].Name != "production" {
        t.Errorf("failed to load 2nd partition queues from file %v", conf)
    }

    // gpu.production
    if conf.Partitions[1].Queues[0].Queues[0].AdminACL != "admin admin" {
        t.Errorf("failed to load admin ACL from file %v", conf)
    }

    // gpu.production
    if conf.Partitions[1].Queues[0].Queues[1].SubmitACL != "*" {
        t.Errorf("failed to load submit ACL from file %v", conf)
    }
}

func TestDeepQueueHierarchy(t *testing.T) {
    data := `
partitions:
  - name: default
    queues:
      - name: root
        queues:
          - name: level1
            queues:
              - name: level2
                queues:
                  - name: level3
                    queues:
                      - name: level4
                        queues:
                          - name: level5
`
    // create the config and process it
    // validate the config and check after the update
    conf, err := CreateConfig(data)
    if err != nil {
        t.Errorf("deep queue hierarchy test should not have failed: %v", err)
    }

    data = `
partitions:
  - name: default
    queues:
      - name: root
        queues:
          - name: level1
            queues:
              - name: level2
                queues:
                  - name: level3
                    queues:
                      - name: level4
                        queues:
                          - name: $$$$
`
    // create the config and process it
    // validate the config and check after the update
    conf, err = CreateConfig(data)
    if err == nil {
        t.Errorf("deep queue hierarchy test should have failed: %v", conf)
    }
}

func TestParsePartitionFail(t *testing.T) {
    data := `
partitions:
  - name: default
`
    // create the config and process it
    // validate the config and check after the update
    conf, err := CreateConfig(data)
    if err == nil {
        t.Errorf("no queue config parsing should have failed: %v", conf)
    }

    data = `
partitions:
  - name: default
    queues:
      - name: root
  - name: default
`
    // validate the config and check after the update
    conf, err = CreateConfig(data)
    if err == nil {
        t.Errorf("multiple default partitions parsing should have failed: %v", conf)
    }
}

func TestParseQueueFail(t *testing.T) {
    data := `
partitions:
  - name: default
    queues:
      - name: root
        resources:
          max: {memory: 10000, vcore: 100}
`
    // validate the config and check after the update
    conf, err := CreateConfig(data)
    if err == nil {
        t.Errorf("resource limit on root queue parsing should have failed: %v", conf)
    }

    data = `
partitions:
  - name: default
    queues:
      - name: test
      - name: test
`
    // validate the config and check after the update
    conf, err = CreateConfig(data)
    if err == nil {
        t.Errorf("duplicate queue names parsing should have failed: %v", conf)
    }

    data = `
partitions:
  - name: default
    queues:
      - name: this-name-is-longer-than-sixtyfour-characters-and-thus-fails-parsing
`
    // validate the config and check after the update
    conf, err = CreateConfig(data)
    if err == nil {
        t.Errorf("too long queue name parsing should have failed: %v", conf)
    }

    data = `
partitions:
  - name: default
    queues:
      - name: no.in-name
`
    // validate the config and check after the update
    conf, err = CreateConfig(data)
    if err == nil {
        t.Errorf("dot in queue names parsing should have failed: %v", conf)
    }

    data = `
partitions:
  - name: default
    queues:
      - name: special-$-name
`
    // validate the config and check after the update
    conf, err = CreateConfig(data)
    if err == nil {
        t.Errorf("special char in queue names parsing should have failed: %v", conf)
    }
}

func TestParseResourceFail(t *testing.T) {
    data := `
partitions:
  - name: default
    queues:
      - name: test
        resources:
          max:
            memory: text
`
    // validate the config and check after the update
    conf, err := CreateConfig(data)
    if err == nil {
        t.Errorf("resource not a number queue parsing should have failed: %v", conf)
    }
}

func TestParseACLFail(t *testing.T) {
    data := `
partitions:
  - name: default
    queues:
      - name: root
        adminacl: "users groups something_to_fail_it"
`
    // validate the config and check after the update
    conf, err := CreateConfig(data)
    if err == nil {
        t.Errorf("3 fields in ACL queue parsing should have failed: %v", conf)
    }

    data = `
partitions:
  - name: default
    queues:
      - name: root
        adminacl: *
`
    // validate the config and check after the update
    conf, err = CreateConfig(data)
    if err == nil {
        t.Errorf("unescaped wild card ACL queue parsing should have failed: %v", conf)
    }

    data = `
partitions:
  - name: default
    queues:
      - name: root
        adminacl: "user""
`
    // validate the config and check after the update
    conf, err = CreateConfig(data)
    if err == nil {
        t.Errorf("unbalanced quotes ACL queue parsing should have failed: %v", conf)
    }
}

func TestPartitionPreemptionParameter(t *testing.T) {
    data := `
partitions:
  - name: default
    queues:
      - name: root
    preemption:
      enabled: true
  - name: "partition-0"
    queues:
      - name: root
`
    // validate the config and check after the update
    conf, err := CreateConfig(data)
    if err != nil {
        t.Errorf("should expect no error %v", err)
    }

    if !conf.Partitions[0].Preemption.Enabled {
        t.Error("default partition's preemption should be enabled.")
    }

    if conf.Partitions[1].Preemption.Enabled {
        t.Error("partition-0's preemption should NOT be enabled by default")
    }
}

func TestParseRule(t *testing.T) {
    data := `
partitions:
  - name: default
    queues:
      - name: root
    placementrules:
      - name: User
        create: true
        filter:
          type: allow
          users:
            - test1
            - test1
          groups:
            - test1
            - test1
`
    // validate the config and check after the update
    conf, err := CreateConfig(data)
    if err != nil {
        t.Errorf("rule parsing should not have failed: %v", err)
    }
    rule := conf.Partitions[0].PlacementRules[0]
    if !rule.Create {
        t.Errorf("Create flag is not set correctly expected 'true' got 'false'")
    }
    if rule.Parent != nil {
        t.Errorf("Parent rule was set incorrectly expected 'nil' got %v", rule.Parent)
    }
    if rule.Filter.Type != "allow" {
        t.Errorf("Filter type set incorrectly expected 'allow' got %v", rule.Filter.Type)
    }
    if len(rule.Filter.Groups) != 2 || len(rule.Filter.Users) != 2 {
        t.Errorf("Failed rule or group filter parsing length should have been 2 for both, got: %v, %v", rule.Filter.Users, rule.Filter.Groups)
    }

    data = `
partitions:
  - name: default
    queues:
      - name: root
    placementrules:
      - name: User
        create: false
        parent:
          name: PrimaryGroup
          create: false
          filter:
            type: allow
            users:
              - test1
`
    // validate the config and check after the update
    conf, err = CreateConfig(data)
    if err != nil {
        t.Errorf("rule parsing should not have failed: %v", err)
    }
    rule = conf.Partitions[0].PlacementRules[0]
    if rule.Create {
        t.Errorf("Create flag is not set correctly expected 'false' got 'true'")
    }
    if rule.Parent == nil {
        t.Errorf("Parent rule was set incorrectly expected 'rule ' got nil")
    }
    if rule.Parent.Create {
        t.Errorf("Create flag is not set correctly expected 'false' got 'true'")
    }
    if len(rule.Parent.Filter.Users) != 1 && len(rule.Parent.Filter.Groups) != 0 {
        t.Errorf("Failed rule or group filter parsing length should have been 1 and 0, got: %v, %v", rule.Filter.Users, rule.Filter.Groups)
    }

    data = `
partitions:
  - name: default
    queues:
      - name: root
    placementrules:
      - name: User
        filter:
          users:
            - test.test
      - name: PrimaryGroup
        filter:
          users:
            - test1*
      - name: Something
        filter:
          users:
            - test[1-9]
`
    // validate the config and check after the update
    conf, err = CreateConfig(data)
    if err != nil {
        t.Errorf("rule parsing should not have failed: %v", err)
    }
    if len(conf.Partitions[0].PlacementRules) != 3 {
        t.Errorf("incorrect number of rules returned expected 3 got: %d", len(conf.Partitions[0].PlacementRules))
    }

    data = `
partitions:
  - name: default
    queues:
      - name: root
    placementrules:
      - name: fixed
        value: default
      - name: tag
        value: Just Any value
`
    // validate the config and check after the update
    conf, err = CreateConfig(data)
    if err != nil {
        t.Errorf("rule parsing should not have failed: %v", err)
    }
    if len(conf.Partitions[0].PlacementRules) != 2 {
        t.Errorf("incorrect number of rules returned expected 2 got: %d", len(conf.Partitions[0].PlacementRules))
    }
    if conf.Partitions[0].PlacementRules[0].Value != "default" &&
        conf.Partitions[0].PlacementRules[1].Value != "Just Any value" {
        t.Errorf("incorrect values set inside the rules: %v", conf.Partitions[0].PlacementRules)
    }
}

func TestParseRuleFail(t *testing.T) {
    data := `
partitions:
  - name: default
    queues:
      - name: root
    placementrules:
      - name: User
        create: bogus
`
    // validate the config and check after the update
    conf, err := CreateConfig(data)
    if err == nil {
        t.Errorf("not a boolean create flag rule parsing should have failed: %v", conf)
    }

    data = `
partitions:
  - name: default
    queues:
      - name: root
    placementrules:
      - name: User
        filter:
          type: bogus
`
    // validate the config and check after the update
    conf, err = CreateConfig(data)
    if err == nil {
        t.Errorf("filter type parsing should have failed rule parsing: %v", conf)
    }

    data = `
partitions:
  - name: default
    queues:
      - name: root
    placementrules:
      - name: User
        filter:
          users: 
            - 99test
`
    // validate the config and check after the update
    conf, err = CreateConfig(data)
    if err == nil {
        t.Errorf("user parsing filter should have failed rule parsing: %v", conf)
    }

    data = `
partitions:
  - name: default
    queues:
      - name: root
    placementrules:
      - name: User
        filter:
          groups:
            - test@group
`
    // validate the config and check after the update
    conf, err = CreateConfig(data)
    if err == nil {
        t.Errorf("group parsing filter should have failed rule parsing: %v", conf)
    }

    data = `
partitions:
  - name: default
    queues:
      - name: root
    placementrules:
      - name: User
        filter:
          users:
            - test[test
`
    // validate the config and check after the update
    conf, err = CreateConfig(data)
    if err == nil {
        t.Errorf("user parsing filter should have failed rule parsing: %v", conf)
    }
}

func TestRecurseParent(t *testing.T) {
    data := `
partitions:
  - name: default
    queues:
      - name: root
    placementrules:
      - name: user
        parent:
          name: fixed
          parent:
            name: provided
`
    // validate the config and check after the update
    conf, err := CreateConfig(data)
    if err != nil {
        t.Errorf("recursive parent rule parsing should not have failed: %v", conf)
    }
}