package server

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ThinkInAIXYZ/go-mcp/pkg"
	"github.com/ThinkInAIXYZ/go-mcp/protocol"
	"github.com/ThinkInAIXYZ/go-mcp/server/components"
	"github.com/ThinkInAIXYZ/go-mcp/transport"
)

type currentTimeReq struct {
	Timezone string `json:"timezone" description:"current time timezone"`
}

func TestServerHandle(t *testing.T) {
	reader1, writer1 := io.Pipe()
	reader2, writer2 := io.Pipe()

	var (
		in = struct {
			reader io.ReadCloser
			writer io.WriteCloser
		}{
			reader: reader1,
			writer: writer1,
		}

		out = struct {
			reader io.ReadCloser
			writer io.WriteCloser
		}{
			reader: reader2,
			writer: writer2,
		}

		outScan = bufio.NewScanner(out.reader)
	)

	srv, err := NewServer(
		transport.NewMockServerTransport(in.reader, out.writer),
		WithServerInfo(protocol.Implementation{
			Name:    "ExampleServer",
			Version: "1.0.0",
		}))
	if err != nil {
		t.Fatalf("NewServer: %+v", err)
	}

	// add tool
	testTool := protocol.NewTool("test_tool", "test_tool")

	testToolCallContent := protocol.TextContent{
		Type: "text",
		Text: "pong",
	}
	if err := RegisterTool(srv, testTool, func(_ context.Context, _ *currentTimeReq) (*protocol.CallToolResult, error) {
		return &protocol.CallToolResult{
			Content: []protocol.Content{testToolCallContent},
		}, nil
	}); err != nil {
		t.Fatalf("RegisterTool: %+v", err)
	}

	// add prompt
	testPrompt := &protocol.Prompt{
		Name:        "test_prompt",
		Description: "test_prompt_description",
		Arguments: []protocol.PromptArgument{
			{
				Name:        "params1",
				Description: "params1's description",
				Required:    true,
			},
		},
	}
	testPromptGetResponse := &protocol.GetPromptResult{
		Description: "test_prompt_description",
	}
	srv.RegisterPrompt(testPrompt, func(context.Context, *protocol.GetPromptRequest) (*protocol.GetPromptResult, error) {
		return testPromptGetResponse, nil
	})

	// add resource
	testResource := &protocol.Resource{
		URI:      "file:///test.txt",
		Name:     "test.txt",
		MimeType: "text/plain-txt",
	}
	testResourceContent := protocol.TextResourceContents{
		URI:      testResource.URI,
		MimeType: testResource.MimeType,
		Text:     "test",
	}
	srv.RegisterResource(testResource, func(context.Context, *protocol.ReadResourceRequest) (*protocol.ReadResourceResult, error) {
		return &protocol.ReadResourceResult{
			Contents: []protocol.ResourceContents{
				testResourceContent,
			},
		}, nil
	})

	// add resource template
	testResourceTemplate := &protocol.ResourceTemplate{
		URITemplate: "file:///{path}",
		Name:        "test",
	}
	if err := srv.RegisterResourceTemplate(testResourceTemplate, func(context.Context, *protocol.ReadResourceRequest) (*protocol.ReadResourceResult, error) {
		return &protocol.ReadResourceResult{
			Contents: []protocol.ResourceContents{
				testResourceContent,
			},
		}, nil
	}); err != nil {
		t.Fatalf("RegisterResourceTemplate: %+v", err)
		return
	}

	go func() {
		if err := srv.Run(); err != nil {
			t.Errorf("server start: %+v", err)
		}
	}()

	testServerInit(t, srv, in.writer, outScan)

	tests := []struct {
		name             string
		method           protocol.Method
		request          protocol.ClientRequest
		expectedResponse protocol.ServerResponse
	}{
		{
			name:             "test_list_tool",
			method:           protocol.ToolsList,
			request:          protocol.ListToolsRequest{},
			expectedResponse: protocol.ListToolsResult{Tools: []*protocol.Tool{testTool}},
		},
		{
			name:   "test_call_tool",
			method: protocol.ToolsCall,
			request: protocol.CallToolRequest{
				Name: testTool.Name,
			},
			expectedResponse: protocol.CallToolResult{
				Content: []protocol.Content{
					testToolCallContent,
				},
			},
		},
		{
			name:             "test_ping",
			method:           protocol.Ping,
			request:          protocol.PingRequest{},
			expectedResponse: protocol.PingResult{},
		},
		{
			name:    "test_list_prompt",
			method:  protocol.PromptsList,
			request: protocol.ListPromptsRequest{},
			expectedResponse: protocol.ListPromptsResult{
				Prompts: []protocol.Prompt{*testPrompt},
			},
		},
		{
			name:   "test_get_prompt",
			method: protocol.PromptsGet,
			request: protocol.GetPromptRequest{
				Name: testPrompt.Name,
			},
			expectedResponse: testPromptGetResponse,
		},
		{
			name:    "test_list_resource",
			method:  protocol.ResourcesList,
			request: protocol.ListResourcesRequest{},
			expectedResponse: protocol.ListResourcesResult{
				Resources: []protocol.Resource{*testResource},
			},
		},
		{
			name:   "test_read_resource",
			method: protocol.ResourcesRead,
			request: protocol.ReadResourceRequest{
				URI: testResource.URI,
			},
			expectedResponse: protocol.ReadResourceResult{
				Contents: []protocol.ResourceContents{testResourceContent},
			},
		},
		{
			name:    "test_list_resource_template",
			method:  protocol.ResourceListTemplates,
			request: protocol.ListResourceTemplatesRequest{},
			expectedResponse: protocol.ListResourceTemplatesResult{
				ResourceTemplates: []protocol.ResourceTemplate{*testResourceTemplate},
			},
		},
		{
			name:   "test_resource_subscribe",
			method: protocol.ResourcesSubscribe,
			request: protocol.SubscribeRequest{
				URI: testResource.URI,
			},
			expectedResponse: protocol.SubscribeResult{},
		},
		{
			name:   "test_resource_unsubscribe",
			method: protocol.ResourcesUnsubscribe,
			request: protocol.UnsubscribeRequest{
				URI: testResource.URI,
			},
			expectedResponse: protocol.UnsubscribeResult{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			uuid, _ := uuid.NewUUID()
			req := protocol.NewJSONRPCRequest(uuid, tt.method, tt.request)
			reqBytes, err := json.Marshal(req)
			if err != nil {
				t.Fatalf("json Marshal: %+v", err)
			}
			if _, err = in.writer.Write(append(reqBytes, "\n"...)); err != nil {
				t.Fatalf("in Write: %+v", err)
			}

			var respBytes []byte
			if outScan.Scan() {
				respBytes = outScan.Bytes()
				if outScan.Err() != nil {
					t.Fatalf("outScan: %+v", err)
				}
			}

			var respMap map[string]interface{}
			if err = pkg.JSONUnmarshal(respBytes, &respMap); err != nil {
				t.Fatal(err)
			}

			expectedResp := protocol.NewJSONRPCSuccessResponse(uuid, tt.expectedResponse)
			expectedRespBytes, err := json.Marshal(expectedResp)
			if err != nil {
				t.Fatalf("json Marshal: %+v", err)
			}
			var expectedRespMap map[string]interface{}
			if err := pkg.JSONUnmarshal(expectedRespBytes, &expectedRespMap); err != nil {
				t.Fatal(err)
			}

			if !reflect.DeepEqual(respMap, expectedRespMap) {
				t.Fatalf("response not as expected.\ngot  = %v\nwant = %v", respMap, expectedRespMap)
			}
		})
	}
}

