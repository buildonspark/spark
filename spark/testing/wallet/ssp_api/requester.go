package sspapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/Khan/genqlient/graphql"

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
	baseURL           string
	identityPublicKey string

	httpClient *http.Client
}

const (
	defaultBaseURL = "https://api.dev.dev.sparkinfra.net/graphql/spark/rc"
	userAgent      = "spark"
)

func NewRequester(identityPublicKey string) (*Requester, error) {
	return &Requester{identityPublicKey: identityPublicKey, baseURL: defaultBaseURL}, nil
}

func NewRequesterWithBaseURL(identityPublicKey string, baseURL string) (*Requester, error) {
	if len(baseURL) == 0 {
		return NewRequester(identityPublicKey)
	}
	if err := validateBaseURL(baseURL); err != nil {
		return nil, err
	}
	return &Requester{
		identityPublicKey: identityPublicKey,
		baseURL:           baseURL,
	}, nil
}

func validateBaseURL(baseURL string) error {
	if len(baseURL) == 0 {
		return errors.New("base url is empty")
	}
	parsedURL, err := url.Parse(baseURL)
	if err != nil {
		return fmt.Errorf("invalid base url: %q", baseURL)
	}
	hostNameParts := strings.Split(parsedURL.Hostname(), ".")
	hostNameTLD := hostNameParts[len(hostNameParts)-1]
	if parsedURL.Scheme != "https" && !isAllowlistedLocalhost(parsedURL, hostNameTLD) {
		return fmt.Errorf("invalid base url: %q must be https:// if not targeting localhost", baseURL)
	}
	return nil
}

func isAllowlistedLocalhost(parsedURL *url.URL, hostNameTLD string) bool {
	if parsedURL.Hostname() == "localhost" || hostNameTLD == "local" || hostNameTLD == "internal" {
		return true
	}
	asIP := net.ParseIP(parsedURL.Hostname())
	return asIP != nil && asIP.IsLoopback()
}

var graphQLPattern = regexp.MustCompile(`(?i)\s*(?:query|mutation)\s+(?P<OperationName>\w+)`)

// MakeRequest makes the given GraphQL request. It implements the graphql.Client interface.
func (r *Requester) MakeRequest(ctx context.Context, req *graphql.Request, resp *graphql.Response) error {
	payload := map[string]any{
		"operationName": req.OpName,
		"query":         req.Query,
		"variables":     req.Variables,
	}

	encodedPayload, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("error when encoding payload: %w", err)
	}

	body := encodedPayload
	shouldCompress := len(encodedPayload) > 1024
	if shouldCompress {
		if body, err = zstd.Compress(nil, encodedPayload); err != nil {
			return err
		}
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, r.baseURL, bytes.NewBuffer(body))
	if err != nil {
		return err
	}

	if len(r.identityPublicKey) != 0 {
		request.Header.Add("Spark-Identity-Public-Key", r.identityPublicKey)
	}
	request.Header.Add("Content-Type", "application/json")
	if shouldCompress {
		request.Header.Add("Content-Encoding", "zstd")
	}
	request.Header.Add("Accept-Encoding", "zstd")
	request.Header.Add("X-GraphQL-Operation", req.OpName)
	request.Header.Add("User-Agent", userAgent)
	request.Header.Add("X-Lightspark-SDK", userAgent)

	httpClient := r.httpClient
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return RequestError{Message: response.Status, StatusCode: response.StatusCode}
	}

	data, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}

	if response.Header.Get("Content-Encoding") == "zstd" {
		if data, err = zstd.Decompress(nil, data); err != nil {
			return fmt.Errorf("invalid zstd compression: %w", err)
		}
	}

	if err = json.Unmarshal(data, resp); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}

	if len(resp.Errors) == 0 {
		return nil
	}

	respErr := resp.Errors[0]
	errorMessage := respErr.Message
	if errorName, ok := respErr.Extensions["error_name"].(string); ok && len(errorName) > 0 {
		return GraphQLError{Message: errorMessage, Type: errorName}
	}
	return GraphQLInternalError{Message: errorMessage}
}
