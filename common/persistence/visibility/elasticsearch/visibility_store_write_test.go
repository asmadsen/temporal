// The MIT License
//
// Copyright (c) 2020 Temporal Technologies Inc.  All rights reserved.
//
// Copyright (c) 2020 Uber Technologies, Inc.
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in
// all copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
// THE SOFTWARE.

package elasticsearch

import (
	"fmt"
	"time"

	"github.com/golang/mock/gomock"
	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/server/common/persistence/visibility"
	esclient "go.temporal.io/server/common/persistence/visibility/elasticsearch/client"

	"go.temporal.io/server/common/payload"
	"go.temporal.io/server/common/persistence"
	"go.temporal.io/server/common/searchattribute"
)

func (s *ESVisibilitySuite) TestRecordWorkflowExecutionStarted() {
	// test non-empty request fields match
	request := &visibility.InternalRecordWorkflowExecutionStartedRequest{
		InternalVisibilityRequestBase: &visibility.InternalVisibilityRequestBase{
			NamespaceID:      "namespaceID",
			WorkflowID:       "wid",
			RunID:            "rid",
			WorkflowTypeName: "wfType",
			StartTime:        time.Unix(0, 123).UTC(),
			ExecutionTime:    time.Unix(0, 321).UTC(),
			TaskID:           int64(111),
			ShardID:          2208,
			Memo:             persistence.NewDataBlob([]byte("test bytes"), enumspb.ENCODING_TYPE_PROTO3.String()),
			Status:           enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING,
			TaskQueue:        "task-queue-name",
			SearchAttributes: &commonpb.SearchAttributes{
				IndexedFields: map[string]*commonpb.Payload{
					"CustomStringField": payload.EncodeString("alex"),
				},
			},
		},
	}

	s.mockProcessor.EXPECT().Add(gomock.Any(), gomock.Any()).
		DoAndReturn(func(bulkRequest *esclient.BulkableRequest, visibilityTaskKey string) <-chan bool {
			s.Equal("2208~111", visibilityTaskKey)

			body := bulkRequest.Doc

			s.Equal(request.NamespaceID, body[searchattribute.NamespaceID])
			s.Equal(request.WorkflowID, body[searchattribute.WorkflowID])
			s.Equal(request.RunID, body[searchattribute.RunID])
			s.Equal(request.WorkflowTypeName, body[searchattribute.WorkflowType])
			s.EqualValues(request.StartTime, body[searchattribute.StartTime])
			s.EqualValues(request.ExecutionTime, body[searchattribute.ExecutionTime])
			s.Equal(request.TaskQueue, body[searchattribute.TaskQueue])
			s.EqualValues(request.Status.String(), body[searchattribute.ExecutionStatus])

			s.Equal(request.Memo.Data, body[searchattribute.Memo])
			s.Equal(enumspb.ENCODING_TYPE_PROTO3.String(), body[searchattribute.MemoEncoding])

			customStringField := body["CustomStringField"].(string)
			// %q because request has JSON encoded string.
			s.EqualValues(request.SearchAttributes.GetIndexedFields()["CustomStringField"].Data, fmt.Sprintf("%q", customStringField))

			s.Equal(esclient.BulkableRequestTypeIndex, bulkRequest.RequestType)
			s.EqualValues(request.TaskID, bulkRequest.Version)
			s.Equal("wid~rid", bulkRequest.ID)
			s.Equal("test-index", bulkRequest.Index)

			ackCh := make(chan bool, 1)
			ackCh <- true
			return ackCh
		})

	err := s.visibilityStore.RecordWorkflowExecutionStarted(request)
	s.NoError(err)
}

func (s *ESVisibilitySuite) TestRecordWorkflowExecutionStarted_EmptyRequest() {
	// test empty request
	request := &visibility.InternalRecordWorkflowExecutionStartedRequest{
		InternalVisibilityRequestBase: &visibility.InternalVisibilityRequestBase{
			Memo: &commonpb.DataBlob{},
		},
	}

	s.mockProcessor.EXPECT().Add(gomock.Any(), gomock.Any()).
		DoAndReturn(func(bulkRequest *esclient.BulkableRequest, visibilityTaskKey string) <-chan bool {
			s.Equal("0~0", visibilityTaskKey)

			body := bulkRequest.Doc

			_, ok := body[searchattribute.Memo]
			s.False(ok)
			_, ok = body[searchattribute.MemoEncoding]
			s.False(ok)

			s.Equal(esclient.BulkableRequestTypeIndex, bulkRequest.RequestType)
			s.EqualValues(request.TaskID, bulkRequest.Version)
			s.Equal("~", bulkRequest.ID)
			s.Equal("test-index", bulkRequest.Index)

			ackCh := make(chan bool, 1)
			ackCh <- true
			return ackCh
		})

	err := s.visibilityStore.RecordWorkflowExecutionStarted(request)
	s.NoError(err)
}