func TestServerNotify(t *testing.T) {
	reader1, writer1 := io.Pipe()
	reader2, writer2 := io.Pipe()

	var (
		in = struct {
			reader io.ReadCloser
			writer io.WriteCloser
		}{
			reader: reader1,
			writer: writer1,
		}

		out = struct {
			reader io.ReadCloser
			writer io.WriteCloser
		}{
			reader: reader2,
			writer: writer2,
		}

		outScan = bufio.NewScanner(out.reader)
	)

	srv, err := NewServer(
		transport.NewMockServerTransport(in.reader, out.writer),
		WithServerInfo(protocol.Implementation{
			Name:    "ExampleServer",
			Version: "1.0.0",
		}))
	if err != nil {
		t.Fatalf("NewServer: %+v", err)
	}

	// add tool
	testTool := protocol.NewTool("test_tool", "test_tool")

	testToolCallContent := protocol.TextContent{
		Type: "text",
		Text: "pong",
	}

	// add prompt
	testPrompt := &protocol.Prompt{
		Name:        "test_prompt",
		Description: "test_prompt_description",
		Arguments: []protocol.PromptArgument{
			{
				Name:        "params1",
				Description: "params1's description",
				Required:    true,
			},
		},
	}
	testPromptGetResponse := &protocol.GetPromptResult{
		Description: "test_prompt_description",
	}

	// add resource
	testResource := &protocol.Resource{
		URI:      "file:///test.txt",
		Name:     "test.txt",
		MimeType: "text/plain-txt",
	}
	testResourceContent := protocol.TextResourceContents{
		URI:      testResource.URI,
		MimeType: testResource.MimeType,
		Text:     "test",
	}

	// add resource template
	testResourceTemplate := &protocol.ResourceTemplate{
		URITemplate: "file:///{path}",
		Name:        "test",
	}

	go func() {
		if err := srv.Run(); err != nil {
			t.Errorf("server start: %+v", err)
		}
	}()

	testServerInit(t, srv, in.writer, outScan)

	tests := []struct {
		name           string
		method         protocol.Method
		f              func()
		expectedNotify protocol.ServerResponse
	}{
		{
			name:   "test_tools_changed_notify",
			method: protocol.NotificationToolsListChanged,
			f: func() {
				if err := RegisterTool(srv, testTool, func(context.Context, *protocol.CallToolRequest) (*protocol.CallToolResult, error) {
					return &protocol.CallToolResult{
						Content: []protocol.Content{testToolCallContent},
					}, nil
				}); err != nil {
					t.Fatalf("RegisterTool: %+v", err)
					return
				}
			},
			expectedNotify: protocol.NewToolListChangedNotification(),
		},
		{
			name:   "test_prompts_changed_notify",
			method: protocol.NotificationPromptsListChanged,
			f: func() {
				srv.RegisterPrompt(testPrompt, func(context.Context, *protocol.GetPromptRequest) (*protocol.GetPromptResult, error) {
					return testPromptGetResponse, nil
				})
			},
			expectedNotify: protocol.NewPromptListChangedNotification(),
		},
		{
			name:   "test_resources_changed_notify",
			method: protocol.NotificationResourcesListChanged,
			f: func() {
				srv.RegisterResource(testResource, func(context.Context, *protocol.ReadResourceRequest) (*protocol.ReadResourceResult, error) {
					return &protocol.ReadResourceResult{
						Contents: []protocol.ResourceContents{
							testResourceContent,
						},
					}, nil
				})
			},
			expectedNotify: protocol.NewResourceListChangedNotification(),
		},
		{
			name:   "test_resources_template_changed_notify",
			method: protocol.NotificationResourcesListChanged,
			f: func() {
				if err := srv.RegisterResourceTemplate(testResourceTemplate, func(context.Context, *protocol.ReadResourceRequest) (*protocol.ReadResourceResult, error) {
					return &protocol.ReadResourceResult{
						Contents: []protocol.ResourceContents{
							testResourceContent,
						},
					}, nil
				}); err != nil {
					t.Fatalf("RegisterResourceTemplate: %+v", err)
					return
				}
			},
			expectedNotify: protocol.NewResourceListChangedNotification(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ch := make(chan struct{})

			go func() {
				var notifyBytes []byte
				if outScan.Scan() {
					notifyBytes = outScan.Bytes()
				}

				var notifyMap map[string]interface{}
				if err := pkg.JSONUnmarshal(notifyBytes, &notifyMap); err != nil {
					t.Error(err)
					return
				}

				expectedNotify := protocol.NewJSONRPCNotification(tt.method, tt.expectedNotify)
				expectedNotifyBytes, err := json.Marshal(expectedNotify)
				if err != nil {
					t.Errorf("json Marshal: %+v", err)
					return
				}
				var expectedNotifyMap map[string]interface{}
				if err := pkg.JSONUnmarshal(expectedNotifyBytes, &expectedNotifyMap); err != nil {
					t.Error(err)
					return
				}

				if !reflect.DeepEqual(notifyMap, expectedNotifyMap) {
					t.Errorf("response not as expected.\ngot  = %v\nwant = %v", notifyMap, expectedNotifyMap)
					return
				}
				ch <- struct{}{}
			}()

			tt.f()

			<-ch
		})
	}
}

