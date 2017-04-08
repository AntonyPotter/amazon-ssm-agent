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

package runscript

import (
	"bytes"
	"fmt"
	"io"
	"path/filepath"
	"testing"

	"github.com/aws/amazon-ssm-agent/agent/context"
	"github.com/aws/amazon-ssm-agent/agent/contracts"
	"github.com/aws/amazon-ssm-agent/agent/executers"
	"github.com/aws/amazon-ssm-agent/agent/fileutil"
	"github.com/aws/amazon-ssm-agent/agent/framework/runpluginutil"
	"github.com/aws/amazon-ssm-agent/agent/jsonutil"
	"github.com/aws/amazon-ssm-agent/agent/log"
	"github.com/aws/amazon-ssm-agent/agent/plugins/pluginutil"
	"github.com/aws/amazon-ssm-agent/agent/task"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/twinj/uuid"
)

type TestCase struct {
	Input            RunScriptPluginInput
	Output           contracts.PluginOutput
	ExecuterStdOut   string
	ExecuterStdErr   string
	ExecuterErrors   []error
	MessageID        string
	AdditionalInputs []RunScriptPluginInput
}

type CommandTester func(p *Plugin, mockCancelFlag *task.MockCancelFlag, mockExecuter *executers.MockCommandExecuter, mockS3Uploader *pluginutil.MockDefaultPlugin)

type PropertyBuilder func(t *testing.T, testCase TestCase) interface{}

const (
	orchestrationDirectory = "OrchesDir"
	s3BucketName           = "bucket"
	s3KeyPrefix            = "key"
	pluginID               = "aws:runScript1"
	testInstanceID         = "i-12345678"
	bucketRegionErrorMsg   = "AuthorizationHeaderMalformed: The authorization header is malformed; the region 'us-east-1' is wrong; expecting 'us-west-2' status code: 400, request id: []"
)

var TestCases = []TestCase{
	generateTestCaseOk("0"),
	generateTestCaseOk("1"),
	generateTestCaseFail("2"),
	generateTestCaseFail("3"),
}

var MultiInputTestCases = []TestCase{
	generateTestCaseMultipleInputsOk([]string{"0", "1"}),
}

var logger = log.NewMockLog()

func generateTestCaseOk(id string) TestCase {
	return generateTestCaseMultipleInputsOk([]string{id})
}

func generateTestCaseMultipleInputsOk(ids []string) TestCase {
	var firstInput RunScriptPluginInput
	var firstID string
	additionalInputs := []RunScriptPluginInput{}
	for index, id := range ids {
		input := RunScriptPluginInput{
			RunCommand:       []string{"echo " + id},
			ID:               id + ".aws:runScript",
			WorkingDirectory: "Dir" + id,
			TimeoutSeconds:   "1",
		}
		if index == 0 {
			firstInput = input
			firstID = id
		} else {
			additionalInputs = append(additionalInputs, input)
		}
	}
	testCase := TestCase{
		Input:            firstInput,
		Output:           contracts.PluginOutput{},
		AdditionalInputs: additionalInputs,
	}

	testCase.Output.Stdout = "standard output of test case " + firstID
	testCase.ExecuterStdOut = testCase.Output.Stdout
	testCase.Output.Stderr = "standard error of test case " + firstID
	testCase.ExecuterStdErr = testCase.Output.Stderr
	testCase.Output.ExitCode = 0
	testCase.Output.Status = "Success"

	return testCase
}

func combinedErrorOutput(stderr string, errs []error) string {
	var expectedStdErr string
	for _, err := range errs {
		if len(expectedStdErr) > 0 {
			expectedStdErr = fmt.Sprintf("%v\nfailed to run commands: %v", expectedStdErr, err)
		} else {
			expectedStdErr = fmt.Sprintf("failed to run commands: %v", err)
		}
	}
	if len(expectedStdErr) > 0 {
		expectedStdErr = fmt.Sprintf("%v\n%v", expectedStdErr, stderr)
	} else {
		expectedStdErr = stderr
	}
	return expectedStdErr
}

func generateTestCaseFail(id string) TestCase {
	t := generateTestCaseOk(id)
	t.ExecuterErrors = []error{fmt.Errorf("Error happened for cmd %v", id)}
	t.Output.Stderr = combinedErrorOutput(t.ExecuterStdErr, t.ExecuterErrors)
	t.Output.ExitCode = 1
	t.Output.Status = "Failed"
	return t
}

// TestRunScripts tests the runScripts and runScriptsRawInput methods, which run one set of commands.
func TestRunScripts(t *testing.T) {
	for _, testCase := range TestCases {
		testRunScripts(t, testCase, true)
		testRunScripts(t, testCase, false)
	}
}

