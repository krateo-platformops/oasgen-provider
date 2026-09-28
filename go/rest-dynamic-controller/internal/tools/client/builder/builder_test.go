package builder

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/text"
	restclient "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/client"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/client/apiaction"
	getter "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/fieldmapping"
	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// mockUnstructuredClient is a mock implementation of the UnstructuredClientInterface for testing.
type mockUnstructuredClient struct {
	requestedParams  map[string]text.StringSet
	requestedQuery   map[string]text.StringSet
	requestedHeaders map[string]text.StringSet
	requestedCookies map[string]text.StringSet
	requestedBody    map[string]text.StringSet
	validateError    error
	paramsError      error
	bodyError        error
}

func (m *mockUnstructuredClient) RequestedParams(method, path string) (text.StringSet, text.StringSet, text.StringSet, text.StringSet, error) {
	if m.paramsError != nil {
		return nil, nil, nil, nil, m.paramsError
	}
	return m.requestedParams[path], m.requestedQuery[path], m.requestedHeaders[path], m.requestedCookies[path], nil
}

func (m *mockUnstructuredClient) RequestedBody(method, path string) (text.StringSet, error) {
	if m.bodyError != nil {
		return nil, m.bodyError
	}
	return m.requestedBody[path], nil
}

func (m *mockUnstructuredClient) ValidateRequest(method, path string, params, query, headers, cookies map[string]string) error {
	return m.validateError
}

func (m *mockUnstructuredClient) Validate(req *http.Request) (bool, []error) {
	if m.validateError != nil {
		return false, []error{m.validateError}
	}
	return true, nil
}

func (m *mockUnstructuredClient) Call(ctx context.Context, cli *http.Client, path string, conf *restclient.RequestConfiguration) (restclient.Response, error) {
	return restclient.Response{}, nil
}

func (m *mockUnstructuredClient) FindBy(ctx context.Context, cli *http.Client, path string, conf *restclient.RequestConfiguration, findByAction *getter.VerbsDescription) (restclient.Response, error) {
	return restclient.Response{}, nil
}

func TestAPICallBuilder(t *testing.T) {
	info := &getter.Info{
		Resource: getter.Resource{
			Identifiers: []string{"id"},
			VerbsDescription: []getter.VerbsDescription{
				{Action: "get", Method: "GET", Path: "/test"},
				{Action: "create", Method: "POST", Path: "/test"},
				{Action: "update", Method: "PUT", Path: "/test"},
				{Action: "delete", Method: "DELETE", Path: "/test"},
				{Action: "findBy", Method: "GET", Path: "/test"},
			},
		},
	}

	testCases := []struct {
		name           string
		client         *mockUnstructuredClient
		action         apiaction.APIAction
		expectCallInfo bool
		expectedMethod string
		expectError    bool
		expectedErrMsg string
	}{
		{
			name:   "GET action",
			action: apiaction.Get,
			client: &mockUnstructuredClient{
				requestedParams: map[string]text.StringSet{"/test": text.NewStringSet("id")},
			},
			expectCallInfo: true,
			expectedMethod: "GET",
			expectError:    false,
		},
		{
			name:   "CREATE action",
			action: apiaction.Create,
			client: &mockUnstructuredClient{
				requestedBody: map[string]text.StringSet{"/test": text.NewStringSet("name")},
			},
			expectCallInfo: true,
			expectedMethod: "POST",
			expectError:    false,
		},
		{
			name:   "UPDATE action",
			action: apiaction.Update,
			client: &mockUnstructuredClient{
				requestedBody: map[string]text.StringSet{"/test": text.NewStringSet("name")},
			},
			expectCallInfo: true,
			expectedMethod: "PUT",
			expectError:    false,
		},
		{
			name:           "DELETE action",
			action:         apiaction.Delete,
			client:         &mockUnstructuredClient{},
			expectCallInfo: true,
			expectedMethod: "DELETE",
			expectError:    false,
		},
		{
			name:           "FindBy action",
			action:         apiaction.FindBy,
			client:         &mockUnstructuredClient{},
			expectCallInfo: true,
			expectedMethod: "GET",
			expectError:    false,
		},
		{
			name:           "Unknown action",
			action:         apiaction.APIAction("unknown"),
			client:         &mockUnstructuredClient{},
			expectCallInfo: false,
			expectError:    false,
		},
		{
			name:   "Error from RequestedParams",
			action: apiaction.Get,
			client: &mockUnstructuredClient{
				paramsError: errors.New("client params error"),
			},
			expectCallInfo: false,
			expectError:    true,
			expectedErrMsg: "retrieving requested params: client params error",
		},
		{
			name:   "Error from RequestedBody",
			action: apiaction.Create,
			client: &mockUnstructuredClient{
				bodyError: errors.New("client body error"),
			},
			expectCallInfo: false,
			expectError:    true,
			expectedErrMsg: "retrieving requested body params: client body error",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			_, callInfo, err := APICallBuilder(tc.client, info, tc.action)

			if tc.expectError {
				assert.Error(t, err)
				assert.EqualError(t, err, tc.expectedErrMsg)
			} else {
				assert.NoError(t, err)
			}

			if tc.expectCallInfo {
				assert.NotNil(t, callInfo)
				// apiFunc check is tricky because of function pointers, so we check callInfo
				assert.Equal(t, tc.expectedMethod, callInfo.Method)
			} else {
				assert.Nil(t, callInfo)
			}
		})
	}
}

