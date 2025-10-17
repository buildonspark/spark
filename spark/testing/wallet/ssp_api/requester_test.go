package sspapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DataDog/zstd"
	"github.com/Khan/genqlient/graphql"
	"github.com/stretchr/testify/require"

	"github.com/stretchr/testify/assert"
)

func TestValidateBaseURL(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
	}{
		{
			name:    "https URL",
			baseURL: "https://api.example.com",
		},
		{
			name:    "https URL with path",
			baseURL: "https://api.example.com/graphql",
		},
		{
			name:    "https URL with port",
			baseURL: "https://api.example.com:443",
		},
		{
			name:    "https URL with query params",
			baseURL: "https://api.example.com/graphql?version=1",
		},
		{
			name:    "localhost http URL",
			baseURL: "http://localhost:8080",
		},
		{
			name:    "localhost https URL",
			baseURL: "https://localhost:8080",
		},
		{
			name:    "localhost URL without port",
			baseURL: "http://localhost",
		},
		{
			name:    "localhost URL with path",
			baseURL: "http://localhost/graphql",
		},
		{
			name:    ".local TLD http URL",
			baseURL: "http://api.local",
		},
		{
			name:    ".local TLD https URL",
			baseURL: "https://api.local",
		},
		{
			name:    ".local TLD with subdomain",
			baseURL: "http://dev.api.local",
		},
		{
			name:    ".local TLD with port",
			baseURL: "http://api.local:3000",
		},
		{
			name:    ".internal TLD http",
			baseURL: "http://api.internal",
		},
		{
			name:    ".internal TLD https",
			baseURL: "https://api.internal",
		},
		{
			name:    ".internal TLD with subdomain",
			baseURL: "http://dev.api.internal",
		},
		{
			name:    ".internal TLD with port",
			baseURL: "http://api.internal:8080",
		},
		{
			name:    "127.0.0.1 http URL",
			baseURL: "http://127.0.0.1:8080",
		},
		{
			name:    "127.0.0.1 https URL",
			baseURL: "https://127.0.0.1:8080",
		},
		{
			name:    "127.0.0.1 URL without port",
			baseURL: "http://127.0.0.1",
		},
		{
			name:    "127.0.0.1 with path",
			baseURL: "http://127.0.0.1/graphql",
		},
		{
			name:    "127.0.0.2",
			baseURL: "http://127.0.0.1",
		},
		{
			name:    "IPv6",
			baseURL: "http://[::1]",
		},
		{
			name:    "fragment",
			baseURL: "https://api.example.com#section",
		},
		{
			name:    "multiple query params",
			baseURL: "https://api.example.com?param1=value1&param2=value2",
		},
		{
			name:    "special characters in path",
			baseURL: "https://api.example.com/path/with/special/chars/grüßen",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.NoError(t, validateBaseURL(tt.baseURL))
		})
	}
}

func TestValidateBaseURL_Invalid(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		errMsg  string
	}{
		{
			name:    "invalid URL - malformed",
			baseURL: "cache_object:foo/bar",
			errMsg:  `invalid base url: "cache_object:foo/bar`,
		},
		{
			name:    "invalid URL - missing scheme",
			baseURL: "api.example.com",
			errMsg:  `invalid base url: "api.example.com" must be https:// if not targeting localhost`,
		},
		{
			name:    "invalid URL - empty string",
			baseURL: "",
			errMsg:  "base url is empty",
		},
		{
			name:    "invalid HTTP URL - non-localhost",
			baseURL: "http://api.example.com",
			errMsg:  `invalid base url: "http://api.example.com" must be https:// if not targeting localhost`,
		},
		{
			name:    "invalid HTTP URL - non-localhost with port",
			baseURL: "http://api.example.com:8080",
			errMsg:  `invalid base url: "http://api.example.com:8080" must be https:// if not targeting localhost`,
		},
		{
			name:    "invalid HTTP URL - non-localhost with path",
			baseURL: "http://api.example.com/graphql",
			errMsg:  `invalid base url: "http://api.example.com/graphql" must be https:// if not targeting localhost`,
		},
		{
			name:    "invalid HTTP URL - non-localhost with query params",
			baseURL: "http://api.example.com?test=1",
			errMsg:  `invalid base url: "http://api.example.com?test=1" must be https:// if not targeting localhost`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.ErrorContains(t, validateBaseURL(tt.baseURL), tt.errMsg)
		})
	}
}

