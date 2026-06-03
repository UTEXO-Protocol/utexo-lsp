package node_client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Client represents the RGB Lightning Node API client.
// API docs: https://github.com/RGB-Tools/rgb-lightning-node
type Client struct {
	baseURL    string // Full base URL including user_id and node_id, e.g., https://node-api.thunderstack.org/{user_id}/{node_id}
	authToken  string // Bearer token for authentication
	httpClient *http.Client
}

// NewClient creates a new RGB Lightning API client.
// baseURL should be the full URL including user_id and node_id.
func NewClient(baseURL, authToken string, httpClient *http.Client) *Client {
	// transport := &http.Transport{
	// 	TLSClientConfig: &tls.Config{
	// 		MinVersion: tls.VersionTLS12,
	// 	},
	// }

	// &http.Client{
	// 	Timeout:   5 * time.Minute,
	// 	Transport: transport,
	// }

	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		authToken:  authToken,
		httpClient: httpClient,
	}
}

// APIError represents an error from the RGB Lightning API.
type APIError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Details string `json:"details,omitempty"`
}

func (e *APIError) Error() string {
	if e.Details != "" {
		return fmt.Sprintf("RGB Lightning API error %d: %s (%s)", e.Code, e.Message, e.Details)
	}
	return fmt.Sprintf("RGB Lightning API error %d: %s", e.Code, e.Message)
}

// post makes a POST request to the specified endpoint.
func (c *Client) post(ctx context.Context, endpoint string, payload any, response any) error {
	url := c.baseURL + endpoint

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal request payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("failed to create HTTP request: %w", err)
	}

	c.setHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to make HTTP request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &APIError{
			Code:    resp.StatusCode,
			Message: string(body),
			Details: fmt.Sprintf("HTTP %d", resp.StatusCode),
		}
	}

	if response != nil && len(body) > 0 {
		if err := json.Unmarshal(body, response); err != nil {
			return fmt.Errorf("failed to parse response JSON: %w", err)
		}
	}

	return nil
}

// get makes a GET request to the specified endpoint.
func (c *Client) get(ctx context.Context, endpoint string, response any) error {
	url := c.baseURL + endpoint

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return fmt.Errorf("failed to create HTTP request: %w", err)
	}

	c.setHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to make HTTP request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &APIError{
			Code:    resp.StatusCode,
			Message: string(body),
			Details: fmt.Sprintf("HTTP %d", resp.StatusCode),
		}
	}

	if response != nil && len(body) > 0 {
		if err := json.Unmarshal(body, response); err != nil {
			return fmt.Errorf("failed to parse response JSON: %w", err)
		}
	}

	return nil
}

// setHeaders sets common headers for API requests.
func (c *Client) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if c.authToken != "" {
		// Thunderstack node API accepts standard Bearer auth.
		// Allow env to be either raw token or already prefixed with "Bearer ".
		auth := strings.TrimSpace(c.authToken)
		if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
			req.Header.Set("Authorization", auth)
		} else {
			req.Header.Set("Authorization", "Bearer "+auth)
		}
	}
}
