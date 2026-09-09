package restclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/pb33f/libopenapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func createTestClient(t *testing.T) *UnstructuredClient {
	f, err := os.Open("testdata/openapi.yaml")
	require.NoError(t, err)
	defer f.Close()
	b, err := io.ReadAll(f)
	require.NoError(t, err)
	doc, err := libopenapi.NewDocument(b)
	require.NoError(t, err)

	model, errs := doc.BuildV3Model()
	require.Empty(t, errs)

	return &UnstructuredClient{
		//Doc:       doc, // TODO: to be re-enabled when libopenapi-validation is more stable
		DocScheme: model,
		Server:    "https://example.com",
	}
}

func TestPathValidation(t *testing.T) {
	client := createTestClient(t)

	// This should get errors because the path does not exist in the OpenAPI document
	_, err := client.Call(context.Background(), &http.Client{}, "/api/nonexistent", &RequestConfiguration{Method: "GET"})
	assert.Error(t, err)
}

func TestCallWithRecorder(t *testing.T) {
	tests := []struct {
		name          string
		handler       http.HandlerFunc
		path          string
		opts          *RequestConfiguration
		clientSetup   func(*UnstructuredClient)
		expected      interface{}
		expectedError string
		expectedURL   string
	}{
		{
			name: "path with slash in path parameter",
			handler: func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "GET", r.Method)
				r.URL, _ = url.Parse(r.URL.String())
				assert.Equal(t, "/api/test/123%2F456", r.URL.Path)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(map[string]interface{}{"message": "success"})
			},
			path: "/api/test/{id}",
			opts: &RequestConfiguration{
				Method: "GET",
				Parameters: map[string]string{
					"id": "123%2F456",
				},
			},
			expected: map[string]interface{}{"message": "success"},
		},
		{
			name: "successful GET request",
			handler: func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "GET", r.Method)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(map[string]interface{}{"message": "success"})
			},
			path: "/api/test",
			opts: &RequestConfiguration{
				Method: "GET",
			},
			expected: map[string]interface{}{"message": "success"},
		},
		{
			name: "successful POST request with body",
			handler: func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "POST", r.Method)
				assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

				var body map[string]interface{}
				err := json.NewDecoder(r.Body).Decode(&body)
				require.NoError(t, err)
				assert.Equal(t, "test", body["name"])

				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				json.NewEncoder(w).Encode(map[string]interface{}{"id": "123"})
			},
			path: "/api/test",
			opts: &RequestConfiguration{
				Method: "POST",
				Body:   map[string]interface{}{"name": "test"},
			},
			expected: map[string]interface{}{"id": "123"},
		},
		{
			name: "error for invalid status code",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"error": "invalid request",
				})
			},
			path: "/api/test",
			opts: &RequestConfiguration{
				Method: "GET",
			},
			expectedError: "unexpected status: 400: invalid status code: 400",
		},
		{
			name: "non-standard success code rejected without successCodes",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(250)
				json.NewEncoder(w).Encode(map[string]interface{}{"ok": true})
			},
			path: "/api/test",
			opts: &RequestConfiguration{
				Method: "GET",
			},
			expectedError: "invalid status code: 250",
		},
		{
			name: "non-standard success code accepted via successCodes",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(250)
				json.NewEncoder(w).Encode(map[string]interface{}{"ok": true})
			},
			path: "/api/test",
			opts: &RequestConfiguration{
				Method:       "GET",
				SuccessCodes: []int{250},
			},
			expected: map[string]interface{}{"ok": true},
		},
		{
			name: "successful GET with basic auth",
			handler: func(w http.ResponseWriter, r *http.Request) {
				username, password, ok := r.BasicAuth()
				assert.True(t, ok)
				assert.Equal(t, "testuser", username)
				assert.Equal(t, "testpass", password)

				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(map[string]interface{}{"message": "success"})
			},
			path: "/api/test",
			opts: &RequestConfiguration{
				Method: "GET",
			},
			clientSetup: func(c *UnstructuredClient) {
				c.SetAuth = func(req *http.Request) {
					req.SetBasicAuth("testuser", "testpass")
				}
			},
			expected: map[string]interface{}{"message": "success"},
		},
		{
			name: "successful POST with bearer token",
			handler: func(w http.ResponseWriter, r *http.Request) {
				authHeader := r.Header.Get("Authorization")
				assert.Equal(t, "Bearer testtoken123", authHeader)

				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				json.NewEncoder(w).Encode(map[string]interface{}{"id": "123"})
			},
			path: "/api/test",
			opts: &RequestConfiguration{
				Method: "POST",
				Body:   map[string]interface{}{"name": "test"},
			},
			clientSetup: func(c *UnstructuredClient) {
				c.SetAuth = func(req *http.Request) {
					req.Header.Set("Authorization", "Bearer "+"testtoken123")
				}
			},
			expected: map[string]interface{}{"id": "123"},
		},
		{
			name: "unauthorized with invalid credentials",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusUnauthorized)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"error": "invalid credentials",
				})
			},
			path: "/api/test",
			opts: &RequestConfiguration{
				Method: "GET",
			},
			clientSetup: func(c *UnstructuredClient) {

				c.SetAuth = func(req *http.Request) {
					req.SetBasicAuth("invaliduser", "invalidpass")
				}

			},
			expectedError: "unexpected status: 401: invalid status code: 401",
		},
		{
			name: "server override in operation",
			handler: func(w http.ResponseWriter, r *http.Request) {
				// Verify that the request is using the override server URL
				// The mock transport doesn't actually change the host, but we can verify
				// the path and that the request was made
				assert.Equal(t, "GET", r.Method)
				assert.Equal(t, "/api/override", r.URL.Path)

				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(map[string]interface{}{"message": "override success"})
			},
			path: "/api/override",
			opts: &RequestConfiguration{
				Method: "GET",
			},
			expected:    map[string]interface{}{"message": "override success"},
			expectedURL: "http://override.example.com/api/override",
		},
		{
			name: "success with empty body and 200 status code",
			handler: func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "DELETE", r.Method)
				w.WriteHeader(http.StatusOK) // 200 OK
				// No body written
			},
			path: "/api/test/{id}",
			opts: &RequestConfiguration{
				Method: "DELETE",
				Parameters: map[string]string{
					"id": "123",
				},
			},
			expected: nil, // 200 with empty body should return nil
		},
		{
			// Regression guard for issue #27 Bug 1: the client MUST forward opts.Body on DELETE. An earlier
			// httplib-based client sent DELETE as URL-only, so a delete operation declaring a requestBody
			// (e.g. GitHub contents-delete needs {message, sha}) sent nothing -> 422 -> stuck finalizer.
			name: "DELETE forwards its request body (regression: #27)",
			handler: func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "DELETE", r.Method)
				assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
				var body map[string]interface{}
				require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
				assert.Equal(t, "tear down", body["message"])
				assert.Equal(t, "abc123", body["sha"])
				w.WriteHeader(http.StatusOK)
			},
			path: "/api/test/{id}",
			opts: &RequestConfiguration{
				Method:     "DELETE",
				Parameters: map[string]string{"id": "42"},
				Body:       map[string]interface{}{"message": "tear down", "sha": "abc123"},
			},
			expected: nil, // 200 with empty body
		},
		{
			name: "success with empty body and 201 status code",
			handler: func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "POST", r.Method)
				w.WriteHeader(http.StatusCreated) // 201 Created
				// No body written (some APIs return only Location header)
			},
			path: "/api/test",
			opts: &RequestConfiguration{
				Method: "POST",
				Body:   map[string]interface{}{"name": "test"},
			},
			expected: nil, // 201 with empty body should return nil
		},
		{
			name: "success with empty body and 202 status code",
			handler: func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "POST", r.Method)
				w.WriteHeader(http.StatusAccepted) // 202 Accepted
				// No body written
			},
			path: "/api/test",
			opts: &RequestConfiguration{
				Method: "POST",
				Body:   map[string]interface{}{"name": "test"},
			},
			expected: nil, // 202 with empty body should return nil
		},
		{
			name: "error with empty body and 206 status code",
			handler: func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "GET", r.Method)
				w.WriteHeader(http.StatusPartialContent) // 206 Partial Content
				// No body written - 206 should always have content
			},
			path: "/api/test/{id}",
			opts: &RequestConfiguration{
				Method: "GET",
				Parameters: map[string]string{
					"id": "123",
				},
			},
			expectedError: "response body is empty for a status code which requires a body: 206",
		},
		{
			name: "success with empty body and 204 status code",
			handler: func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "PUT", r.Method)
				w.WriteHeader(http.StatusNoContent) // 204 No Content
				// No body written
			},
			path: "/api/test/{id}",
			opts: &RequestConfiguration{
				Method: "PUT",
				Parameters: map[string]string{
					"id": "123",
				},
				Body: map[string]interface{}{"name": "test"},
			},
			expected: nil, // 204 should return nil
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create our mock transport that uses the ResponseRecorder
			mockTransport := &mockTransportImpl{
				handler: tt.handler,
			}

			// If we need to verify the URL (we set the field in the test case), capture it
			if tt.expectedURL != "" {
				mockTransport.capturedURL = ""
			}

			// Create test client
			client := createTestClient(t)
			if tt.clientSetup != nil {
				tt.clientSetup(client)
			}
			testClient := &http.Client{
				Transport: mockTransport,
			}

			// Call the method under test
			result, err := client.Call(context.Background(), testClient, tt.path, tt.opts)

			// Verify the URL if expected, if we set the field in the test case
			if tt.expectedURL != "" {
				assert.Equal(t, tt.expectedURL, mockTransport.capturedURL)
			}

			if tt.expectedError != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectedError)
				return
			}

			require.NoError(t, err)

			// We expect nil for 204 and 304 responses and no error
			// if tt.expected == nil {
			// 	assert.Nil(t, result)
			// 	return
			// }

			if result.ResponseBody == nil {
				assert.Nil(t, tt.expected)
				return
			}

			switch v := result.ResponseBody.(type) {
			case map[string]interface{}:
				assert.Equal(t, tt.expected, v)
			case []interface{}:
				assert.Equal(t, tt.expected, v)
			default:
				t.Errorf("unexpected result type: %T", result)
			}
		})
	}
}
func TestCallAdditionalCases(t *testing.T) {
	tests := []struct {
		name          string
		handler       http.HandlerFunc
		path          string
		opts          *RequestConfiguration
		clientSetup   func(*UnstructuredClient)
		expected      interface{}
		expectedError string
	}{
		{
			name: "operation not found for method",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			},
			path: "/api/test",
			opts: &RequestConfiguration{
				Method: "PATCH", // Method not defined in the OpenAPI spec
			},
			expectedError: "operation not found for method PATCH at path /api/test",
		},
		{
			name: "invalid body type",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			},
			path: "/api/test",
			opts: &RequestConfiguration{
				Method: "POST",
				Body:   "invalid body type", // Should be map[string]any
			},
			expectedError: "invalid body type: string",
		},
		{
			name: "failed to read response body",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				// This will be handled by the mock transport to simulate read error
			},
			path: "/api/test",
			opts: &RequestConfiguration{
				Method: "GET",
			},
			clientSetup: func(c *UnstructuredClient) {
				// Will be handled by special mock transport
			},
			expectedError: "failed to read response body",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var mockTransport http.RoundTripper

			if tt.name == "failed to read response body" {
				// Special case: simulate body read error
				mockTransport = &errorBodyTransport{handler: tt.handler}
			} else {
				mockTransport = &mockTransportImpl{handler: tt.handler}
			}

			client := createTestClient(t)
			if tt.clientSetup != nil {
				tt.clientSetup(client)
			}

			testClient := &http.Client{
				Transport: mockTransport,
			}

			result, err := client.Call(context.Background(), testClient, tt.path, tt.opts)

			if tt.expectedError != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectedError)
				return
			}

			require.NoError(t, err)
			if tt.expected != nil {
				switch v := result.ResponseBody.(type) {
				case *map[string]interface{}:
					assert.Equal(t, tt.expected, *v)
				default:
					t.Errorf("unexpected result type: %T", result)
				}
			}
		})
	}
}