// testRunScripts tests the runScripts or the runScriptsRawInput method for one testcase.
func testRunScripts(t *testing.T, testCase TestCase, rawInput bool) {
	logger.On("Error", mock.Anything).Return(nil)
	logger.Infof("test run commands %v", testCase)
	runScriptTester := func(p *Plugin, mockCancelFlag *task.MockCancelFlag, mockExecuter *executers.MockCommandExecuter, mockS3Uploader *pluginutil.MockDefaultPlugin) {
		// set expectations
		setExecuterExpectations(mockExecuter, testCase, mockCancelFlag, p)
		setS3UploaderExpectations(mockS3Uploader, testCase, p)

		// call method under test
		var res contracts.PluginOutput
		if rawInput {
			// prepare plugin input
			var rawPluginInput interface{}
			err := jsonutil.Remarshal(testCase.Input, &rawPluginInput)
			assert.Nil(t, err)

			res = p.runCommandsRawInput(logger, pluginID, rawPluginInput, orchestrationDirectory, mockCancelFlag, s3BucketName, s3KeyPrefix)
		} else {
			res = p.runCommands(logger, pluginID, testCase.Input, orchestrationDirectory, mockCancelFlag, s3BucketName, s3KeyPrefix)
		}

		// assert output is correct (mocked object expectations are tested automatically by testExecution)
		assert.Equal(t, testCase.Output, res)
	}

	testExecution(t, runScriptTester)
}

// TestBucketsInDifferentRegions tests runScripts when S3Buckets are present in IAD and PDX region.
func TestBucketsInDifferentRegions(t *testing.T) {
	for _, testCase := range TestCases {
		testBucketsInDifferentRegions(t, testCase, true)
		testBucketsInDifferentRegions(t, testCase, false)
	}
}

// testBucketsInDifferentRegions tests the runScripts with S3 buckets present in IAD and PDX region.
func testBucketsInDifferentRegions(t *testing.T, testCase TestCase, testingBucketsInDifferentRegions bool) {
	logger.On("Error", mock.Anything).Return(nil)
	logger.Infof("test run commands %v", testCase)
	runScriptTester := func(p *Plugin, mockCancelFlag *task.MockCancelFlag, mockExecuter *executers.MockCommandExecuter, mockS3Uploader *pluginutil.MockDefaultPlugin) {
		// set expectations
		setExecuterExpectations(mockExecuter, testCase, mockCancelFlag, p)
		setS3UploaderExpectations(mockS3Uploader, testCase, p)

		// call method under test
		var res contracts.PluginOutput
		res = p.runCommands(logger, pluginID, testCase.Input, orchestrationDirectory, mockCancelFlag, s3BucketName, s3KeyPrefix)

		// assert output is correct (mocked object expectations are tested automatically by testExecution)
		assert.Equal(t, testCase.Output, res)
	}

	testExecution(t, runScriptTester)
}

// TestExecute tests the Execute method, which runs multiple sets of commands.
func TestExecute(t *testing.T) {
	// test each plugin input as a separate execution
	for _, testCase := range TestCases {
		testExecute(t, testCase, arrayPropertyBuilder)
		testExecute(t, testCase, singleValuePropertyBuilder)
	}
	for _, testCase := range MultiInputTestCases {
		testExecute(t, testCase, arrayPropertyBuilder)
	}
}

func arrayPropertyBuilder(t *testing.T, testCase TestCase) interface{} {
	var pluginProperties []interface{}

	// prepare plugin input
	var rawPluginInput interface{}
	err := jsonutil.Remarshal(testCase.Input, &rawPluginInput)
	assert.Nil(t, err)
	// prepare plugin input
	for _, input := range testCase.AdditionalInputs {
		var rawPluginInput interface{}
		err := jsonutil.Remarshal(input, &rawPluginInput)
		assert.Nil(t, err)
		pluginProperties = append(pluginProperties, rawPluginInput)
	}

	pluginProperties = append(pluginProperties, rawPluginInput)
	return pluginProperties
}

func singleValuePropertyBuilder(t *testing.T, testCase TestCase) interface{} {
	// prepare plugin input
	var rawPluginInput interface{}
	err := jsonutil.Remarshal(testCase.Input, &rawPluginInput)
	assert.Nil(t, err)

	return rawPluginInput
}