func TestBuildCallConfig(t *testing.T) {
	baseCallInfo := &CallInfo{
		Path:   "/test/{id}",
		Method: "GET",
		ReqParams: &RequestedParams{
			Parameters: text.NewStringSet("id"),
			Query:      text.NewStringSet("filter"),
			Body:       text.NewStringSet("name"),
		},
		IdentifierFields: []string{"id"},
	}

	baseMg := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"status": map[string]interface{}{
				"id":     "123",
				"status": "active",
			},
			"spec": map[string]interface{}{
				"filter": "test",
				"name":   "testname",
				"nested": map[string]interface{}{
					"field1": "value1",
					"field2": "value2",
				},
				"root.level.with.dots": "dotvalue_original",
			},
		},
	}

	testCases := []struct {
		name           string
		callInfo       *CallInfo
		mg             *unstructured.Unstructured
		configSpec     map[string]interface{}
		expectNil      bool
		expectedMethod string
		expectedParams map[string]string
		expectedQuery  map[string]string
		expectedBody   map[string]interface{}
	}{
		{
			name:           "Happy path",
			callInfo:       baseCallInfo,
			mg:             baseMg,
			configSpec:     nil,
			expectNil:      false,
			expectedMethod: "GET",
			expectedParams: map[string]string{"id": "123"},
			expectedQuery:  map[string]string{"filter": "test"},
			expectedBody:   map[string]interface{}{"name": "testname"},
		},
		{
			name:      "Nil callInfo",
			callInfo:  nil,
			mg:        baseMg,
			expectNil: true,
		},
		{
			name:      "Nil managed resource",
			callInfo:  baseCallInfo,
			mg:        nil,
			expectNil: true,
		},
		{
			name:     "Resource with missing status",
			callInfo: baseCallInfo,
			mg: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"spec": map[string]interface{}{
						"filter": "test",
						"name":   "testname",
						"id":     "456", // ID is now in spec
					},
				},
			},
			expectNil:      false,
			expectedMethod: "GET",
			expectedParams: map[string]string{"id": "456"},
			expectedQuery:  map[string]string{"filter": "test"},
			expectedBody:   map[string]interface{}{"name": "testname"},
		},
		{
			name:     "Resource with missing spec",
			callInfo: baseCallInfo,
			mg: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"status": map[string]interface{}{
						"id": "123",
					},
				},
			},
			expectNil:      false,
			expectedMethod: "GET",
			expectedParams: map[string]string{"id": "123"},
			expectedQuery:  map[string]string{},
			expectedBody:   map[string]interface{}{},
		},
		{
			name: "FieldMapping from status to path",
			callInfo: &CallInfo{
				ReqParams: &RequestedParams{
					Parameters: text.NewStringSet("id"),
				},
				RequestFieldMapping: []getter.RequestFieldMappingItem{
					{
						InPath:           "id",
						InCustomResource: "status.metadata.id",
					},
				},
			},
			mg: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"status": map[string]interface{}{
						"metadata": map[string]interface{}{
							"id": "subnet-123",
						},
					},
				},
			},
			expectedParams: map[string]string{
				"id": "subnet-123",
			},
			expectedQuery: map[string]string{},
			expectedBody:  map[string]interface{}{},
		},
		{
			name: "FieldMapping from spec to query",
			callInfo: &CallInfo{
				ReqParams: &RequestedParams{
					Query: text.NewStringSet("ignoreDeleted"),
				},
				RequestFieldMapping: []getter.RequestFieldMappingItem{
					{
						InQuery:          "ignoreDeleted",
						InCustomResource: "spec.ignoreDeletedStatus",
					},
				},
			},
			mg: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"spec": map[string]interface{}{
						"ignoreDeletedStatus": true,
					},
				},
			},
			expectedParams: map[string]string{},
			expectedQuery: map[string]string{
				"ignoreDeleted": "true",
			},
			expectedBody: map[string]interface{}{},
		},
		{
			name: "FieldMapping from spec to body",
			callInfo: &CallInfo{
				ReqParams: &RequestedParams{
					Body: text.NewStringSet("instanceName", "cores"),
				},
				RequestFieldMapping: []getter.RequestFieldMappingItem{
					{
						InBody:           "instanceName",
						InCustomResource: "spec.name",
					},
					{
						InBody:           "cores",
						InCustomResource: "spec.cpu",
					},
				},
			},
			mg: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"spec": map[string]interface{}{
						"name": "my-instance",
						"cpu":  4,
					},
				},
			},
			expectedParams: map[string]string{},
			expectedQuery:  map[string]string{},
			expectedBody: map[string]interface{}{
				"instanceName": "my-instance",
				"cores":        int64(4),
			},
		},
		{
			name: "Mixed mapping",
			callInfo: &CallInfo{
				ReqParams: &RequestedParams{
					Parameters: text.NewStringSet("projectId", "vpcId", "id"),
				},
				RequestFieldMapping: []getter.RequestFieldMappingItem{
					{
						InPath:           "id",
						InCustomResource: "status.metadata.id",
					},
				},
			},
			mg: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"spec": map[string]interface{}{
						"projectId": "p1",
						"vpcId":     "v1",
					},
					"status": map[string]interface{}{
						"metadata": map[string]interface{}{
							"id": "subnet-123",
						},
					},
				},
			},
			expectedParams: map[string]string{
				"projectId": "p1",
				"vpcId":     "v1",
				"id":        "subnet-123",
			},
			expectedQuery: map[string]string{},
			expectedBody:  map[string]interface{}{},
		},
		{
			name: "Field renaming",
			callInfo: &CallInfo{
				ReqParams: &RequestedParams{
					Parameters: text.NewStringSet("owner", "repo"),
				},
				RequestFieldMapping: []getter.RequestFieldMappingItem{
					{
						InPath:           "owner",
						InCustomResource: "spec.org",
					},
					{
						InPath:           "repo",
						InCustomResource: "spec.name",
					},
				},
			},
			mg: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"spec": map[string]interface{}{
						"org":  "my-org",
						"name": "my-repo",
					},
				},
			},
			expectedParams: map[string]string{
				"owner": "my-org",
				"repo":  "my-repo",
			},
			expectedQuery: map[string]string{},
			expectedBody:  map[string]interface{}{},
		},
		{
			name: "Nested body mapping",
			callInfo: &CallInfo{
				ReqParams: &RequestedParams{
					Body: text.NewStringSet("nested"),
				},
				RequestFieldMapping: []getter.RequestFieldMappingItem{
					{
						InBody:           "nested.field1",
						InCustomResource: "spec.nested.field1",
					},
					{
						InBody:           "nested.field2",
						InCustomResource: "spec.nested.field2",
					},
				},
			},
			mg: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"spec": map[string]interface{}{
						"nested": map[string]interface{}{
							"field1": "value1",
							"field2": "value2",
						},
					},
				},
			},
			expectedParams: map[string]string{},
			expectedQuery:  map[string]string{},
			expectedBody: map[string]interface{}{
				"nested": map[string]interface{}{
					"field1": "value1",
					"field2": "value2",
				},
			},
		},
		{
			name: "Dot literals in field name of spec",
			callInfo: &CallInfo{
				ReqParams: &RequestedParams{
					Body: text.NewStringSet("dotfield.in.body"),
				},
				RequestFieldMapping: []getter.RequestFieldMappingItem{
					{
						InBody:           "['dotfield.in.body']",
						InCustomResource: "spec.['root.level.with.dots']",
					},
				},
			},
			mg:             baseMg,
			expectedParams: map[string]string{},
			expectedQuery:  map[string]string{},
			expectedBody: map[string]interface{}{
				"dotfield.in.body": "dotvalue_original",
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			config := BuildCallConfig(context.Background(), tc.callInfo, tc.mg, tc.configSpec, nil)

			if tc.expectNil {
				assert.Nil(t, config)
				return
			}

			assert.NotNil(t, config)
			if tc.expectedMethod != "" {
				assert.Equal(t, tc.expectedMethod, config.Method)
			}
			if diff := cmp.Diff(tc.expectedParams, config.Parameters); diff != "" {
				t.Errorf("mismatch in parameters (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.expectedQuery, config.Query); diff != "" {
				t.Errorf("mismatch in query (-want +got):\n%s", diff)
			}

			bodyMap, ok := config.Body.(map[string]interface{})
			assert.True(t, ok)
			if diff := cmp.Diff(tc.expectedBody, bodyMap); diff != "" {
				t.Errorf("mismatch in body (-want +got):\n%s", diff)
			}
		})
	}
}

func TestBuildCallConfig_WithMerge(t *testing.T) {
	testCases := []struct {
		name            string
		callInfo        *CallInfo
		mg              *unstructured.Unstructured
		configSpec      map[string]interface{}
		expectedQuery   map[string]string
		expectedHeaders map[string]string
		expectedBody    map[string]interface{}
	}{
		{
			name: "Values from config and resource are merged correctly",
			callInfo: &CallInfo{
				Action: apiaction.Get,
				ReqParams: &RequestedParams{
					Query:   text.NewStringSet("filter", "api-version"),
					Body:    text.NewStringSet("name", "description"),
					Headers: text.NewStringSet("X-Custom-Header"),
				},
			},
			mg: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"spec": map[string]interface{}{
						"filter": "from-resource", // This should not override the value from config
						"name":   "from-resource",
					},
				},
			},
			configSpec: map[string]interface{}{
				"query": map[string]interface{}{
					"get": map[string]interface{}{
						"api-version": "v1-from-config",
						"filter":      "from-config",
					},
				},
				"headers": map[string]interface{}{
					"get": map[string]interface{}{
						"X-Custom-Header": "from-config",
					},
				},
			},
			expectedQuery: map[string]string{
				"api-version": "v1-from-config",
				"filter":      "from-config",
			},
			expectedHeaders: map[string]string{
				"X-Custom-Header": "from-config",
			},
			expectedBody: map[string]interface{}{
				"name": "from-resource",
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			config := BuildCallConfig(context.Background(), tc.callInfo, tc.mg, tc.configSpec, nil)

			assert.NotNil(t, config, "config should not be nil")
			assert.Equal(t, tc.expectedQuery, config.Query)
			assert.Equal(t, tc.expectedHeaders, config.Headers)

			body, ok := config.Body.(map[string]interface{})
			assert.True(t, ok, "body should be a map")
			assert.Equal(t, tc.expectedBody, body)
		})
	}
}