func TestFindBy(t *testing.T) {
	tests := []struct {
		name             string
		handler          http.HandlerFunc
		path             string
		opts             *RequestConfiguration
		clientSetup      func(*UnstructuredClient)
		expected         interface{}
		expectedError    string
		identifierFields []string
		mg               map[string]interface{}
	}{
		{
			name: "successful find by identifier",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode([]interface{}{
					map[string]interface{}{"id": "1", "name": "item1"},
					map[string]interface{}{"id": "2", "name": "item2"},
					map[string]interface{}{"id": "target", "name": "target_item"},
				},
				)
			},
			path: "/api/test",
			opts: &RequestConfiguration{
				Method: "GET",
			},
			identifierFields: []string{"id"},
			mg:               map[string]interface{}{"spec": map[string]interface{}{"id": "target"}},
			expected:         map[string]interface{}{"id": "target", "name": "target_item"},
		},
		{
			name: "successful find by identifier on status",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode([]interface{}{
					map[string]interface{}{"id": "1", "name": "item1"},
					map[string]interface{}{"id": "2", "name": "item2"},
					map[string]interface{}{"id": "target", "name": "target_item"},
				},
				)
			},
			path: "/api/test",
			opts: &RequestConfiguration{
				Method: "GET",
			},
			identifierFields: []string{"id"},
			mg:               map[string]interface{}{"spec": map[string]interface{}{"name": "test"}, "status": map[string]interface{}{"id": "target"}},
			expected:         map[string]interface{}{"id": "target", "name": "target_item"},
		},
		{
			name: "find by nested identifier",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"items": []interface{}{
						map[string]interface{}{
							"metadata": map[string]interface{}{"name": "item1"},
							"spec":     map[string]interface{}{"value": "test1"},
						},
						map[string]interface{}{
							"metadata": map[string]interface{}{"name": "target"},
							"spec":     map[string]interface{}{"value": "test2"},
						},
					},
				})
			},
			path: "/api/test",
			opts: &RequestConfiguration{
				Method: "GET",
			},
			identifierFields: []string{"metadata.name"},
			mg: map[string]interface{}{
				"spec": map[string]interface{}{
					"metadata": map[string]interface{}{
						"name": "target"}}},
			expected: map[string]interface{}{
				"metadata": map[string]interface{}{"name": "target"},
				"spec":     map[string]interface{}{"value": "test2"},
			},
		},
		{
			name: "item not found",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"items": []interface{}{
						map[string]interface{}{"id": "1", "name": "item1"},
						map[string]interface{}{"id": "2", "name": "item2"},
					},
				})
			},
			path: "/api/test",
			opts: &RequestConfiguration{
				Method: "GET",
			},
			identifierFields: []string{"id"},
			mg:               map[string]interface{}{"spec": map[string]interface{}{"id": "nonexistent"}},
			expectedError:    "item not found",
		},
		{
			name: "empty response",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode([]interface{}{})
			},
			path: "/api/test",
			opts: &RequestConfiguration{
				Method: "GET",
			},
			identifierFields: []string{"id"},
			mg:               map[string]interface{}{"spec": map[string]interface{}{"id": "target"}},
			expectedError:    "item not found",
		},
		{
			name: "Call method returns error",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
			},
			path: "/api/test",
			opts: &RequestConfiguration{
				Method: "GET",
			},
			identifierFields: []string{"id"},
			mg:               map[string]interface{}{"id": "target"},
			expectedError:    "unexpected status: 500",
		},
		{
			name: "non-string identifier value",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"items": []interface{}{
						map[string]interface{}{"id": 123, "name": "item1"},
						map[string]interface{}{"id": 456, "name": "item2"},
					},
				})
			},
			path: "/api/test",
			opts: &RequestConfiguration{
				Method: "GET",
			},
			identifierFields: []string{"id"},
			mg:               map[string]interface{}{"spec": map[string]interface{}{"id": 456}},
			expected:         map[string]interface{}{"id": 456, "name": "item2"},
		},
		{
			name: "object as identifier value",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"items": []interface{}{
						map[string]interface{}{"id": map[string]interface{}{"part1": "a", "part2": "b"}, "name": "item1"},
						map[string]interface{}{"id": map[string]interface{}{"part1": "a", "part2": "d"}, "name": "item2"},
					},
				})
			},
			path: "/api/test",
			opts: &RequestConfiguration{
				Method: "GET",
			},
			identifierFields: []string{"id"},
			mg:               map[string]interface{}{"spec": map[string]interface{}{"id": map[string]interface{}{"part1": "a", "part2": "d"}}},
			expected:         map[string]interface{}{"id": map[string]interface{}{"part1": "a", "part2": "d"}, "name": "item2"},
		},
		{
			name: "object as identifier value with nested fields",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"items": []interface{}{
						map[string]interface{}{
							"id":   "policy-1",
							"name": "PolicyConfig1",
							"settings": map[string]interface{}{
								"addedFilesOnly":       true,
								"creatorVoteCounts":    true,
								"minimumApproverCount": 1,
								"filenamePatterns":     []interface{}{"**/*.go", "**/*.md"},
								"requiredReviewerIds":  []interface{}{"32915def-23aa-609f-b91d-9a8d94384aa2"},
								"scope": []interface{}{
									map[string]interface{}{
										"repositoryId": nil,
										"refName":      "refs/heads/main",
										"matchKind":    "Exact",
									},
									map[string]interface{}{
										"repositoryId": nil,
										"refName":      "refs/heads/release/*",
										"matchKind":    "Prefix",
									},
								},
							},
						},
						map[string]interface{}{
							"id":   "policy-2",
							"name": "PolicyConfig2",
							"settings": map[string]interface{}{
								"addedFilesOnly":       true,
								"creatorVoteCounts":    true,
								"minimumApproverCount": 1,
								"filenamePatterns":     []interface{}{"**/*.go", "**/*.md"},
								"requiredReviewerIds":  []interface{}{"32915def-23aa-609f-b91d-9a8d94384aa2"},
								"scope": []interface{}{
									map[string]interface{}{
										"repositoryId": nil,
										"refName":      "refs/heads/release/*",
										"matchKind":    "Prefix",
									},
								},
							},
						},
					},
				})
			},
			path: "/api/test",
			opts: &RequestConfiguration{
				Method: "GET",
			},
			identifierFields: []string{"settings"},
			mg: map[string]interface{}{
				"spec": map[string]interface{}{
					"settings": map[string]interface{}{
						"addedFilesOnly":       true,
						"creatorVoteCounts":    true,
						"minimumApproverCount": 1,
						"filenamePatterns":     []interface{}{"**/*.go", "**/*.md"},
						"requiredReviewerIds":  []interface{}{"32915def-23aa-609f-b91d-9a8d94384aa2"},
						"scope": []interface{}{
							map[string]interface{}{
								"repositoryId": nil,
								"refName":      "refs/heads/main",
								"matchKind":    "Exact",
							},
							map[string]interface{}{
								"repositoryId": nil,
								"refName":      "refs/heads/release/*",
								"matchKind":    "Prefix",
							},
						},
					},
				},
			},
			expected: map[string]interface{}{
				"id":   "policy-1",
				"name": "PolicyConfig1",
				"settings": map[string]interface{}{
					"addedFilesOnly":       true,
					"creatorVoteCounts":    true,
					"minimumApproverCount": 1,
					"filenamePatterns":     []interface{}{"**/*.go", "**/*.md"},
					"requiredReviewerIds":  []interface{}{"32915def-23aa-609f-b91d-9a8d94384aa2"},
					"scope": []interface{}{
						map[string]interface{}{
							"repositoryId": nil,
							"refName":      "refs/heads/main",
							"matchKind":    "Exact",
						},
						map[string]interface{}{
							"repositoryId": nil,
							"refName":      "refs/heads/release/*",
							"matchKind":    "Prefix",
						},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockTransport := &mockTransportImpl{
				handler: tt.handler,
			}

			client := createTestClient(t)
			client.IdentifierFields = tt.identifierFields

			u := unstructured.Unstructured{}
			u.SetUnstructuredContent(tt.mg)
			client.Resource = &u

			if tt.clientSetup != nil {
				tt.clientSetup(client)
			}

			testClient := &http.Client{
				Transport: mockTransport,
			}

			result, err := client.FindBy(context.Background(), testClient, tt.path, tt.opts, nil)

			if tt.expectedError != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectedError)
				return
			}

			require.NoError(t, err)

			if tt.expected == nil {
				assert.Nil(t, result)
				return
			}

			switch v := result.ResponseBody.(type) {
			case *map[string]interface{}:
				assert.Equal(t, tt.expected, *v)
			default:
				assert.Equal(t, tt.expected, result.ResponseBody)
			}
		})
	}
}