func TestValidateBaseURL_AllowlistedHosts(t *testing.T) {
	allowlistedHosts := []string{
		"localhost",
		"127.0.0.1",
		"127.5.5.5",
		"[::1]",
		"api.local",
		"dev.api.local",
		"api.internal",
		"dev.api.internal",
	}

	for _, host := range allowlistedHosts {
		t.Run(host, func(t *testing.T) {
			assert.NoError(t, validateBaseURL("http://"+host), "Unexpected error for HTTP URL")
			assert.NoError(t, validateBaseURL("https://"+host), "Unexpected error for HTTPS URL")
		})
	}
}

func TestMakeRequest(t *testing.T) {
	tests := []struct {
		name              string
		opName            string
		query             string
		variables         map[string]any
		identityPublicKey string
		wantData          map[string]any
		wantCompression   bool
	}{
		{
			name:      "simple query",
			opName:    "GetUser",
			query:     "query GetUser { user { id name } }",
			variables: map[string]any{"id": "123"},
			wantData: map[string]any{
				"user": map[string]any{
					"id":   "123",
					"name": "John Doe",
				},
			},
		},
		{
			name:      "mutation success",
			opName:    "CreateUser",
			query:     "mutation CreateUser($name: String!) { createUser(name: $name) { id name } }",
			variables: map[string]any{"name": "Jane Doe"},
			wantData: map[string]any{
				"createUser": map[string]any{
					"id":   "456",
					"name": "Jane Doe",
				},
			},
		},
		{
			name:              "with identity public key",
			opName:            "GetUser",
			query:             "query GetUser { user { id } }",
			variables:         map[string]any{},
			identityPublicKey: "test-public-key",
			wantData:          map[string]any{"user": map[string]any{"id": "789"}},
		},
		{
			name:      "with custom base URL",
			opName:    "GetUser",
			query:     "query GetUser { user { id } }",
			variables: map[string]any{},
			wantData:  map[string]any{"user": map[string]any{"id": "999"}},
		},
		{
			name:      "empty base URL",
			opName:    "GetUser",
			query:     "query GetUser { user { id } }",
			variables: map[string]any{},
			wantData:  map[string]any{"user": map[string]any{"id": "999"}},
		},
		{
			name:   "large payload with compression",
			opName: "GetLargeData",
			query:  "query GetLargeData { largeData { content } }",
			variables: map[string]any{
				"largeParam": string(make([]byte, 2000)), // Large payload to trigger compression
			},
			wantData:        map[string]any{"largeData": map[string]any{"content": "large content"}},
			wantCompression: true,
		},
		{
			name:      "compressed response decompression",
			opName:    "GetCompressedData",
			query:     "query GetCompressedData { compressedData { content } }",
			variables: map[string]any{},
			wantData: map[string]any{
				"compressedData": map[string]any{
					"content": "decompressed content from zstd",
				},
			},
			wantCompression: false, // We're not compressing the request, but expecting compressed response
		},
		{
			name:      "compressed response with large data",
			opName:    "GetLargeCompressedData",
			query:     "query GetLargeCompressedData { largeCompressedData { content } }",
			variables: map[string]any{},
			wantData: map[string]any{
				"largeCompressedData": map[string]any{
					"content": string(make([]byte, 5000)), // Large content that would benefit from compression
				},
			},
			wantCompression: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opName := graphQLPattern.FindStringSubmatch(tt.query)[1]
			server := newValidatingServer(t, tt.wantData, opName, tt.identityPublicKey, tt.wantCompression)
			defer server.Close()
			requester, err := NewRequesterWithBaseURL(tt.identityPublicKey, server.URL)
			require.NoError(t, err)

			request := &graphql.Request{
				Query:     tt.query,
				Variables: tt.variables,
				OpName:    tt.opName,
			}
			response := &graphql.Response{}
			err = requester.MakeRequest(t.Context(), request, response)

			require.NoError(t, err)
			want := &graphql.Response{Data: tt.wantData}
			assert.Equal(t, want, response)
		})
	}
}