func TestIsResourceKnown(t *testing.T) {
	baseInfo := &getter.Info{
		Resource: getter.Resource{
			Identifiers: []string{"id"},
			VerbsDescription: []getter.VerbsDescription{
				{Action: "get", Method: "GET", Path: "/test"},
			},
		},
	}

	baseMg := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"status": map[string]interface{}{"id": "123"},
		},
	}

	testCases := []struct {
		name   string
		client restclient.UnstructuredClientInterface
		info   *getter.Info
		mg     *unstructured.Unstructured
		expect bool
	}{
		{
			name: "Happy path, resource is known",
			client: &mockUnstructuredClient{
				requestedParams: map[string]text.StringSet{"/test": text.NewStringSet("id")},
				validateError:   nil,
			},
			info:   baseInfo,
			mg:     baseMg,
			expect: true,
		},
		{
			name:   "Nil clientInfo",
			client: &mockUnstructuredClient{},
			info:   nil,
			mg:     baseMg,
			expect: false,
		},
		{
			name:   "Nil managed resource",
			client: &mockUnstructuredClient{},
			info:   baseInfo,
			mg:     nil,
			expect: false,
		},
		{
			name: "Error from APICallBuilder",
			client: &mockUnstructuredClient{
				paramsError: errors.New("client error"),
			},
			info:   baseInfo,
			mg:     baseMg,
			expect: false,
		},
		{
			name: "Error from ValidateRequest",
			client: &mockUnstructuredClient{
				requestedParams: map[string]text.StringSet{"/test": text.NewStringSet("id")},
				validateError:   errors.New("validation failed"),
			},
			info:   baseInfo,
			mg:     baseMg,
			expect: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := IsResourceKnown(context.Background(), tc.client, tc.info, tc.mg)
			assert.Equal(t, tc.expect, result)
		})
	}
}

func TestProcessFields(t *testing.T) {
	testCases := []struct {
		name           string
		callInfo       *CallInfo
		fields         map[string]interface{}
		expectedParams map[string]string
		expectedQuery  map[string]string
		expectedBody   map[string]interface{}
	}{
		{
			name: "Separate fields for path, query, and body",
			callInfo: &CallInfo{
				ReqParams: &RequestedParams{
					Parameters: text.NewStringSet("id"),
					Query:      text.NewStringSet("filter"),
					Body:       text.NewStringSet("name"),
				},
			},
			fields: map[string]interface{}{
				"id":     "123",
				"filter": "test",
				"name":   "testname",
				"":       "empty", // should be skipped
			},
			expectedParams: map[string]string{"id": "123"},
			expectedQuery:  map[string]string{"filter": "test"},
			expectedBody:   map[string]interface{}{"name": "testname"},
		},
		{
			name: "Field used both in Path and Body",
			callInfo: &CallInfo{
				ReqParams: &RequestedParams{
					Parameters: text.NewStringSet("id"),
					Query:      text.NewStringSet("filter"),
					Body:       text.NewStringSet("id", "name"),
				},
			},
			fields: map[string]interface{}{
				"id":   "123",
				"name": "testname",
			},
			expectedParams: map[string]string{"id": "123"},
			expectedQuery:  map[string]string{},
			expectedBody:   map[string]interface{}{"id": "123", "name": "testname"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			reqConfig := &restclient.RequestConfiguration{
				Parameters: make(map[string]string),
				Query:      make(map[string]string),
			}
			mapBody := make(map[string]interface{})

			processFields(tc.callInfo, tc.fields, reqConfig, mapBody)

			assert.Equal(t, tc.expectedParams, reqConfig.Parameters)
			assert.Equal(t, tc.expectedQuery, reqConfig.Query)
			assert.Equal(t, tc.expectedBody, mapBody)

			_, exists := reqConfig.Parameters[""]
			assert.False(t, exists, "empty field should not be processed")

			_, exists = reqConfig.Query[""]
			assert.False(t, exists, "empty field should not be processed")

			_, exists = mapBody[""]
			assert.False(t, exists, "empty field should not be processed")

		})
	}
}

func TestApplyConfigSpec(t *testing.T) {
	testCases := []struct {
		name            string
		configSpec      map[string]interface{}
		action          string
		initialReq      *restclient.RequestConfiguration
		expectedReq     *restclient.RequestConfiguration
		expectNoChanges bool
	}{
		{
			name:   "Happy path - all fields populated for 'get' action",
			action: "get",
			configSpec: map[string]interface{}{
				"headers": map[string]interface{}{
					"get": map[string]interface{}{
						"X-Test-Header": "header-value",
						"X-Another":     "another-header",
					},
					"post": map[string]interface{}{ // This should be ignored since we are dealing with "get" action
						"X-Post-Header": "post-header",
					},
				},
				"query": map[string]interface{}{
					"get": map[string]interface{}{
						"param1": "value1",
						"param2": int64(123), // non-string value
					},
				},
				"cookies": map[string]interface{}{
					"get": map[string]interface{}{
						"session": "abc",
					},
				},
				"path": map[string]interface{}{
					"get": map[string]interface{}{
						"userId": "user-123",
					},
				},
			},
			initialReq: &restclient.RequestConfiguration{
				Headers:    make(map[string]string),
				Query:      make(map[string]string),
				Cookies:    make(map[string]string),
				Parameters: make(map[string]string),
			},
			expectedReq: &restclient.RequestConfiguration{
				Headers: map[string]string{
					"X-Test-Header": "header-value",
					"X-Another":     "another-header",
				},
				Query: map[string]string{
					"param1": "value1",
					"param2": "123", // Converted to string
				},
				Cookies: map[string]string{
					"session": "abc",
				},
				Parameters: map[string]string{
					"userId": "user-123",
				},
			},
		},
		{
			name:       "Nil configSpec",
			action:     "get",
			configSpec: nil,
			initialReq: &restclient.RequestConfiguration{
				Headers:    make(map[string]string),
				Query:      make(map[string]string),
				Cookies:    make(map[string]string),
				Parameters: make(map[string]string),
			},
			expectNoChanges: true,
		},
		{
			name:       "Empty configSpec",
			action:     "get",
			configSpec: map[string]interface{}{},
			initialReq: &restclient.RequestConfiguration{
				Headers:    make(map[string]string),
				Query:      make(map[string]string),
				Cookies:    make(map[string]string),
				Parameters: make(map[string]string),
			},
			expectNoChanges: true,
		},
		{
			name:   "No matching action in configSpec",
			action: "delete",
			configSpec: map[string]interface{}{
				"headers": map[string]interface{}{
					"get": map[string]interface{}{
						"X-Test-Header": "header-value",
					},
				},
			},
			initialReq: &restclient.RequestConfiguration{
				Headers:    make(map[string]string),
				Query:      make(map[string]string),
				Cookies:    make(map[string]string),
				Parameters: make(map[string]string),
			},
			expectNoChanges: true,
		},
		{
			name:   "Partially matching configSpec",
			action: "get",
			configSpec: map[string]interface{}{
				"headers": map[string]interface{}{
					"get": map[string]interface{}{
						"X-Test-Header": "header-value",
					},
				},
				// No "query" for "get", so this should be ignored
				"query": map[string]interface{}{
					"post": map[string]interface{}{
						"param1": "value1",
					},
				},
			},
			initialReq: &restclient.RequestConfiguration{
				Headers:    make(map[string]string),
				Query:      make(map[string]string),
				Cookies:    make(map[string]string),
				Parameters: make(map[string]string),
			},
			expectedReq: &restclient.RequestConfiguration{
				Headers: map[string]string{
					"X-Test-Header": "header-value",
				},
				Query:      make(map[string]string),
				Cookies:    make(map[string]string),
				Parameters: make(map[string]string),
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// If we expect no changes, the expected state is a clone of the initial state.
			if tc.expectNoChanges {
				tc.expectedReq = &restclient.RequestConfiguration{
					Headers:    make(map[string]string),
					Query:      make(map[string]string),
					Cookies:    make(map[string]string),
					Parameters: make(map[string]string),
				}
				for k, v := range tc.initialReq.Headers {
					tc.expectedReq.Headers[k] = v
				}
				for k, v := range tc.initialReq.Query {
					tc.expectedReq.Query[k] = v
				}
				for k, v := range tc.initialReq.Cookies {
					tc.expectedReq.Cookies[k] = v
				}
				for k, v := range tc.initialReq.Parameters {
					tc.expectedReq.Parameters[k] = v
				}
			}

			applyConfigSpec(tc.initialReq, tc.configSpec, tc.action)

			assert.Equal(t, tc.expectedReq.Headers, tc.initialReq.Headers)
			assert.Equal(t, tc.expectedReq.Query, tc.initialReq.Query)
			assert.Equal(t, tc.expectedReq.Cookies, tc.initialReq.Cookies)
			assert.Equal(t, tc.expectedReq.Parameters, tc.initialReq.Parameters)
		})
	}
}