func testServerInit(t *testing.T, server *Server, in io.Writer, outScan *bufio.Scanner) {
	uuid, _ := uuid.NewUUID()
	req := protocol.NewJSONRPCRequest(uuid, protocol.Initialize, protocol.InitializeRequest{ProtocolVersion: protocol.Version})
	reqBytes, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("json Marshal: %+v", err)
	}
	if _, err = in.Write(append(reqBytes, "\n"...)); err != nil {
		t.Fatalf("in Write: %+v", err)
	}

	var respBytes []byte
	if outScan.Scan() {
		respBytes = outScan.Bytes()
		if outScan.Err() != nil {
			t.Fatalf("outScan: %+v", err)
		}
	}

	var respMap map[string]interface{}
	if err = pkg.JSONUnmarshal(respBytes, &respMap); err != nil {
		t.Fatal(err)
	}

	expectedResp := protocol.NewJSONRPCSuccessResponse(uuid, protocol.InitializeResult{
		ProtocolVersion: protocol.Version,
		Capabilities:    *server.capabilities,
		ServerInfo:      *server.serverInfo,
	})
	expectedRespBytes, err := json.Marshal(expectedResp)
	if err != nil {
		t.Fatalf("json Marshal: %+v", err)
	}
	var expectedRespMap map[string]interface{}
	if err = pkg.JSONUnmarshal(expectedRespBytes, &expectedRespMap); err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(respMap, expectedRespMap) {
		t.Fatalf("response not as expected.\ngot  = %v\nwant = %v", respMap, expectedRespMap)
	}

	notify := protocol.NewJSONRPCNotification(protocol.NotificationInitialized, nil)
	notifyBytes, err := json.Marshal(notify)
	if err != nil {
		t.Fatalf("json Marshal: %+v", err)
	}
	if _, err := in.Write(append(notifyBytes, "\n"...)); err != nil {
		t.Fatalf("in Write: %+v", err)
	}
}