func TestMakeRequest_Errors(t *testing.T) {
	tests := []struct {
		name           string
		opName         string
		query          string
		serverResponse map[string]any
		serverStatus   int
		wantErr        string
	}{
		{
			name:    "invalid query payload",
			opName:  "InvalidQuery",
			query:   "invalid query",
			wantErr: "invalid JSON",
		},
		{
			name:         "HTTP 400 error",
			opName:       "GetUser",
			query:        "query GetUser { user { id } }",
			serverStatus: http.StatusBadRequest,
			wantErr:      "lightspark request failed: 400:",
		},
		{
			name:         "HTTP 500 error",
			opName:       "GetUser",
			query:        "query GetUser { user { id } }",
			serverStatus: http.StatusInternalServerError,
			wantErr:      "lightspark request failed: 500:",
		},
		{
			name:   "GraphQL internal error",
			opName: "GetUser",
			query:  "query GetUser { user { id } }",
			serverResponse: map[string]any{
				"errors": []any{
					map[string]any{
						"message": "Internal server error",
					},
				},
			},
			wantErr: "lightspark request failed: Internal server error",
		},
		{
			name:   "GraphQL user error",
			opName: "GetUser",
			query:  "query GetUser { user { id } }",
			serverResponse: map[string]any{
				"errors": []any{
					map[string]any{
						"message": "User not found",
						"extensions": map[string]any{
							"error_name": "USER_NOT_FOUND",
						},
					},
				},
			},
			wantErr: "USER_NOT_FOUND: User not found",
		},
		{
			name:   "GraphQL error with extensions but no error_name",
			opName: "GetUser",
			query:  "query GetUser { user { id } }",
			serverResponse: map[string]any{
				"errors": []any{
					map[string]any{
						"message": "Some error",
						"extensions": map[string]any{
							"other_field": "value",
						},
					},
				},
			},
			wantErr: "lightspark request failed: Some error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newErrorServer(t, tt.serverStatus, tt.serverResponse)
			defer server.Close()

			requester, err := NewRequesterWithBaseURL("", server.URL)
			require.NoError(t, err)

			request := &graphql.Request{
				Query:  tt.query,
				OpName: tt.opName,
			}
			response := &graphql.Response{}
			err = requester.MakeRequest(t.Context(), request, response)

			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestMakeRequest_DecompressionErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		corruptedData := []byte{0x28, 0xB5, 0x2F, 0xFD, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
		w.Header().Set("Content-Encoding", "zstd")
		_, _ = w.Write(corruptedData)
	}))
	defer server.Close()

	requester, err := NewRequesterWithBaseURL("", server.URL)
	require.NoError(t, err)

	request := &graphql.Request{
		Query:  "query GetUser { user { id } }",
		OpName: "GetUser",
	}
	response := &graphql.Response{}
	err = requester.MakeRequest(t.Context(), request, response)

	require.ErrorContains(t, err, "invalid zstd compression")
}

func TestMakeRequest_InvalidCompressedJSON_Errors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		compressed, _ := zstd.Compress(nil, []byte("not-valid-json"))
		w.Header().Set("Content-Encoding", "zstd")
		_, _ = w.Write(compressed)
	}))
	defer server.Close()

	requester, err := NewRequesterWithBaseURL("", server.URL)
	require.NoError(t, err)

	request := &graphql.Request{
		Query:  "query GetUser { user { id } }",
		OpName: "GetUser",
	}
	response := &graphql.Response{}
	err = requester.MakeRequest(t.Context(), request, response)

	require.ErrorContains(t, err, "invalid JSON")
}

func TestNewRequesterWithBaseURL_InvalidBaseURL(t *testing.T) {
	_, err := NewRequesterWithBaseURL("", "http://invalid-url")
	require.ErrorContains(t, err, `invalid base url: "http://invalid-url" must be https:// if not targeting localhost`)
}

func newErrorServer(t *testing.T, status int, serverResponse map[string]any) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != 0 {
			w.WriteHeader(status)
			return
		}
		if len(serverResponse) == 0 {
			return
		}
		responseData, _ := json.Marshal(serverResponse)
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write(responseData); err != nil {
			t.Error(err) // We have to call Error since we're in a goroutine.
		}
	}))
}

func newValidatingServer(t *testing.T, wantData map[string]any, wantOpName string, identityPubKey string, compression bool) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		assert.Equal(t, "zstd", r.Header.Get("Accept-Encoding"))
		assert.Equal(t, "spark", r.Header.Get("User-Agent"))
		assert.Equal(t, "spark", r.Header.Get("X-Lightspark-SDK"))
		assert.Equal(t, wantOpName, r.Header.Get("X-GraphQL-Operation"))

		// Verify identity public key header if provided
		if identityPubKey != "" {
			assert.Equal(t, identityPubKey, r.Header.Get("Spark-Identity-Public-Key"))
		}

		// Verify compression header if expected
		if compression {
			assert.Equal(t, "zstd", r.Header.Get("Content-Encoding"))
		}

		responseData, _ := json.Marshal(map[string]any{"data": wantData})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		if _, err := w.Write(responseData); err != nil {
			t.Error(err) // We have to call Error since we're in a goroutine.
		}
	}))
}