func TestApplyRequestFieldMapping(t *testing.T) {
	testCases := []struct {
		name              string
		callInfo          *CallInfo
		mg                *unstructured.Unstructured
		initialReqConfig  *restclient.RequestConfiguration
		initialMapBody    map[string]interface{}
		expectedReqConfig *restclient.RequestConfiguration
		expectedMapBody   map[string]interface{}
	}{
		{
			name: "Map from spec to path, query, and body",
			callInfo: &CallInfo{
				RequestFieldMapping: []getter.RequestFieldMappingItem{
					{InPath: "userId", InCustomResource: "spec.userIdentifier"},
					{InQuery: "filter", InCustomResource: "spec.queryFilter"},
					{InBody: "itemName", InCustomResource: "spec.name"},
					{InBody: "itemValue", InCustomResource: "spec.value"},
				},
			},
			mg: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"spec": map[string]interface{}{
						"userIdentifier": "user-123",
						"queryFilter":    "active",
						"name":           "my-item",
						"value":          100,
					},
				},
			},
			initialReqConfig: &restclient.RequestConfiguration{
				Parameters: make(map[string]string),
				Query:      make(map[string]string),
			},
			initialMapBody: make(map[string]interface{}),
			expectedReqConfig: &restclient.RequestConfiguration{
				Parameters: map[string]string{"userId": "user-123"},
				Query:      map[string]string{"filter": "active"},
			},
			expectedMapBody: map[string]interface{}{
				"itemName":  "my-item",
				"itemValue": int64(100),
			},
		},
		{
			name: "Map from nested status field to path",
			callInfo: &CallInfo{
				RequestFieldMapping: []getter.RequestFieldMappingItem{
					{InPath: "id", InCustomResource: "status.metadata.id"},
				},
			},
			mg: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"status": map[string]interface{}{
						"metadata": map[string]interface{}{
							"id": "subnet-xyz",
						},
					},
				},
			},
			initialReqConfig: &restclient.RequestConfiguration{
				Parameters: make(map[string]string),
				Query:      make(map[string]string),
			},
			initialMapBody: make(map[string]interface{}),
			expectedReqConfig: &restclient.RequestConfiguration{
				Parameters: map[string]string{"id": "subnet-xyz"},
				Query:      map[string]string{},
			},
			expectedMapBody: make(map[string]interface{}),
		},
		{
			name: "Field not found in custom resource", // Eventually, this should not be experienced since the validation of presence is done earlier in the oasgen-provider
			callInfo: &CallInfo{
				RequestFieldMapping: []getter.RequestFieldMappingItem{
					{InPath: "id", InCustomResource: "spec.nonExistent"},
				},
			},
			mg: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"spec": map[string]interface{}{"someOtherField": "value"},
				},
			},
			initialReqConfig: &restclient.RequestConfiguration{
				Parameters: make(map[string]string),
				Query:      make(map[string]string),
			},
			initialMapBody: make(map[string]interface{}),
			expectedReqConfig: &restclient.RequestConfiguration{
				Parameters: make(map[string]string),
				Query:      make(map[string]string),
			},
			expectedMapBody: make(map[string]interface{}),
		},
		{
			name: "Nil RequestFieldMapping",
			callInfo: &CallInfo{
				RequestFieldMapping: nil,
			},
			mg: &unstructured.Unstructured{
				Object: map[string]interface{}{"spec": map[string]interface{}{"id": "123"}},
			},
			initialReqConfig: &restclient.RequestConfiguration{
				Parameters: make(map[string]string),
				Query:      make(map[string]string),
			},
			initialMapBody: make(map[string]interface{}),
			expectedReqConfig: &restclient.RequestConfiguration{
				Parameters: make(map[string]string),
				Query:      make(map[string]string),
			},
			expectedMapBody: make(map[string]interface{}),
		},
		{
			name: "Empty RequestFieldMapping",
			callInfo: &CallInfo{
				RequestFieldMapping: []getter.RequestFieldMappingItem{},
			},
			mg: &unstructured.Unstructured{
				Object: map[string]interface{}{"spec": map[string]interface{}{"id": "123"}},
			},
			initialReqConfig: &restclient.RequestConfiguration{
				Parameters: make(map[string]string),
				Query:      make(map[string]string),
			},
			initialMapBody: make(map[string]interface{}),
			expectedReqConfig: &restclient.RequestConfiguration{
				Parameters: make(map[string]string),
				Query:      make(map[string]string),
			},
			expectedMapBody: make(map[string]interface{}),
		},
		{
			name: "Mapping overwrites existing values", // TODO: check if this could happen in practice
			callInfo: &CallInfo{
				RequestFieldMapping: []getter.RequestFieldMappingItem{
					{InPath: "id", InCustomResource: "spec.id"},
					{InBody: "name", InCustomResource: "spec.name"},
				},
			},
			mg: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"spec": map[string]interface{}{
						"id":   "new-id",
						"name": "new-name",
					},
				},
			},
			initialReqConfig: &restclient.RequestConfiguration{
				Parameters: map[string]string{"id": "old-id"},
				Query:      make(map[string]string),
			},
			initialMapBody: map[string]interface{}{"name": "old-name"},
			expectedReqConfig: &restclient.RequestConfiguration{
				Parameters: map[string]string{"id": "new-id"},
				Query:      make(map[string]string),
			},
			expectedMapBody: map[string]interface{}{"name": "new-name"},
		},
		{
			name: "Nested in body mapping",
			callInfo: &CallInfo{
				RequestFieldMapping: []getter.RequestFieldMappingItem{
					{InBody: "metadata.owner", InCustomResource: "spec.owner"},
				},
			},
			mg: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"spec": map[string]interface{}{
						"owner": "team-a",
					},
				},
			},
			initialReqConfig: &restclient.RequestConfiguration{
				Parameters: make(map[string]string),
				Query:      make(map[string]string),
			},
			initialMapBody: make(map[string]interface{}),
			expectedReqConfig: &restclient.RequestConfiguration{
				Parameters: make(map[string]string),
				Query:      make(map[string]string),
			},
			expectedMapBody: map[string]interface{}{
				"metadata": map[string]interface{}{
					"owner": "team-a",
				},
			},
		},
		{
			name: "Dot literals in field names",
			callInfo: &CallInfo{
				RequestFieldMapping: []getter.RequestFieldMappingItem{
					{InBody: "req_body_root.['dot.field.in.body']", InCustomResource: "spec.first_level_field.['level.with.dots']"},
					{InPath: "['path_parameter.with.dots']", InCustomResource: "spec.first_level_field.['level.with.dots']"},
					{InQuery: "['query_parameter.with.dots']", InCustomResource: "spec.['another.field.with.dots']"},
				},
			},
			mg: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"spec": map[string]interface{}{
						"first_level_field": map[string]interface{}{
							"level.with.dots": "dotvalue",
						},
						"another.field.with.dots": "othervalue",
					},
				},
			},
			initialReqConfig: &restclient.RequestConfiguration{
				Parameters: make(map[string]string),
				Query:      make(map[string]string),
			},
			initialMapBody: make(map[string]interface{}),
			expectedReqConfig: &restclient.RequestConfiguration{
				Parameters: map[string]string{"path_parameter.with.dots": "dotvalue"},
				Query:      map[string]string{"query_parameter.with.dots": "othervalue"},
			},
			expectedMapBody: map[string]interface{}{
				"req_body_root": map[string]interface{}{
					"dot.field.in.body": "dotvalue",
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			applyRequestFieldMapping(tc.callInfo, tc.mg, tc.initialReqConfig, tc.initialMapBody)

			if diff := cmp.Diff(tc.expectedReqConfig.Parameters, tc.initialReqConfig.Parameters); diff != "" {
				t.Errorf("mismatch in parameters (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.expectedReqConfig.Query, tc.initialReqConfig.Query); diff != "" {
				t.Errorf("mismatch in query (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.expectedMapBody, tc.initialMapBody); diff != "" {
				t.Errorf("mismatch in body (-want +got):\n%s", diff)
			}
		})
	}
}