// testExecute tests the run command plugin's Execute method.
func testExecute(t *testing.T, testCase TestCase, propBuilder PropertyBuilder) {
	executeTester := func(p *Plugin, mockCancelFlag *task.MockCancelFlag, mockExecuter *executers.MockCommandExecuter, mockS3Uploader *pluginutil.MockDefaultPlugin) {
		// setup expectations and correct outputs
		var correctOutputs string
		mockContext := context.NewMockDefault()

		// set expectations
		setCancelFlagExpectations(mockCancelFlag, testCase)
		setExecuterExpectations(mockExecuter, testCase, mockCancelFlag, p)
		setS3UploaderExpectations(mockS3Uploader, testCase, p)

		// prepare plugin input
		pluginProperties := propBuilder(t, testCase)
		correctOutputs = testCase.Output.String()

		//Create messageId which is in the format of aws.ssm.<commandID>.<InstanceID>
		commandID := uuid.NewV4().String()

		// call plugin
		res := p.Execute(
			mockContext,
			contracts.Configuration{
				Properties:             pluginProperties,
				OutputS3BucketName:     s3BucketName,
				OutputS3KeyPrefix:      s3KeyPrefix,
				OrchestrationDirectory: orchestrationDirectory,
				BookKeepingFileName:    commandID,
				PluginID:               pluginID,
			}, mockCancelFlag, runpluginutil.PluginRunner{})

		// assert output is correct (mocked object expectations are tested automatically by testExecution)
		assert.NotNil(t, res.StartDateTime)
		assert.NotNil(t, res.EndDateTime)
		assert.Equal(t, correctOutputs, res.Output)

		// TODO:MF: Re-enable these checks when we're using PluginResult.StandardOutput and StandardError and are setting them
		//assert.NotNil(t, res.StandardError)
		//assert.Equal(t, testCase.Output.Stderr, res.StandardError)
		//assert.NotNil(t, res.StandardOutput)
		//assert.Equal(t, testCase.Output.Stdout, res.StandardOutput)
	}

	testExecution(t, executeTester)
}

// testExecution sets up boiler plate mocked objects then delegates to a more
// specific tester, then asserts general expectations on the mocked objects.
// It is the responsibility of the inner tester to set up expectations
// and assert specific result conditions.
func testExecution(t *testing.T, commandtester CommandTester) {
	// create mocked objects
	mockCancelFlag := new(task.MockCancelFlag)
	mockExecuter := new(executers.MockCommandExecuter)
	mockS3Uploader := new(pluginutil.MockDefaultPlugin)

	// create plugin
	p := new(Plugin)
	p.StdoutFileName = "stdout"
	p.StderrFileName = "stderr"
	p.MaxStdoutLength = 1000
	p.MaxStderrLength = 1000
	p.OutputTruncatedSuffix = "-more-"
	p.UploadToS3Sync = true
	p.CommandExecuter = mockExecuter
	p.ExecuteUploadOutputToS3Bucket = pluginutil.UploadOutputToS3BucketExecuter(mockS3Uploader.UploadOutputToS3Bucket)
	p.Name = "aws:runShellScript"
	p.ScriptName = "_script.sh"
	p.ShellCommand = "sh"
	p.ShellArguments = []string{"-c"}

	// run inner command tester
	commandtester(p, mockCancelFlag, mockExecuter, mockS3Uploader)

	// assert that the expectations were met
	mockExecuter.AssertExpectations(t)
	mockCancelFlag.AssertExpectations(t)
	mockS3Uploader.AssertExpectations(t)
}

func setExecuterExpectations(mockExecuter *executers.MockCommandExecuter, t TestCase, cancelFlag task.CancelFlag, p *Plugin) {
	inputs := append(t.AdditionalInputs, t.Input)
	for _, input := range inputs {
		orchestrationDir := fileutil.BuildPath(orchestrationDirectory, input.ID)
		stdoutFilePath := filepath.Join(orchestrationDir, p.StdoutFileName)
		stderrFilePath := filepath.Join(orchestrationDir, p.StderrFileName)
		mockExecuter.On("Execute", mock.Anything, input.WorkingDirectory, stdoutFilePath, stderrFilePath, cancelFlag, mock.Anything, mock.Anything, mock.Anything).Return(
			readerFromString(t.ExecuterStdOut), readerFromString(t.ExecuterStdErr), t.Output.ExitCode, t.ExecuterErrors)
	}
}

func setS3UploaderExpectations(mockS3Uploader *pluginutil.MockDefaultPlugin, t TestCase, p *Plugin) {
	var emptyArray []string
	inputs := append(t.AdditionalInputs, t.Input)
	for _, input := range inputs {
		orchestrationDir := fileutil.BuildPath(orchestrationDirectory, input.ID)
		s3PluginID := input.ID
		if s3PluginID == "" {
			s3PluginID = pluginID
		}
		mockS3Uploader.On("UploadOutputToS3Bucket", mock.Anything, s3PluginID, orchestrationDir, s3BucketName, s3KeyPrefix, false, "", t.Output.Stdout, t.Output.Stderr).Return(emptyArray)
	}
}

func setCancelFlagExpectations(mockCancelFlag *task.MockCancelFlag, t TestCase) {
	times := 1 + len(t.AdditionalInputs)
	mockCancelFlag.On("Canceled").Return(false).Times(times)
	mockCancelFlag.On("ShutDown").Return(false).Times(times)
}

func readerFromString(s string) io.Reader {
	return bytes.NewReader([]byte(s))
}