type testLimiter struct {
	name               string
	rate               components.Rate
	numRequests        int
	requestInterval    time.Duration // Interval between requests
	expectedErrorCount int
	description        string
}

func TestServerRateLimiters(t *testing.T) {
	tests := []testLimiter{
		{
			name: "rapid_requests_exceed_burst",
			rate: components.Rate{
				Limit: 5.0,
				Burst: 10,
			},
			numRequests:        15,
			requestInterval:    0, // No delay between requests
			expectedErrorCount: 5,
			description:        "Sending requests rapidly should exceed burst limit and trigger rate limiting",
		},
		{
			name: "slow_requests_under_limit",
			rate: components.Rate{
				Limit: 5.0,
				Burst: 5,
			},
			numRequests:        10,
			requestInterval:    210 * time.Millisecond, // ~4.7 req/s, under the 5.0 limit
			expectedErrorCount: 0,
			description:        "Sending requests under the rate limit should not trigger rate limiting",
		},
		{
			name: "mixed_rate_pattern",
			rate: components.Rate{
				Limit: 10.0,
				Burst: 5,
			},
			numRequests:        20,
			requestInterval:    50 * time.Millisecond, // 20 req/s, above the 10.0 limit
			expectedErrorCount: 5,
			description:        "Sending requests at a rate higher than limit should trigger rate limiting after burst is consumed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testServerRateLimiter(t, tt)
		})
	}
}