func TestApplyFieldMapping(t *testing.T) {
	testCases := []struct {
		name              string
		callInfo          *CallInfo
		mg                *unstructured.Unstructured
		initialReqConfig  *restclient.RequestConfiguration
		initialMapBody    map[string]interface{}
		expectedReqConfig *restclient.RequestConfiguration
		expectedMapBody   map[string]interface{}
	}{
		{
			name: "Map from spec to path, query, and body, no value transform",
			callInfo: &CallInfo{
				FieldMapping: []getter.FieldMappingItem{
					{InPath: "userId", InCustomResource: "spec.userIdentifier"},
					{InQuery: "filter", InCustomResource: "spec.queryFilter"},
					{InBody: "itemName", InCustomResource: "spec.name"},
				},
			},
			mg: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"spec": map[string]interface{}{
						"userIdentifier": "user-123",
						"queryFilter":    "active",
						"name":           "my-item",
					},
				},
			},
			initialReqConfig: &restclient.RequestConfiguration{
				Parameters: make(map[string]string),
				Query:      make(map[string]string),
			},
			initialMapBody: make(map[string]interface{}),
			expectedReqConfig: &restclient.RequestConfiguration{
				Parameters: map[string]string{"userId": "user-123"},
				Query:      map[string]string{"filter": "active"},
			},
			expectedMapBody: map[string]interface{}{"itemName": "my-item"},
		},
		{
			name: "Alias value transform applied in the request direction",
			callInfo: &CallInfo{
				FieldMapping: []getter.FieldMappingItem{
					{
						InQuery:          "role",
						InCustomResource: "spec.role",
						ValueMapping: &getter.ValueMapping{
							Type: "alias",
							Aliases: []getter.ValueAlias{
								{CustomResourceValue: "maintainer", APIValue: "pull"},
							},
						},
					},
				},
			},
			mg: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"spec": map[string]interface{}{"role": "maintainer"},
				},
			},
			initialReqConfig: &restclient.RequestConfiguration{
				Parameters: make(map[string]string),
				Query:      make(map[string]string),
			},
			initialMapBody: make(map[string]interface{}),
			expectedReqConfig: &restclient.RequestConfiguration{
				Parameters: make(map[string]string),
				Query:      map[string]string{"role": "pull"},
			},
			expectedMapBody: make(map[string]interface{}),
		},
		{
			name: "Unmapped alias value passes through unchanged",
			callInfo: &CallInfo{
				FieldMapping: []getter.FieldMappingItem{
					{
						InQuery:          "role",
						InCustomResource: "spec.role",
						ValueMapping: &getter.ValueMapping{
							Type: "alias",
							Aliases: []getter.ValueAlias{
								{CustomResourceValue: "maintainer", APIValue: "pull"},
							},
						},
					},
				},
			},
			mg: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"spec": map[string]interface{}{"role": "owner"},
				},
			},
			initialReqConfig: &restclient.RequestConfiguration{
				Parameters: make(map[string]string),
				Query:      make(map[string]string),
			},
			initialMapBody: make(map[string]interface{}),
			expectedReqConfig: &restclient.RequestConfiguration{
				Parameters: make(map[string]string),
				Query:      map[string]string{"role": "owner"},
			},
			expectedMapBody: make(map[string]interface{}),
		},
		{
			// Was "jq ... is not yet wired for request direction: entry is skipped". It is wired now, and
			// the skip it asserted was the bug: a declared transform produced NO field at all (#117).
			name: "jq value transform applies in the request direction",
			callInfo: &CallInfo{
				FieldMapping: []getter.FieldMappingItem{
					{
						InQuery:          "role",
						InCustomResource: "spec.role",
						ValueMapping: &getter.ValueMapping{
							Type: "jq",
							JQ:   &getter.JQProgram{Inline: `.`},
						},
					},
				},
			},
			mg: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"spec": map[string]interface{}{"role": "owner"},
				},
			},
			initialReqConfig: &restclient.RequestConfiguration{
				Parameters: make(map[string]string),
				Query:      make(map[string]string),
			},
			initialMapBody: make(map[string]interface{}),
			expectedReqConfig: &restclient.RequestConfiguration{
				Parameters: make(map[string]string),
				Query:      map[string]string{"role": "owner"},
			},
			expectedMapBody: make(map[string]interface{}),
		},
		{
			name: "inResponse-only entries are ignored",
			callInfo: &CallInfo{
				FieldMapping: []getter.FieldMappingItem{
					{InResponse: "id", InCustomResource: "status.id"},
				},
			},
			mg: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"status": map[string]interface{}{"id": "abc"},
				},
			},
			initialReqConfig: &restclient.RequestConfiguration{
				Parameters: make(map[string]string),
				Query:      make(map[string]string),
			},
			initialMapBody: make(map[string]interface{}),
			expectedReqConfig: &restclient.RequestConfiguration{
				Parameters: make(map[string]string),
				Query:      make(map[string]string),
			},
			expectedMapBody: make(map[string]interface{}),
		},
		{
			name: "Nested in body mapping",
			callInfo: &CallInfo{
				FieldMapping: []getter.FieldMappingItem{
					{InBody: "metadata.owner", InCustomResource: "spec.owner"},
				},
			},
			mg: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"spec": map[string]interface{}{"owner": "team-a"},
				},
			},
			initialReqConfig: &restclient.RequestConfiguration{
				Parameters: make(map[string]string),
				Query:      make(map[string]string),
			},
			initialMapBody: make(map[string]interface{}),
			expectedReqConfig: &restclient.RequestConfiguration{
				Parameters: make(map[string]string),
				Query:      make(map[string]string),
			},
			expectedMapBody: map[string]interface{}{
				"metadata": map[string]interface{}{"owner": "team-a"},
			},
		},
		{
			name: "Nil FieldMapping",
			callInfo: &CallInfo{
				FieldMapping: nil,
			},
			mg: &unstructured.Unstructured{
				Object: map[string]interface{}{"spec": map[string]interface{}{"id": "123"}},
			},
			initialReqConfig: &restclient.RequestConfiguration{
				Parameters: make(map[string]string),
				Query:      make(map[string]string),
			},
			initialMapBody: make(map[string]interface{}),
			expectedReqConfig: &restclient.RequestConfiguration{
				Parameters: make(map[string]string),
				Query:      make(map[string]string),
			},
			expectedMapBody: make(map[string]interface{}),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			applyFieldMapping(context.Background(), tc.callInfo, tc.mg, tc.initialReqConfig, tc.initialMapBody, nil)

			if diff := cmp.Diff(tc.expectedReqConfig.Parameters, tc.initialReqConfig.Parameters); diff != "" {
				t.Errorf("mismatch in parameters (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.expectedReqConfig.Query, tc.initialReqConfig.Query); diff != "" {
				t.Errorf("mismatch in query (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.expectedMapBody, tc.initialMapBody); diff != "" {
				t.Errorf("mismatch in body (-want +got):\n%s", diff)
			}
		})
	}
}