func (s *ESVisibilitySuite) TestRecordWorkflowExecutionClosed() {
	// test non-empty request fields match
	request := &visibility.InternalRecordWorkflowExecutionClosedRequest{
		InternalVisibilityRequestBase: &visibility.InternalVisibilityRequestBase{
			NamespaceID:      "namespaceID",
			WorkflowID:       "wid",
			RunID:            "rid",
			WorkflowTypeName: "wfType",
			StartTime:        time.Date(2020, 8, 2, 1, 2, 3, 4, time.UTC),
			ExecutionTime:    time.Date(2020, 8, 2, 2, 2, 3, 4, time.UTC),
			TaskID:           int64(111),
			ShardID:          2208,
			Memo:             persistence.NewDataBlob([]byte("test bytes"), enumspb.ENCODING_TYPE_PROTO3.String()),
			Status:           enumspb.WORKFLOW_EXECUTION_STATUS_TERMINATED,
			TaskQueue:        "task-queue-name",
			SearchAttributes: &commonpb.SearchAttributes{
				IndexedFields: map[string]*commonpb.Payload{
					"CustomStringField": payload.EncodeString("alex"),
				},
			},
		},
		CloseTime:     time.Unix(0, 1978).UTC(),
		HistoryLength: int64(20),
	}

	s.mockProcessor.EXPECT().Add(gomock.Any(), gomock.Any()).
		DoAndReturn(func(bulkRequest *esclient.BulkableRequest, visibilityTaskKey string) <-chan bool {
			s.Equal("2208~111", visibilityTaskKey)

			body := bulkRequest.Doc

			s.Equal(request.NamespaceID, body[searchattribute.NamespaceID])
			s.Equal(request.WorkflowID, body[searchattribute.WorkflowID])
			s.Equal(request.RunID, body[searchattribute.RunID])
			s.Equal(request.WorkflowTypeName, body[searchattribute.WorkflowType])
			s.EqualValues(request.StartTime, body[searchattribute.StartTime])
			s.EqualValues(request.ExecutionTime, body[searchattribute.ExecutionTime])
			s.Equal(request.Memo.Data, body[searchattribute.Memo])
			s.Equal(enumspb.ENCODING_TYPE_PROTO3.String(), body[searchattribute.MemoEncoding])
			s.EqualValues(request.CloseTime, body[searchattribute.CloseTime])
			s.EqualValues(request.Status.String(), body[searchattribute.ExecutionStatus])
			s.EqualValues(request.HistoryLength, body[searchattribute.HistoryLength])

			s.Equal(esclient.BulkableRequestTypeIndex, bulkRequest.RequestType)
			s.EqualValues(request.TaskID, bulkRequest.Version)
			s.Equal("wid~rid", bulkRequest.ID)
			s.Equal("test-index", bulkRequest.Index)

			ackCh := make(chan bool, 1)
			ackCh <- true
			return ackCh
		})

	err := s.visibilityStore.RecordWorkflowExecutionClosed(request)
	s.NoError(err)
}

func (s *ESVisibilitySuite) TestRecordWorkflowExecutionClosed_EmptyRequest() {
	// test empty request
	request := &visibility.InternalRecordWorkflowExecutionClosedRequest{
		InternalVisibilityRequestBase: &visibility.InternalVisibilityRequestBase{
			Memo: &commonpb.DataBlob{},
		},
	}

	s.mockProcessor.EXPECT().Add(gomock.Any(), gomock.Any()).
		DoAndReturn(func(bulkRequest *esclient.BulkableRequest, visibilityTaskKey string) <-chan bool {
			s.Equal("0~0", visibilityTaskKey)

			body := bulkRequest.Doc

			_, ok := body[searchattribute.Memo]
			s.False(ok)
			_, ok = body[searchattribute.MemoEncoding]
			s.False(ok)

			s.Equal(esclient.BulkableRequestTypeIndex, bulkRequest.RequestType)
			s.EqualValues(request.TaskID, bulkRequest.Version)
			s.Equal("~", bulkRequest.ID)
			s.Equal("test-index", bulkRequest.Index)

			ackCh := make(chan bool, 1)
			ackCh <- true
			return ackCh
		})

	err := s.visibilityStore.RecordWorkflowExecutionClosed(request)
	s.NoError(err)
}

func (s *ESVisibilitySuite) TestDeleteExecution() {
	// test non-empty request fields match
	request := &visibility.VisibilityDeleteWorkflowExecutionRequest{
		NamespaceID: "namespaceID",
		RunID:       "rid",
		WorkflowID:  "wid",
		TaskID:      int64(111),
	}

	s.mockProcessor.EXPECT().Add(gomock.Any(), gomock.Any()).
		DoAndReturn(func(bulkRequest *esclient.BulkableRequest, visibilityTaskKey string) <-chan bool {
			s.Equal("wid~rid", visibilityTaskKey)

			s.Equal(esclient.BulkableRequestTypeDelete, bulkRequest.RequestType)
			s.EqualValues(request.TaskID, bulkRequest.Version)
			s.Equal("wid~rid", bulkRequest.ID)
			s.Equal("test-index", bulkRequest.Index)

			ackCh := make(chan bool, 1)
			ackCh <- true
			return ackCh
		})

	err := s.visibilityStore.DeleteWorkflowExecution(request)
	s.NoError(err)
}

func (s *ESVisibilitySuite) TestDeleteExecution_EmptyRequest() {
	// test empty request
	request := &visibility.VisibilityDeleteWorkflowExecutionRequest{}

	s.mockProcessor.EXPECT().Add(gomock.Any(), gomock.Any()).
		DoAndReturn(func(bulkRequest *esclient.BulkableRequest, visibilityTaskKey string) <-chan bool {
			s.Equal("~", visibilityTaskKey)

			s.Equal(esclient.BulkableRequestTypeDelete, bulkRequest.RequestType)
			s.Equal("~", bulkRequest.ID)
			s.Equal("test-index", bulkRequest.Index)

			ackCh := make(chan bool, 1)
			ackCh <- true
			return ackCh
		})

	err := s.visibilityStore.DeleteWorkflowExecution(request)
	s.NoError(err)
}
