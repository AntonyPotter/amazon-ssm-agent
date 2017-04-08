// Copyright 2016 Amazon.com, Inc. or its affiliates. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License"). You may not
// use this file except in compliance with the License. A copy of the
// License is located at
//
// http://aws.amazon.com/apache2.0/
//
// or in the "license" file accompanying this file. This file is distributed
// on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND,
// either express or implied. See the License for the specific language governing
// permissions and limitations under the License.

// Package cloudwatch implements cloudwatch plugin and its configuration
package cloudwatch

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"github.com/aws/amazon-ssm-agent/agent/appconfig"
	"github.com/aws/amazon-ssm-agent/agent/fileutil"
	"github.com/aws/amazon-ssm-agent/agent/jsonutil"
	"github.com/aws/amazon-ssm-agent/agent/log"
)

// ConfigFileName represents the name of the configuration file for cloud watch plugin
const (
	ConfigFileName       = "AWS.EC2.Windows.CloudWatch.json"
	ConfigFileFolderName = "awsCloudWatch"
)

// cloudWatchConfig represents the data structure of cloudwatch configuration singleton,
// which contains the essential information to configure cloudwatch plugin
type CloudWatchConfig struct {
	IsEnabled           bool        `json:"IsEnabled"`
	EngineConfiguration interface{} `json:"EngineConfiguration"`
}

var instance *CloudWatchConfig
var once sync.Once
var lock sync.RWMutex

// Initialze ensures the instance has been initialized
func Initialze() {
	once.Do(func() {
		instance = &CloudWatchConfig{}
	})
}

// Instance returns a singleton of CloudWatchConfig instance
func Instance() *CloudWatchConfig {
	return instance
}

// ParseEngineConfiguration marshals the EngineConfiguration from interface{} to string
func ParseEngineConfiguration() (config string, err error) {
	config, err = jsonutil.Marshal(instance.EngineConfiguration)

	return buildFullConfiguration(config), err
}

// Update updates configuration from file system
func Update(log log.T) error {
	var cwConfig CloudWatchConfig
	var err error
	if cwConfig, err = load(log); err != nil {
		return err
	}

	instance.IsEnabled = cwConfig.IsEnabled
	instance.EngineConfiguration = cwConfig.EngineConfiguration

	return err
}

// Write writes the updated configuration of cloud watch to file system
func Write() error {
	lock.Lock()
	defer lock.Unlock()
	fileName := getFileName()
	location := getLocation()
	var err error
	var content string

	content, err = jsonutil.MarshalIndent(instance)
	if err != nil {
		return err
	}

	//verify if parent folder exist
	if !fileUtilWrapper.Exists(location) {
		if err = fileUtilWrapper.MakeDirs(location); err != nil {
			return err
		}
	}

	//it's fine even if we overwrite the content of previous file
	if _, err = fileUtilWrapper.WriteIntoFileWithPermissions(
		fileName,
		content,
		os.FileMode(int(appconfig.ReadWriteAccess))); err != nil {
		return err
	}

	return nil
}

// Enable changes the IsEnabled property in cloud watch config from false to true
func Enable(config *CloudWatchConfig) error {
	var tempConfig CloudWatchConfig
	if err := jsonutil.Remarshal(config.EngineConfiguration, &tempConfig); err != nil {
		errorString := fmt.Errorf("Cannot remarmal cloudwatch configuration format %v;\nerror %v",
			config.EngineConfiguration, err)
		return errorString
	}

	instance.IsEnabled = true
	instance.EngineConfiguration = tempConfig.EngineConfiguration
	return Write()
}

// Disable changes the IsEnabled property in cloud watch config from true to false
func Disable() error {
	instance.IsEnabled = false
	return Write()
}

// load reads cloud watch plugin configuration from config store (file system)
func load(log log.T) (CloudWatchConfig, error) {
	lock.RLock()
	defer lock.RUnlock()
	fileName := getFileName()
	var err error
	var cwConfig CloudWatchConfig

	err = jsonutil.UnmarshalFile(fileName, &cwConfig)

	// For backward compatibility, check if the engine configuration is read as string due to escaped characters.
	// If so, unmarshalling it again should correct the format to a tree of maps.
	switch cwConfig.EngineConfiguration.(type) {
	case string:
		log.Info("Legacy configuration was detected - Reformatting the configuration...")
		var engineConfiguration interface{}
		rawIn := json.RawMessage(cwConfig.EngineConfiguration.(string))
		json.Unmarshal([]byte(rawIn), &engineConfiguration)
		cwConfig.EngineConfiguration = engineConfiguration
	}

	// For backward compatibility, check if the engine configuration contains
	// another engine configuration parameter. If so, remove it from the map.
	switch cwConfig.EngineConfiguration.(type) {
	case map[string]interface{}:
		if ecMap, ok := cwConfig.EngineConfiguration.(map[string]interface{}); ok {
			if val, exist := ecMap["EngineConfiguration"]; exist {
				log.Info("Legacy configuration was detected - Removing a redundant parameter...")
				cwConfig.EngineConfiguration = val
			}
		}
	}

	return cwConfig, err
}

// getFileName returns the full name of the cloud watch config file.
func getFileName() string {
	return fileutil.BuildPath(appconfig.DefaultPluginPath, ConfigFileFolderName, ConfigFileName)
}

// getLocation returns the absolute path of the cloud watch config file folder.
func getLocation() string {
	return fileutil.BuildPath(appconfig.DefaultPluginPath, ConfigFileFolderName)
}

// buildFullConfiguration returns the complete cloud watch configuration for cloudwatch plugin.
func buildFullConfiguration(config string) string {
	// Put the "EngineConfiguration" back manually to pass to cloud watch exe
	var buffer bytes.Buffer
	buffer.WriteString("{\"EngineConfiguration\":")
	buffer.WriteString(config)
	buffer.WriteString("}")
	configuration := buffer.String()
	return configuration
}