// TestApplyFieldMapping_ResolverUsesResolvedValue proves a Resolver-bearing entry never reads
// InCustomResource off mg directly: its value comes entirely from the resolved map, keyed by
// fieldmapping.ResolverKey.
func TestApplyFieldMapping_ResolverUsesResolvedValue(t *testing.T) {
	mapping := getter.FieldMappingItem{
		InBody:           "token",
		InCustomResource: "spec.credentialsRef",
		Resolver: &getter.FieldResolver{
			Type: "secretRef",
			SecretRef: &getter.SecretRefResolver{
				NameFromCustomResource: "spec.credentialsRef.name",
				KeyFromCustomResource:  "spec.credentialsRef.key",
			},
		},
	}
	callInfo := &CallInfo{FieldMapping: []getter.FieldMappingItem{mapping}}
	mg := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"spec": map[string]interface{}{
				"credentialsRef": map[string]interface{}{"name": "db-creds", "key": "password"},
			},
		},
	}
	resolved := map[string]interface{}{
		fieldmapping.ResolverKey(mapping): "hunter2",
	}
	reqConfig := &restclient.RequestConfiguration{Parameters: map[string]string{}, Query: map[string]string{}}
	mapBody := map[string]interface{}{}

	applyFieldMapping(context.Background(), callInfo, mg, reqConfig, mapBody, resolved)

	if mapBody["token"] != "hunter2" {
		t.Fatalf("expected resolved value %q in body, got %v", "hunter2", mapBody["token"])
	}
	if len(reqConfig.SensitiveValues) != 1 || reqConfig.SensitiveValues[0] != "hunter2" {
		t.Fatalf("expected the resolved secretRef value to be recorded as sensitive, got %v", reqConfig.SensitiveValues)
	}
}

// TestApplyFieldMapping_ResolverArrayInBody is a regression test for issue #33: a resolved secretRef value
// must be writable into an array-shaped request body path (e.g. Keycloak's credentials[0].value), not just
// a flat map path. mapBody starts empty, so this also exercises SetNestedField's array auto-vivification.
func TestApplyFieldMapping_ResolverArrayInBody(t *testing.T) {
	mapping := getter.FieldMappingItem{
		InBody:           "credentials[0].value",
		InCustomResource: "spec.credentials[0].valueSecretRef",
		Resolver: &getter.FieldResolver{
			Type: "secretRef",
			SecretRef: &getter.SecretRefResolver{
				NameFromCustomResource: "spec.credentials[0].valueSecretRef.name",
				KeyFromCustomResource:  "spec.credentials[0].valueSecretRef.key",
			},
		},
	}
	callInfo := &CallInfo{FieldMapping: []getter.FieldMappingItem{mapping}}
	mg := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"spec": map[string]interface{}{
				"credentials": []interface{}{
					map[string]interface{}{
						"valueSecretRef": map[string]interface{}{"name": "alice-secret", "key": "password"},
					},
				},
			},
		},
	}
	resolved := map[string]interface{}{
		fieldmapping.ResolverKey(mapping): "hunter2",
	}
	reqConfig := &restclient.RequestConfiguration{Parameters: map[string]string{}, Query: map[string]string{}}
	mapBody := map[string]interface{}{}

	applyFieldMapping(context.Background(), callInfo, mg, reqConfig, mapBody, resolved)

	creds, ok := mapBody["credentials"].([]interface{})
	if !ok || len(creds) != 1 {
		t.Fatalf("expected mapBody[\"credentials\"] to be a 1-element array, got %#v", mapBody["credentials"])
	}
	entry, ok := creds[0].(map[string]interface{})
	if !ok || entry["value"] != "hunter2" {
		t.Fatalf("expected credentials[0].value to be %q, got %#v", "hunter2", creds[0])
	}
}

// TestApplyFieldMapping_ResolverMissingFromResolvedIsSkipped proves a Resolver entry with no matching
// entry in resolved is skipped (left unwritten), not sent with a zero-value/nil placeholder.
func TestApplyFieldMapping_ResolverMissingFromResolvedIsSkipped(t *testing.T) {
	mapping := getter.FieldMappingItem{
		InBody:           "token",
		InCustomResource: "spec.credentialsRef",
		Resolver: &getter.FieldResolver{
			Type: "secretRef",
			SecretRef: &getter.SecretRefResolver{
				NameFromCustomResource: "spec.credentialsRef.name",
				KeyFromCustomResource:  "spec.credentialsRef.key",
			},
		},
	}
	callInfo := &CallInfo{FieldMapping: []getter.FieldMappingItem{mapping}}
	mg := &unstructured.Unstructured{Object: map[string]interface{}{"spec": map[string]interface{}{}}}
	reqConfig := &restclient.RequestConfiguration{Parameters: map[string]string{}, Query: map[string]string{}}
	mapBody := map[string]interface{}{}

	applyFieldMapping(context.Background(), callInfo, mg, reqConfig, mapBody, nil)

	if _, ok := mapBody["token"]; ok {
		t.Fatalf("expected an unresolved resolver entry to be skipped, got body %v", mapBody)
	}
	if len(reqConfig.SensitiveValues) != 0 {
		t.Fatalf("expected no sensitive values recorded, got %v", reqConfig.SensitiveValues)
	}
}