// errorBodyTransport simulates an error when reading the response body
type errorBodyTransport struct {
	handler http.HandlerFunc
}

func (e *errorBodyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	rr := httptest.NewRecorder()
	e.handler(rr, req)

	resp := rr.Result()
	// Replace the body with an error-producing reader
	resp.Body = &errorReader{}
	return resp, nil
}

// errorReader always returns an error when Read is called
type errorReader struct{}

func (e *errorReader) Read(p []byte) (n int, err error) {
	return 0, fmt.Errorf("simulated read error")
}

func (e *errorReader) Close() error {
	return nil
}

// mockTransportImpl implements http.RoundTripper using a ResponseRecorder
type mockTransportImpl struct {
	handler     http.HandlerFunc
	capturedURL string
}

func (m *mockTransportImpl) RoundTrip(req *http.Request) (*http.Response, error) {
	// Capture the full URL for verification
	if m.capturedURL == "" {
		m.capturedURL = req.URL.String()
	}

	// Create a ResponseRecorder
	rr := httptest.NewRecorder()

	// Call our handler with the recorder
	m.handler(rr, req)

	// Return the recorded response
	return rr.Result(), nil
}

func TestResponse_IsPending(t *testing.T) {
	tests := []struct {
		name     string
		response *Response
		want     bool
	}{
		{
			name:     "should be true for StatusProcessing",
			response: &Response{statusCode: http.StatusProcessing},
			want:     true,
		},
		{
			name:     "should be true for StatusContinue",
			response: &Response{statusCode: http.StatusContinue},
			want:     true,
		},
		{
			name:     "should be true for StatusAccepted",
			response: &Response{statusCode: http.StatusAccepted},
			want:     true,
		},
		{
			name:     "should be false for StatusOK",
			response: &Response{statusCode: http.StatusOK},
			want:     false,
		},
		{
			name:     "should be false for StatusBadRequest",
			response: &Response{statusCode: http.StatusBadRequest},
			want:     false,
		},
		{
			name:     "should be false for nil response",
			response: nil,
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.response.IsPending(); got != tt.want {
				t.Errorf("Response.IsPending() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestExtractItemsFromResponse(t *testing.T) {
	singleItem := map[string]interface{}{"id": 1}
	tests := []struct {
		name    string
		body    interface{}
		want    []interface{}
		wantErr bool
	}{
		{
			name: "standard list response",
			body: []interface{}{singleItem, map[string]interface{}{"id": 2}},
			want: []interface{}{singleItem, map[string]interface{}{"id": 2}},
		},
		{
			name: "wrapped list response",
			body: map[string]interface{}{"items": []interface{}{singleItem}, "count": 1},
			want: []interface{}{singleItem},
		},
		{
			name: "single object response",
			body: singleItem,
			want: []interface{}{singleItem},
		},
		{
			name: "empty list response",
			body: []interface{}{},
			want: []interface{}{},
		},
		{
			name: "empty object response",
			body: map[string]interface{}{},
			want: []interface{}{},
		},
		{
			name:    "nil body",
			body:    nil,
			wantErr: true,
		},
		{
			name:    "invalid body type",
			body:    "a string",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ExtractItemsFromResponse(tt.body)
			if (err != nil) != tt.wantErr {
				t.Errorf("ExtractItemsFromResponse() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			assert.Equal(t, tt.want, got, "ExtractItemsFromResponse() got = %v, want %v", got, tt.want)
		})
	}
}

func TestFindItemInList(t *testing.T) {
	item1 := map[string]interface{}{"name": "one", "value": 1}
	item2 := map[string]interface{}{"name": "two", "value": 2}

	tests := []struct {
		name      string
		items     []interface{}
		client    *UnstructuredClient
		wantItem  map[string]interface{}
		wantFound bool
	}{
		{
			name:  "match found",
			items: []interface{}{item1, item2},
			client: &UnstructuredClient{
				IdentifierFields: []string{"name"},
				Resource: &unstructured.Unstructured{Object: map[string]interface{}{
					"spec": map[string]interface{}{"name": "two"},
				}},
			},
			wantItem:  item2,
			wantFound: true,
		},
		{
			name:  "no match found",
			items: []interface{}{item1, item2},
			client: &UnstructuredClient{
				IdentifierFields: []string{"name"},
				Resource: &unstructured.Unstructured{Object: map[string]interface{}{
					"spec": map[string]interface{}{"name": "three"},
				}},
			},
			wantItem:  nil,
			wantFound: false,
		},
		{
			name:      "empty item list",
			items:     []interface{}{},
			client:    &UnstructuredClient{},
			wantItem:  nil,
			wantFound: false,
		},
		{
			name:  "list with non-object items",
			items: []interface{}{"a string", 123, nil, item2},
			client: &UnstructuredClient{
				IdentifierFields: []string{"name"},
				Resource: &unstructured.Unstructured{Object: map[string]interface{}{
					"spec": map[string]interface{}{"name": "two"},
				}},
			},
			wantItem:  item2,
			wantFound: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotItem, gotFound := tt.client.findItemInList(tt.items)
			assert.Equal(t, tt.wantFound, gotFound)
			assert.Equal(t, tt.wantItem, gotItem)
		})
	}
}

func TestIsItemMatch(t *testing.T) {
	tests := []struct {
		name      string
		client    *UnstructuredClient
		itemMap   map[string]interface{}
		wantMatch bool
		wantErr   bool
	}{
		// --- AND Policy Tests ---
		{
			name: "AND policy, all identifiers match",
			client: &UnstructuredClient{
				IdentifiersMatchPolicy: "AND",
				IdentifierFields:       []string{"name", "region"},
				Resource: &unstructured.Unstructured{Object: map[string]interface{}{
					"spec": map[string]interface{}{"name": "test-vm", "region": "us-east-1"},
				}},
			},
			itemMap:   map[string]interface{}{"name": "test-vm", "region": "us-east-1"},
			wantMatch: true,
		},
		{
			name: "AND policy, one identifier does not match",
			client: &UnstructuredClient{
				IdentifiersMatchPolicy: "AND",
				IdentifierFields:       []string{"name", "region"},
				Resource: &unstructured.Unstructured{Object: map[string]interface{}{
					"spec": map[string]interface{}{"name": "test-vm", "region": "us-east-1"},
				}},
			},
			itemMap:   map[string]interface{}{"name": "test-vm", "region": "us-west-2"},
			wantMatch: false,
		},
		{
			name: "AND policy, one identifier is missing from item",
			client: &UnstructuredClient{
				IdentifiersMatchPolicy: "AND",
				IdentifierFields:       []string{"name", "region"},
				Resource: &unstructured.Unstructured{Object: map[string]interface{}{
					"spec": map[string]interface{}{"name": "test-vm", "region": "us-east-1"},
				}},
			},
			itemMap:   map[string]interface{}{"name": "test-vm"},
			wantMatch: false,
		},

		// --- OR Policy Tests ---
		{
			name: "OR policy, first identifier matches",
			client: &UnstructuredClient{
				IdentifiersMatchPolicy: "OR",
				IdentifierFields:       []string{"name", "id"},
				Resource: &unstructured.Unstructured{Object: map[string]interface{}{
					"spec": map[string]interface{}{"name": "test-vm", "id": "vm-stale"},
				}},
			},
			itemMap:   map[string]interface{}{"name": "test-vm", "id": "vm-123"},
			wantMatch: true,
		},
		{
			name: "OR policy, second identifier matches",
			client: &UnstructuredClient{
				IdentifiersMatchPolicy: "OR",
				IdentifierFields:       []string{"name", "id"},
				Resource: &unstructured.Unstructured{Object: map[string]interface{}{
					"spec": map[string]interface{}{"name": "stale-name", "id": "vm-123"},
				}},
			},
			itemMap:   map[string]interface{}{"name": "test-vm", "id": "vm-123"},
			wantMatch: true,
		},
		{
			name: "OR policy, no identifiers match",
			client: &UnstructuredClient{
				IdentifiersMatchPolicy: "OR",
				IdentifierFields:       []string{"name", "id"},
				Resource: &unstructured.Unstructured{Object: map[string]interface{}{
					"spec": map[string]interface{}{"name": "stale-name", "id": "vm-stale"},
				}},
			},
			itemMap:   map[string]interface{}{"name": "test-vm", "id": "vm-123"},
			wantMatch: false,
		},

		// --- Edge Cases ---
		{
			name: "No identifiers specified",
			client: &UnstructuredClient{
				IdentifiersMatchPolicy: "AND",
				IdentifierFields:       []string{},
				Resource:               &unstructured.Unstructured{Object: map[string]interface{}{}},
			},
			itemMap:   map[string]interface{}{"name": "test-vm"},
			wantMatch: false,
		},
		{
			name: "Default policy (empty) is OR, match succeeds on partial match",
			client: &UnstructuredClient{
				IdentifiersMatchPolicy: "", // Default should be OR
				IdentifierFields:       []string{"name", "region"},
				Resource: &unstructured.Unstructured{Object: map[string]interface{}{
					"spec": map[string]interface{}{"name": "test-vm", "region": "us-east-1"},
				}},
			},
			itemMap:   map[string]interface{}{"name": "test-vm", "region": "us-west-2"}, // name matches, region doesn't
			wantMatch: true,                                                             // Should be true with OR logic
		},
		{
			name: "AND policy explicitly set, match fails on partial match",
			client: &UnstructuredClient{
				IdentifiersMatchPolicy: "And",
				IdentifierFields:       []string{"name", "region"},
				Resource: &unstructured.Unstructured{Object: map[string]interface{}{
					"spec": map[string]interface{}{"name": "test-vm", "region": "us-east-1"},
				}},
			},
			itemMap:   map[string]interface{}{"name": "test-vm", "region": "us-west-2"},
			wantMatch: false, // Should be false with AND logic
		},

		// --- Complex Identifier Tests ---
		{
			name: "AND policy, nested object identifier matches",
			client: &UnstructuredClient{
				IdentifiersMatchPolicy: "AND",
				IdentifierFields:       []string{"metadata.labels"},
				Resource: &unstructured.Unstructured{Object: map[string]interface{}{
					"spec": map[string]interface{}{"metadata": map[string]interface{}{
						"labels": map[string]string{"app": "database", "tier": "backend"},
					}},
				}},
			},
			itemMap: map[string]interface{}{"metadata": map[string]interface{}{
				"labels": map[string]string{"tier": "backend", "app": "database"}, // Key order is different
			}},
			wantMatch: true, // DeepEqual should handle map key order
		},
		{
			name: "AND policy, nested object identifier fails",
			client: &UnstructuredClient{
				IdentifiersMatchPolicy: "AND",
				IdentifierFields:       []string{"metadata.labels"},
				Resource: &unstructured.Unstructured{Object: map[string]interface{}{
					"spec": map[string]interface{}{"metadata": map[string]interface{}{
						"labels": map[string]string{"app": "database", "tier": "backend"},
					}},
				}},
			},
			itemMap: map[string]interface{}{"metadata": map[string]interface{}{
				"labels": map[string]string{"app": "database", "tier": "frontend"}, // One value is different
			}},
			wantMatch: false,
		},
		{
			name: "OR policy, array identifier matches",
			client: &UnstructuredClient{
				IdentifiersMatchPolicy: "OR",
				IdentifierFields:       []string{"ports", "name"},
				Resource: &unstructured.Unstructured{Object: map[string]interface{}{
					"spec": map[string]interface{}{
						"name":  "stale-name",
						"ports": []interface{}{80, 443},
					},
				}},
			},
			itemMap:   map[string]interface{}{"name": "test-svc", "ports": []interface{}{80, 443}},
			wantMatch: true,
		},
		{
			name: "OR policy, array identifier fails (order matters)",
			client: &UnstructuredClient{
				IdentifiersMatchPolicy: "OR",
				IdentifierFields:       []string{"ports"}, // Only test the array
				Resource: &unstructured.Unstructured{Object: map[string]interface{}{
					"spec": map[string]interface{}{
						"name":  "test-svc",
						"ports": []interface{}{80, 443},
					},
				}},
			},
			itemMap:   map[string]interface{}{"name": "test-svc", "ports": []interface{}{443, 80}}, // Order is different
			wantMatch: false,                                                                       // DeepEqual for slices is order-sensitive
		},
		{
			name: "Literal dots in identifier fields",
			client: &UnstructuredClient{
				IdentifiersMatchPolicy: "OR",
				IdentifierFields:       []string{"['field.with.dots']"},
				Resource: &unstructured.Unstructured{Object: map[string]interface{}{
					"spec": map[string]interface{}{
						"field.with.dots": "value1",
					},
				}},
			},
			itemMap:   map[string]interface{}{"field.with.dots": "value1"},
			wantMatch: true,
		},
		{
			name: "Literal dots in identifier fields - no match (different value)",
			client: &UnstructuredClient{
				IdentifiersMatchPolicy: "OR",
				IdentifierFields:       []string{"['field.with.dots']"},
				Resource: &unstructured.Unstructured{Object: map[string]interface{}{
					"spec": map[string]interface{}{
						"field.with.dots": "value1",
					},
				}},
			},
			itemMap:   map[string]interface{}{"field.with.dots": "value2"},
			wantMatch: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotMatch, err := tt.client.isItemMatch(tt.itemMap)
			if (err != nil) != tt.wantErr {
				t.Errorf("isItemMatch() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			assert.Equal(t, tt.wantMatch, gotMatch)
		})
	}
}

func TestCall_TolerateCodes(t *testing.T) {
	handler404 := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]interface{}{"error": "not found"})
	}

	t.Run("404 tolerated -> empty success", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(handler404))
		defer srv.Close()
		client := createTestClient(t)
		client.Server = srv.URL

		resp, err := client.Call(context.Background(), srv.Client(), "/api/test",
			&RequestConfiguration{Method: "GET", TolerateCodes: []int{404}})
		require.NoError(t, err, "a tolerated 404 must not error")
		assert.Nil(t, resp.ResponseBody, "tolerated response body is empty")
	})

	t.Run("404 not tolerated -> error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(handler404))
		defer srv.Close()
		client := createTestClient(t)
		client.Server = srv.URL

		_, err := client.Call(context.Background(), srv.Client(), "/api/test",
			&RequestConfiguration{Method: "GET"})
		require.Error(t, err, "an untolerated 404 must error")
	})
}