func testServerRateLimiter(t *testing.T, tt testLimiter) {
	// Set up pipes for communication
	reader1, writer1 := io.Pipe()
	reader2, writer2 := io.Pipe()

	var (
		in = struct {
			reader io.ReadCloser
			writer io.WriteCloser
		}{
			reader: reader1,
			writer: writer1,
		}

		out = struct {
			reader io.ReadCloser
			writer io.WriteCloser
		}{
			reader: reader2,
			writer: writer2,
		}

		outScan = bufio.NewScanner(out.reader)
	)

	// Create server with rate limiter
	server, err := NewServer(
		transport.NewMockServerTransport(in.reader, out.writer),
		WithServerInfo(protocol.Implementation{
			Name:    "ExampleServer",
			Version: "1.0.0",
		}),
		WithRateLimiter(components.NewTokenBucketLimiter(tt.rate)),
	)
	if err != nil {
		t.Fatalf("NewServer: %+v", err)
	}

	// Register test tool
	testTool, err := protocol.NewTool("test_tool", "test_tool", currentTimeReq{})
	if err != nil {
		t.Fatalf("NewTool: %+v", err)
		return
	}
	testToolCallContent := protocol.TextContent{
		Type: "text",
		Text: "pong",
	}

	// Add minimal processing delay to simulate real-world scenario
	server.RegisterTool(testTool, func(_ *protocol.CallToolRequest) (*protocol.CallToolResult, error) {
		time.Sleep(5 * time.Millisecond) // Small processing delay
		return &protocol.CallToolResult{
			Content: []protocol.Content{testToolCallContent},
		}, nil
	})

	// Start server
	serverErrCh := make(chan error, 1)
	go func() {
		if err := server.Run(); err != nil {
			serverErrCh <- err
		}
	}()

	// Initialize server
	testServerInit(t, server, in.writer, outScan)

	// Test rate limiting by sending multiple requests
	errorCount := 0
	successCount := 0

	for i := 0; i < tt.numRequests; i++ {
		uuid, _ := uuid.NewUUID()
		req := protocol.NewJSONRPCRequest(uuid, protocol.ToolsCall, protocol.CallToolRequest{
			Name: testTool.Name,
		})
		reqBytes, err := json.Marshal(req)
		if err != nil {
			t.Fatalf("json Marshal: %+v", err)
		}

		if _, err = in.writer.Write(append(reqBytes, "\n"...)); err != nil {
			t.Fatalf("in Write: %+v", err)
		}

		var respBytes []byte
		if outScan.Scan() {
			respBytes = outScan.Bytes()
			if outScan.Err() != nil {
				t.Fatalf("outScan: %+v", err)
			}
		}

		var resp map[string]interface{}
		if err = pkg.JSONUnmarshal(respBytes, &resp); err != nil {
			t.Fatal(err)
		}

		// Check if response contains error
		if errObj, exists := resp["error"]; exists {
			errorObj, ok := errObj.(map[string]interface{})
			if ok {
				// Check if it's a rate limit error
				if code, codeExists := errorObj["code"].(float64); codeExists && code == float64(-32603) {
					errorCount++
				}
			}
		} else {
			successCount++
		}

		// Apply interval between requests if specified
		if tt.requestInterval > 0 && i < tt.numRequests-1 {
			time.Sleep(tt.requestInterval)
		}
	}

	// duration := time.Since(startTime)

	// Verify that we got the expected number of rate limit errors
	if errorCount != tt.expectedErrorCount {
		t.Errorf("Expected %d rate limit errors, got %d", tt.expectedErrorCount, errorCount)
	}

	// Verify that successful + errors = total requests
	if successCount+errorCount != tt.numRequests {
		t.Errorf("Request count mismatch: got %d successes + %d errors = %d, expected total %d",
			successCount, errorCount, successCount+errorCount, tt.numRequests)
	}

	// Cleanup
	in.writer.Close()
	out.reader.Close()

	// Check if server encountered errors
	select {
	case err := <-serverErrCh:
		t.Fatalf("Server error: %v", err)
	default:
		// No error, continue
	}
}