// TestBuildCallConfig_FieldMappingPrecedesAutoPopulation proves the ordering guarantee applyFieldMapping's
// doc comment claims: an explicit FieldMapping entry must win over the resource's own spec/status
// auto-population of a same-named path/query parameter, exactly like the deprecated RequestFieldMapping
// already does.
func TestBuildCallConfig_FieldMappingPrecedesAutoPopulation(t *testing.T) {
	ci := &CallInfo{
		Path:   "/test/{id}",
		Method: "GET",
		ReqParams: &RequestedParams{
			Parameters: text.StringSet{"id": {}}, // "id" is also a spec field, auto-populated by processFields
		},
		FieldMapping: []getter.FieldMappingItem{
			{InPath: "id", InCustomResource: "spec.explicitId"},
		},
	}
	mg := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"spec": map[string]interface{}{
				"id":         "auto-populated-id",
				"explicitId": "explicit-id",
			},
		},
	}

	got := BuildCallConfig(context.Background(), ci, mg, nil, nil)

	if got.Parameters["id"] != "explicit-id" {
		t.Fatalf("expected the explicit FieldMapping entry to win, got %q", got.Parameters["id"])
	}
}

// TestBuildCallConfig_ResolverSourceStrippedFromBody proves a Resolver's CR-side pointer never reaches
// the API. Auto-population would otherwise forward `valueSecretRef` verbatim alongside the resolved
// `value`; Keycloak rejects the resulting CredentialRepresentation with 400. The resolved value must
// survive, and a NON-resolver mapping's source must NOT be stripped.
func TestBuildCallConfig_ResolverSourceStrippedFromBody(t *testing.T) {
	resolverEntry := getter.FieldMappingItem{
		InBody:           "credentials.0.value",
		InCustomResource: "spec.credentials.0.valueSecretRef",
		Resolver: &getter.FieldResolver{
			Type: "secretRef",
			SecretRef: &getter.SecretRefResolver{
				NameFromCustomResource: "spec.credentials.0.valueSecretRef.name",
				KeyFromCustomResource:  "spec.credentials.0.valueSecretRef.key",
			},
		},
	}
	plainEntry := getter.FieldMappingItem{InBody: "label", InCustomResource: "spec.displayName"}

	ci := &CallInfo{
		Path: "/users", Method: "POST",
		ReqParams:    &RequestedParams{Body: text.StringSet{"credentials": {}, "displayName": {}}},
		FieldMapping: []getter.FieldMappingItem{resolverEntry, plainEntry},
	}
	mg := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"spec": map[string]interface{}{
				"displayName": "Alice",
				"credentials": []interface{}{
					map[string]interface{}{
						"type":           "password",
						"valueSecretRef": map[string]interface{}{"name": "alice-secret", "key": "password"},
					},
				},
			},
		},
	}
	resolved := map[string]interface{}{fieldmapping.ResolverKey(resolverEntry): "hunter2"}

	body := BuildCallConfig(context.Background(), ci, mg, nil, resolved).Body.(map[string]interface{})
	c0 := body["credentials"].([]interface{})[0].(map[string]interface{})

	if _, leaked := c0["valueSecretRef"]; leaked {
		t.Errorf("resolver source leaked into the request body: %#v", c0)
	}
	if c0["value"] != "hunter2" {
		t.Errorf("resolved value lost, got %#v", c0["value"])
	}
	if c0["type"] != "password" {
		t.Errorf("unmapped sibling dropped, got %#v", c0)
	}
	// A non-resolver mapping relocates ordinary data; its source is still API-bound.
	if body["displayName"] != "Alice" {
		t.Errorf("non-resolver source must not be stripped, got %#v", body["displayName"])
	}
}

// TestBuildCallConfig_FieldMappingKeepsUnmappedSiblings pins the granularity of the precedence rule
// above: a fieldMapping writing INTO a body field must win only for the path it wrote, while every
// sibling the CR declared under that same top-level field is still auto-populated. Previously
// processFields skipped the whole field once fieldMapping had touched it, so mapping
// `credentials.0.value` shipped a body containing ONLY that value — dropping `type`/`temporary` and
// the entire second credential. Reproduces the live Keycloak shape: the OTP credential silently never
// registered because it reached the API with just `secretData` and no `type`/`credentialData`.
func TestBuildCallConfig_FieldMappingKeepsUnmappedSiblings(t *testing.T) {
	ci := &CallInfo{
		Path:   "/users",
		Method: "POST",
		ReqParams: &RequestedParams{
			Body: text.StringSet{"credentials": {}, "username": {}},
		},
		FieldMapping: []getter.FieldMappingItem{
			{InBody: "credentials.0.value", InCustomResource: "spec.credentials.0.pw"},
			{InBody: "credentials.1.secretData", InCustomResource: "spec.credentials.1.seed"},
		},
	}
	mg := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"spec": map[string]interface{}{
				"username": "alice",
				"credentials": []interface{}{
					map[string]interface{}{"type": "password", "temporary": false, "pw": "pw-val"},
					map[string]interface{}{"type": "otp", "userLabel": "OTP", "credentialData": "{}", "seed": "seed-val"},
				},
			},
		},
	}

	got := BuildCallConfig(context.Background(), ci, mg, nil, nil)

	creds, ok := got.Body.(map[string]interface{})["credentials"].([]interface{})
	if !ok || len(creds) != 2 {
		t.Fatalf("expected 2 credentials in body, got %#v", got.Body.(map[string]interface{})["credentials"])
	}

	c0 := creds[0].(map[string]interface{})
	if c0["value"] != "pw-val" {
		t.Errorf("credentials[0].value: mapped value lost, got %#v", c0["value"])
	}
	if c0["type"] != "password" || c0["temporary"] != false {
		t.Errorf("credentials[0]: unmapped siblings dropped, got %#v", c0)
	}

	c1 := creds[1].(map[string]interface{})
	if c1["secretData"] != "seed-val" {
		t.Errorf("credentials[1].secretData: mapped value lost, got %#v", c1["secretData"])
	}
	if c1["type"] != "otp" || c1["credentialData"] != "{}" || c1["userLabel"] != "OTP" {
		t.Errorf("credentials[1]: unmapped siblings dropped, got %#v", c1)
	}

	if got.Body.(map[string]interface{})["username"] != "alice" {
		t.Errorf("unrelated body field lost, got %#v", got.Body)
	}
}

// TestBuildCallConfig_PredicateMappingIsOrderIndependent is the point of [?key=value]: the SAME field
// mappings must land on the right credential no matter what order the CR lists them in. With positional
// indices, reordering the CR silently writes the password into the OTP credential.
func TestBuildCallConfig_PredicateMappingIsOrderIndependent(t *testing.T) {
	newCI := func() *CallInfo {
		return &CallInfo{
			Path:   "/users",
			Method: "POST",
			ReqParams: &RequestedParams{
				Body: text.StringSet{"credentials": {}, "username": {}},
			},
			FieldMapping: []getter.FieldMappingItem{
				{InBody: "credentials[?type=password].value", InCustomResource: "spec.credentials[?type=password].pw"},
				{InBody: "credentials[?type=otp].secretData", InCustomResource: "spec.credentials[?type=otp].seed"},
			},
		}
	}
	password := map[string]interface{}{"type": "password", "temporary": false, "pw": "pw-val"}
	otp := map[string]interface{}{"type": "otp", "userLabel": "OTP", "credentialData": "{}", "seed": "seed-val"}

	mgWith := func(creds ...interface{}) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]interface{}{
			"spec": map[string]interface{}{"username": "alice", "credentials": creds},
		}}
	}

	for _, tc := range []struct {
		name  string
		creds []interface{}
	}{
		{"password first", []interface{}{password, otp}},
		{"otp first", []interface{}{otp, password}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := BuildCallConfig(context.Background(), newCI(), mgWith(tc.creds...), nil, nil)

			creds, ok := got.Body.(map[string]interface{})["credentials"].([]interface{})
			if !ok || len(creds) != 2 {
				t.Fatalf("expected 2 credentials in body, got %#v", got.Body.(map[string]interface{})["credentials"])
			}

			byType := map[string]map[string]interface{}{}
			for _, c := range creds {
				m := c.(map[string]interface{})
				byType[m["type"].(string)] = m
			}

			if byType["password"]["value"] != "pw-val" {
				t.Errorf("password credential: want value=pw-val, got %#v", byType["password"])
			}
			if _, leaked := byType["otp"]["value"]; leaked {
				t.Errorf("password value written into the otp credential: %#v", byType["otp"])
			}
			if byType["otp"]["secretData"] != "seed-val" {
				t.Errorf("otp credential: want secretData=seed-val, got %#v", byType["otp"])
			}
			if _, leaked := byType["password"]["secretData"]; leaked {
				t.Errorf("otp secretData written into the password credential: %#v", byType["password"])
			}
			// Unmapped siblings survive, exactly as in the positional case.
			if byType["password"]["temporary"] != false || byType["otp"]["credentialData"] != "{}" {
				t.Errorf("unmapped siblings dropped: %#v", byType)
			}
		})
	}
}