func TestCall_NotFoundCodes(t *testing.T) {
	handler410 := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusGone) // 410
		json.NewEncoder(w).Encode(map[string]interface{}{"error": "gone"})
	}

	t.Run("410 remapped to not-found via notFoundCodes", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(handler410))
		defer srv.Close()
		client := createTestClient(t)
		client.Server = srv.URL

		_, err := client.Call(context.Background(), srv.Client(), "/api/test",
			&RequestConfiguration{Method: "GET", NotFoundCodes: []int{410}})
		require.Error(t, err)
		assert.True(t, IsNotFoundError(err), "a notFoundCode must be remapped to a not-found result")
	})

	t.Run("410 without notFoundCodes -> generic invalid-status error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(handler410))
		defer srv.Close()
		client := createTestClient(t)
		client.Server = srv.URL

		_, err := client.Call(context.Background(), srv.Client(), "/api/test",
			&RequestConfiguration{Method: "GET"})
		require.Error(t, err)
		assert.False(t, IsNotFoundError(err), "without notFoundCodes a 410 is not a not-found")
	})
}

func TestRedact(t *testing.T) {
	t.Run("replaces every occurrence of every value", func(t *testing.T) {
		in := []byte(`Authorization: Bearer hunter2\nbody: {"token":"hunter2","other":"fine"}`)
		out := redact(in, []string{"hunter2"})
		assert.NotContains(t, string(out), "hunter2")
		assert.Contains(t, string(out), "***REDACTED***")
		assert.Contains(t, string(out), "fine")
	})

	t.Run("empty values are ignored, not treated as a match-everything wildcard", func(t *testing.T) {
		in := []byte(`some request bytes`)
		out := redact(in, []string{""})
		assert.Equal(t, in, out)
	})

	t.Run("no values is a no-op", func(t *testing.T) {
		in := []byte(`some request bytes`)
		out := redact(in, nil)
		assert.Equal(t, in, out)
	})

	t.Run("multiple distinct values are all redacted", func(t *testing.T) {
		in := []byte(`secret-one and secret-two both present`)
		out := redact(in, []string{"secret-one", "secret-two"})
		assert.NotContains(t, string(out), "secret-one")
		assert.NotContains(t, string(out), "secret-two")
	})
}

