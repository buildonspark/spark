// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved
package sspapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/DataDog/zstd"
)

// RequestError indicates that a request to the Lightspark API failed.
// It could be due to a service outage or a network error.
// The request should be retried if RequestError is returned with server errors (500-599).
type RequestError struct {
	Message    string
	StatusCode int
}

func (e RequestError) Error() string {
	return "lightspark request failed: " + strconv.Itoa(e.StatusCode) + ": " + e.Message
}

// GraphQLInternalError indicates there's a failure in the Lightspark API.
// It could be due to a bug on Ligthspark's side.
// The request can be retried, because the error might be transient.
type GraphQLInternalError struct {
	Message string
}

func (e GraphQLInternalError) Error() string {
	return "lightspark request failed: " + e.Message
}

// GraphQLError indicates the GraphQL request succeeded, but there's a user error.
// The request should not be retried, because the error is due to the user's input.
type GraphQLError struct {
	Message string
	Type    string
}

func (e GraphQLError) Error() string {
	return e.Type + ": " + e.Message
}

type Requester struct {
	BaseURL           *string
	IdentityPublicKey *string

	HTTPClient *http.Client
}

func NewRequester(identityPublicKey *string) (*Requester, error) {
	return &Requester{
		IdentityPublicKey: identityPublicKey,
	}, nil
}

func NewRequesterWithBaseURL(identityPublicKey *string, baseURL *string) (*Requester, error) {
	if baseURL == nil {
		return NewRequester(identityPublicKey)
	}
	if err := ValidateBaseURL(*baseURL); err != nil {
		return nil, err
	}
	return &Requester{
		IdentityPublicKey: identityPublicKey,
		BaseURL:           baseURL,
	}, nil
}

func ValidateBaseURL(baseURL string) error {
	parsedURL, err := url.Parse(baseURL)
	if err != nil {
		return errors.New("invalid base url. Not a valid URL")
	}
	hostNameParts := strings.Split(parsedURL.Hostname(), ".")
	hostNameTLD := hostNameParts[len(hostNameParts)-1]
	isWhitelistedLocalHost := parsedURL.Hostname() == "localhost" ||
		hostNameTLD == "local" ||
		hostNameTLD == "internal" ||
		parsedURL.Hostname() == "127.0.0.1"
	if parsedURL.Scheme != "https" && !isWhitelistedLocalHost {
		return errors.New("invalid base url. Must be https:// if not targeting localhost")
	}
	return nil
}

const DefaultBaseURL = "https://api.dev.dev.sparkinfra.net/graphql/spark/rc"

func (r *Requester) ExecuteGraphqlWithContext(ctx context.Context, query string, variables map[string]interface{}) (map[string]interface{}, error) {
	re := regexp.MustCompile(`(?i)\s*(?:query|mutation)\s+(?P<OperationName>\w+)`)
	matches := re.FindStringSubmatch(query)
	index := re.SubexpIndex("OperationName")
	if len(matches) <= index {
		return nil, errors.New("invalid query payload")
	}
	operationName := matches[index]

	payload := map[string]interface{}{
		"operationName": operationName,
		"query":         query,
		"variables":     variables,
	}

	encodedPayload, err := json.Marshal(payload)
	if err != nil {
		return nil, errors.New("error when encoding payload")
	}

	body := encodedPayload
	compressed := false
	if len(encodedPayload) > 1024 {
		compressed = true
		body, err = zstd.Compress(nil, encodedPayload)
		if err != nil {
			return nil, err
		}
	}

	var serverURL string
	if r.BaseURL == nil {
		serverURL = DefaultBaseURL
	} else {
		serverURL = *r.BaseURL
	}
	if err := ValidateBaseURL(serverURL); err != nil {
		return nil, err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, serverURL, bytes.NewBuffer(body))
	if err != nil {
		return nil, err
	}

	if r.IdentityPublicKey != nil {
		request.Header.Add("Spark-Identity-Public-Key", *r.IdentityPublicKey)
	}
	request.Header.Add("Content-Type", "application/json")
	if compressed {
		request.Header.Add("Content-Encoding", "zstd")
	}
	request.Header.Add("Accept-Encoding", "zstd")
	request.Header.Add("X-GraphQL-Operation", operationName)
	request.Header.Add("User-Agent", r.getUserAgent())
	request.Header.Add("X-Lightspark-SDK", r.getUserAgent())

	httpClient := r.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return nil, RequestError{Message: response.Status, StatusCode: response.StatusCode}
	}

	data, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}

	if response.Header.Get("Content-Encoding") == "zstd" {
		data, err = zstd.Decompress(nil, data)
		if err != nil {
			return nil, err
		}
	}

	var result map[string]interface{}
	err = json.Unmarshal(data, &result)
	if err != nil {
		return nil, err
	}

	if errs, ok := result["errors"]; ok {
		err := errs.([]interface{})[0]
		errMap := err.(map[string]interface{})
		errorMessage := errMap["message"].(string)
		if errMap["extensions"] == nil {
			return nil, GraphQLInternalError{Message: errorMessage}
		}
		extensions := errMap["extensions"].(map[string]interface{})
		if extensions["error_name"] == nil {
			return nil, GraphQLInternalError{Message: errorMessage}
		}
		errorName := extensions["error_name"].(string)
		return nil, GraphQLError{Message: errorMessage, Type: errorName}
	}

	return result["data"].(map[string]interface{}), nil
}

func (r *Requester) getUserAgent() string {
	return "spark"
}
