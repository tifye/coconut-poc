package e2etest

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/log"
	"github.com/stretchr/testify/require"
	"github.com/tifye/tunnel/pkg"
	"golang.org/x/crypto/ssh"
	"golang.org/x/sync/errgroup"
)

var defaultMockBackendConfig = &MockBackendConfig{
	RateLimit: 0,
}

type testSuite struct {
	t *testing.T

	shutdownCancel context.CancelFunc
	server         *pkg.Server
	serverAddr     string
	client         *pkg.Client
	mockBackend    *MockBackend
}

func newTestSuite(t *testing.T, mbcfg *MockBackendConfig) *testSuite {
	suite := &testSuite{t: t}
	suite.setup(mbcfg)
	return suite
}

func (ts *testSuite) setup(mbcfg *MockBackendConfig) {
	if mbcfg != nil {
		ts.mockBackend = newMockBackend(mbcfg)
	} else {
		ts.mockBackend = newMockBackend(defaultMockBackendConfig)
	}

	_, pri, err := ed25519.GenerateKey(nil)
	require.NoError(ts.t, err, "failed to generate ssh key pair")

	signer, err := ssh.NewSignerFromKey(pri)
	require.NoError(ts.t, err, "failed create signer from private key")

	scfg := &pkg.ServerConfig{
		NoClientAuth:          true,
		Logger:                log.New(io.Discard),
		ClientListenerAddress: "127.0.0.1:9000",
		// needed to set custom host because could not make request to subdomain on localhost from here
		FrontendAddress: "this.pc:9997",
		Signer:          signer,
	}
	server, err := pkg.NewServer(scfg)
	require.Nil(ts.t, err, err)
	ts.server = server
	ts.serverAddr = scfg.FrontendAddress

	ccfg := &pkg.ClientConfig{
		User:          "Test User",
		NoAuth:        true,
		Logger:        log.New(io.Discard),
		ProxyPass:     "http://" + ts.mockBackend.Addr,
		ServerHostKey: signer.PublicKey(),
	}
	client, err := pkg.NewClient(ccfg)
	require.Nil(ts.t, err, err)
	ts.client = client

	ctx, cancel := context.WithCancel(context.Background())
	ts.shutdownCancel = cancel
	go func() {
		err := server.Start(ctx)
		if err != nil {
			ts.t.Errorf("server err: %s", err)
		}
	}()
	go func() {
		err := client.Start(ctx, scfg.ClientListenerAddress)
		if err != nil {
			ts.t.Errorf("client err: %s", err)
		}
	}()
}

func (ts *testSuite) teardown() {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	eg, ctx := errgroup.WithContext(ctx)

	ts.shutdownCancel()

	eg.Go(func() error {
		ts.mockBackend.Close()
		return nil
	})

	eg.Go(func() error {
		return ts.server.Close(ctx)
	})

	err := eg.Wait()
	require.Nil(ts.t, err, err)
}

type simpleRequest struct {
	name, path, method, body string
	expectedStatus           int
	expectedBody             string
}

func (ts *testSuite) runTestCase(t *testing.T, tc simpleRequest, client *http.Client) {
	url := fmt.Sprintf("http://%s.%s%s", "star-sage-sanctum", ts.serverAddr, tc.path)

	var body io.Reader
	if tc.body != "" {
		body = strings.NewReader(tc.body)
	}

	req, err := http.NewRequest(tc.method, url, body)
	require.Nilf(t, err, "failed to create request: %v", err)

	if tc.body != "" {
		req.Header.Set("Content-Type", "application/json")
	}

	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	resp, err := client.Do(req)
	require.Nilf(t, err, "request failed: %v", err)
	defer resp.Body.Close()

	if resp.StatusCode != tc.expectedStatus {
		t.Errorf("Expected status %d, got %d", tc.expectedStatus, resp.StatusCode)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	require.Nilf(t, err, "failed to reead response body: %v", err)

	bodyBytesStr := string(bodyBytes)
	if !strings.Contains(bodyBytesStr, tc.expectedBody) {
		t.Errorf("Expected body to contain %q, got %q", tc.expectedBody, bodyBytesStr)
	}
}

func TestE2E_Simple(t *testing.T) {
	suite := newTestSuite(t, defaultMockBackendConfig)
	defer suite.teardown()

	testCases := []simpleRequest{
		{
			name:           "Basic GET Request",
			path:           "/",
			method:         "GET",
			expectedStatus: http.StatusOK,
			expectedBody:   "mock backend response",
		},
		{
			name:           "POST Request with Body",
			path:           "/echo/body",
			method:         "POST",
			body:           `{"key": "value"}`,
			expectedStatus: http.StatusOK,
			expectedBody:   `{"key": "value"}`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			suite.runTestCase(t, tc, nil)
		})
	}
}

func TestE2E_Latency(t *testing.T) {
	suite := newTestSuite(t, &MockBackendConfig{
		Latency: 500,
	})
	defer suite.teardown()

	testCases := []struct {
		simpleRequest
		numConcurrent int
	}{
		{
			simpleRequest: simpleRequest{
				name:           "Basic GET Request",
				path:           "/",
				method:         "GET",
				expectedStatus: http.StatusOK,
				expectedBody:   "mock backend response",
			},
			numConcurrent: 10,
		},
		{
			simpleRequest: simpleRequest{
				name:           "POST Request with Body",
				path:           "/echo/body",
				method:         "POST",
				body:           `{"key": "value"}`,
				expectedStatus: http.StatusOK,
				expectedBody:   `{"key": "value"}`,
			},
			numConcurrent: 10,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			wg := sync.WaitGroup{}
			wg.Add(tc.numConcurrent)
			client := &http.Client{Timeout: 10 * time.Second}
			for range tc.numConcurrent {
				go func() {
					defer wg.Done()
					suite.runTestCase(t, tc.simpleRequest, client)
				}()
			}
			wg.Wait()
		})
	}
}