// TestDebuggingRoundTripper_RedactsSensitiveValues proves the debug transport actually applies
// RedactValues to the dumped request before writing it out, closing the leak path a secretRef-resolved
// value would otherwise take through verbose/debug logging.
func TestDebuggingRoundTripper_RedactsSensitiveValues(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	var out bytes.Buffer
	rt := &debuggingRoundTripper{
		Transport:    http.DefaultTransport,
		Out:          &out,
		RedactValues: []string{"hunter2"},
	}
	cli := &http.Client{Transport: rt}

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/test", strings.NewReader(`{"token":"hunter2"}`))
	require.NoError(t, err)

	_, err = cli.Do(req)
	require.NoError(t, err)

	assert.NotContains(t, out.String(), "hunter2")
	assert.Contains(t, out.String(), "***REDACTED***")
}

// TestCall_QueryValueReachesServerEscapedOnce is the end-to-end proof of the query-escaping contract:
// the value a caller puts in RequestConfiguration.Query must arrive at the upstream server decodable in
// ONE pass. It asserts on the request as the server sees it, so it covers the whole chain
// (buildPath -> url.Values.Encode -> http.Request.URL.RequestURI), not just the URL builder.
//
// The regression it guards is a git ref named "builder/sock-shop" arriving as "builder%252Fsock-shop":
// buildPath used to url.QueryEscape each value before handing it to url.Values, whose Encode() escapes
// again, so the '%' of the first pass became '%25'. GitHub then looked up a ref literally named
// "builder%2Fsock-shop" and answered 404, which stalled RepoContent's observe forever.
func TestCall_QueryValueReachesServerEscapedOnce(t *testing.T) {
	var gotRawQuery, gotDecoded string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRawQuery = r.URL.RawQuery
		gotDecoded = r.URL.Query().Get("query")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode([]map[string]interface{}{})
	}))
	defer srv.Close()

	client := createTestClient(t)
	client.Server = srv.URL

	_, err := client.Call(context.Background(), srv.Client(), "/api/search", &RequestConfiguration{
		Method: "GET",
		Query:  map[string]string{"query": "builder/sock-shop"},
	})
	require.NoError(t, err)

	assert.Equal(t, "query=builder%2Fsock-shop", gotRawQuery, "the query value must be percent-encoded exactly once on the wire")
	assert.Equal(t, "builder/sock-shop", gotDecoded, "one decode at the server must yield the original value")
}