// TestBuildCallConfig_PredicateResolverWritesResolvedSecret pins the real Keycloak wiring: a secretRef
// resolver whose body path is a predicate writes the RESOLVED secret value into the matching credential
// and strips the {name,key} ref that only exists to point at the Secret.
func TestBuildCallConfig_PredicateResolverWritesResolvedSecret(t *testing.T) {
	mapping := getter.FieldMappingItem{
		InBody:           "credentials[?type=password].value",
		InCustomResource: "spec.credentials[?type=password].valueSecretRef",
		Resolver: &getter.FieldResolver{
			Type: "secretRef",
			SecretRef: &getter.SecretRefResolver{
				NameFromCustomResource: "spec.credentials[?type=password].valueSecretRef.name",
				KeyFromCustomResource:  "spec.credentials[?type=password].valueSecretRef.key",
			},
		},
	}
	ci := &CallInfo{
		Path:   "/users",
		Method: "POST",
		ReqParams: &RequestedParams{
			Body: text.StringSet{"credentials": {}, "username": {}},
		},
		FieldMapping: []getter.FieldMappingItem{mapping},
	}
	mg := &unstructured.Unstructured{Object: map[string]interface{}{
		"spec": map[string]interface{}{
			"username": "alice",
			"credentials": []interface{}{
				map[string]interface{}{"type": "otp", "credentialData": "{}"},
				map[string]interface{}{"type": "password", "valueSecretRef": map[string]interface{}{"name": "s", "key": "pw"}},
			},
		},
	}}
	resolved := map[string]interface{}{fieldmapping.ResolverKey(mapping): "hunter2"}

	got := BuildCallConfig(context.Background(), ci, mg, nil, resolved)

	creds := got.Body.(map[string]interface{})["credentials"].([]interface{})
	pw := creds[1].(map[string]interface{})
	if pw["type"] != "password" {
		t.Fatalf("expected the password credential at index 1, got %#v", pw)
	}
	if pw["value"] != "hunter2" {
		t.Errorf("resolved secret not written: %#v", pw)
	}
	if _, still := pw["valueSecretRef"]; still {
		t.Errorf("resolver source not stripped from the body: %#v", pw)
	}
	if _, leaked := creds[0].(map[string]interface{})["value"]; leaked {
		t.Errorf("secret written into the otp credential: %#v", creds[0])
	}
}

func TestBuildCallConfig_SuccessCodesPropagated(t *testing.T) {
	ci := &CallInfo{
		Path:         "/test/{id}",
		Method:       "POST",
		ReqParams:    &RequestedParams{Parameters: text.NewStringSet("id"), Query: text.NewStringSet(), Body: text.NewStringSet()},
		SuccessCodes: []int{201, 202},
	}
	mg := &unstructured.Unstructured{Object: map[string]interface{}{"spec": map[string]interface{}{}, "status": map[string]interface{}{}}}

	rc := BuildCallConfig(context.Background(), ci, mg, nil, nil)
	if assert.NotNil(t, rc) {
		assert.Equal(t, []int{201, 202}, rc.SuccessCodes, "per-verb successCodes must reach the request configuration")
	}
}

func TestBuildCallConfig_HeadersInjected(t *testing.T) {
	ci := &CallInfo{
		Path:      "/test/{id}",
		Method:    "POST",
		ReqParams: &RequestedParams{Parameters: text.NewStringSet("id"), Query: text.NewStringSet(), Body: text.NewStringSet()},
		Headers: []getter.HeaderItem{
			{Name: "Accept", Value: "application/vnd.github.v3.repository+json"},
			{Name: "Content-Type", Value: "application/json"},
		},
	}
	mg := &unstructured.Unstructured{Object: map[string]interface{}{"spec": map[string]interface{}{}, "status": map[string]interface{}{}}}

	rc := BuildCallConfig(context.Background(), ci, mg, nil, nil)
	if assert.NotNil(t, rc) {
		assert.Equal(t, "application/vnd.github.v3.repository+json", rc.Headers["Accept"])
		assert.Equal(t, "application/json", rc.Headers["Content-Type"])
	}
}

func TestBuildCallConfig_QueriesInjected(t *testing.T) {
	ci := &CallInfo{
		Path:      "/test/{id}",
		Method:    "PUT",
		ReqParams: &RequestedParams{Parameters: text.NewStringSet("id"), Query: text.NewStringSet(), Body: text.NewStringSet()},
		Queries:   []getter.QueryParam{{Name: "api-version", Value: "7.2-preview.7"}},
	}
	mg := &unstructured.Unstructured{Object: map[string]interface{}{"spec": map[string]interface{}{}, "status": map[string]interface{}{}}}

	rc := BuildCallConfig(context.Background(), ci, mg, nil, nil)
	if assert.NotNil(t, rc) {
		assert.Equal(t, "7.2-preview.7", rc.Query["api-version"], "per-verb static query must reach the request configuration")
	}
}

func TestUnresolvedPathParams(t *testing.T) {
	for _, tc := range []struct {
		name   string
		path   string
		params map[string]string
		want   []string
	}{
		{
			name:   "all populated",
			path:   "/admin/realms/{realm}/users/{id}",
			params: map[string]string{"realm": "demo", "id": "abc"},
		},
		{
			// The motivating case: create never succeeded, so status.id was never written.
			name:   "identifier never written to status",
			path:   "/admin/realms/{realm}/users/{id}",
			params: map[string]string{"realm": "demo"},
			want:   []string{"id"},
		},
		{
			// An empty value is NOT a substitution: it would address /users/ instead of /users/{id}.
			name:   "present but empty counts as unresolved",
			path:   "/admin/realms/{realm}/users/{id}",
			params: map[string]string{"realm": "demo", "id": ""},
			want:   []string{"id"},
		},
		{
			name:   "reports every missing placeholder in order",
			path:   "/admin/realms/{realm}/users/{id}",
			params: map[string]string{},
			want:   []string{"realm", "id"},
		},
		{
			// A parameterless delete is addressable without any identifier — it must NOT be skipped.
			name:   "no placeholders",
			path:   "/admin/session",
			params: map[string]string{},
		},
		{
			name:   "unterminated placeholder does not panic",
			path:   "/users/{id",
			params: map[string]string{},
		},
		{
			name:   "empty placeholder name is ignored",
			path:   "/users/{}",
			params: map[string]string{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := UnresolvedPathParams(tc.path, tc.params)
			if len(got) != len(tc.want) {
				t.Fatalf("got %#v, want %#v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %#v, want %#v", got, tc.want)
				}
			}
		})
	}
}
