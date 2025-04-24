package server

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/ThinkInAIXYZ/go-mcp/pkg"
	"github.com/ThinkInAIXYZ/go-mcp/protocol"
	"github.com/ThinkInAIXYZ/go-mcp/server/session"
	"github.com/ThinkInAIXYZ/go-mcp/transport"
)

type Option func(*Server)

func WithCapabilities(capabilities protocol.ServerCapabilities) Option {
	return func(s *Server) {
		s.capabilities = &capabilities
	}
}

func WithServerInfo(serverInfo protocol.Implementation) Option {
	return func(s *Server) {
		s.serverInfo = &serverInfo
	}
}

func WithInstructions(instructions string) Option {
	return func(s *Server) {
		s.instructions = instructions
	}
}

func WithSessionMaxIdleTime(maxIdleTime time.Duration) Option {
	return func(s *Server) {
		s.sessionManager.SetMaxIdleTime(maxIdleTime)
	}
}

func WithLogger(logger pkg.Logger) Option {
	return func(s *Server) {
		s.logger = logger
	}
}

type Server struct {
	transport transport.ServerTransport

	tools             pkg.SyncMap[*toolEntry]
	prompts           pkg.SyncMap[*promptEntry]
	resources         pkg.SyncMap[*resourceEntry]
	resourceTemplates pkg.SyncMap[*resourceTemplateEntry]

	sessionManager *session.Manager

	inShutdown   *pkg.AtomicBool // true when server is in shutdown
	inFlyRequest sync.WaitGroup

	capabilities *protocol.ServerCapabilities
	serverInfo   *protocol.Implementation
	instructions string

	logger pkg.Logger
}

func NewServer(t transport.ServerTransport, opts ...Option) (*Server, error) {
	server := &Server{
		transport: t,
		capabilities: &protocol.ServerCapabilities{
			Prompts:   &protocol.PromptsCapability{ListChanged: true},
			Resources: &protocol.ResourcesCapability{ListChanged: true, Subscribe: true},
			Tools:     &protocol.ToolsCapability{ListChanged: true},
		},
		inShutdown: pkg.NewAtomicBool(),
		serverInfo: &protocol.Implementation{},
		logger:     pkg.DefaultLogger,
	}

	t.SetReceiver(transport.ServerReceiverF(server.receive))

	server.sessionManager = session.NewManager(server.sessionDetection)

	for _, opt := range opts {
		opt(server)
	}

	server.sessionManager.SetLogger(server.logger)

	t.SetSessionManager(server.sessionManager)

	return server, nil
}

func (server *Server) Run() error {
	go func() {
		defer pkg.Recover()

		server.sessionManager.StartHeartbeatAndCleanInvalidSessions()
	}()

	if err := server.transport.Run(); err != nil {
		return fmt.Errorf("init mcp server transpor run fail: %w", err)
	}
	return nil
}

type toolEntry struct {
	tool    *protocol.Tool
	handler ToolHandlerFunc
}

// ToolHandlerFunc is the type of the function that handles a tool call
type ToolHandlerFunc func(context.Context, *protocol.CallToolRequest) (*protocol.CallToolResult, error)

// ToolHandlerFuncGenerics is the type of the function that handles a tool call with a generic type
type ToolHandlerFuncGenerics[T any] func(context.Context, T) (*protocol.CallToolResult, error)

// RegisterTool registers a tool with a handler that takes a request of type T
func RegisterTool[T any](server *Server, tool *protocol.Tool, toolHandler ToolHandlerFuncGenerics[T]) error {
	if len(tool.RawInputSchema) == 0 {
		schema, err := protocol.GenerateSchemaFromReqStruct(new(T))
		if err != nil {
			return err
		}
		tool.InputSchema = *schema
	}

	server.tools.Store(tool.Name, &toolEntry{tool: tool, handler: func(ctx context.Context, ctr *protocol.CallToolRequest) (*protocol.CallToolResult, error) {
		var req T
		if err := protocol.VerifyAndUnmarshal(ctr.RawArguments, &req); err != nil {
			return nil, err
		}
		return toolHandler(ctx, req)
	}})
	if !server.sessionManager.IsEmpty() {
		if err := server.sendNotification4ToolListChanges(context.Background()); err != nil {
			server.logger.Warnf("send notification toll list changes fail: %v", err)
			return nil
		}
	}
	return nil
}

func (server *Server) UnregisterTool(name string) {
	server.tools.Delete(name)
	if !server.sessionManager.IsEmpty() {
		if err := server.sendNotification4ToolListChanges(context.Background()); err != nil {
			server.logger.Warnf("send notification toll list changes fail: %v", err)
			return
		}
	}
}

type promptEntry struct {
	prompt  *protocol.Prompt
	handler PromptHandlerFunc
}

type PromptHandlerFunc func(context.Context, *protocol.GetPromptRequest) (*protocol.GetPromptResult, error)

func (server *Server) RegisterPrompt(prompt *protocol.Prompt, promptHandler PromptHandlerFunc) {
	server.prompts.Store(prompt.Name, &promptEntry{prompt: prompt, handler: promptHandler})
	if !server.sessionManager.IsEmpty() {
		if err := server.sendNotification4PromptListChanges(context.Background()); err != nil {
			server.logger.Warnf("send notification prompt list changes fail: %v", err)
			return
		}
	}
}

func (server *Server) UnregisterPrompt(name string) {
	server.prompts.Delete(name)
	if !server.sessionManager.IsEmpty() {
		if err := server.sendNotification4PromptListChanges(context.Background()); err != nil {
			server.logger.Warnf("send notification prompt list changes fail: %v", err)
			return
		}
	}
}

type resourceEntry struct {
	resource *protocol.Resource
	handler  ResourceHandlerFunc
}

type ResourceHandlerFunc func(context.Context, *protocol.ReadResourceRequest) (*protocol.ReadResourceResult, error)

func (server *Server) RegisterResource(resource *protocol.Resource, resourceHandler ResourceHandlerFunc) {
	server.resources.Store(resource.URI, &resourceEntry{resource: resource, handler: resourceHandler})
	if !server.sessionManager.IsEmpty() {
		if err := server.sendNotification4ResourceListChanges(context.Background()); err != nil {
			server.logger.Warnf("send notification resource list changes fail: %v", err)
			return
		}
	}
}

func (server *Server) UnregisterResource(uri string) {
	server.resources.Delete(uri)
	if !server.sessionManager.IsEmpty() {
		if err := server.sendNotification4ResourceListChanges(context.Background()); err != nil {
			server.logger.Warnf("send notification resource list changes fail: %v", err)
			return
		}
	}
}

type resourceTemplateEntry struct {
	resourceTemplate *protocol.ResourceTemplate
	handler          ResourceHandlerFunc
}

func (server *Server) RegisterResourceTemplate(resource *protocol.ResourceTemplate, resourceHandler ResourceHandlerFunc) error {
	if err := resource.ParseURITemplate(); err != nil {
		return err
	}
	server.resourceTemplates.Store(resource.URITemplate, &resourceTemplateEntry{resourceTemplate: resource, handler: resourceHandler})
	if !server.sessionManager.IsEmpty() {
		if err := server.sendNotification4ResourceListChanges(context.Background()); err != nil {
			server.logger.Warnf("send notification resource list changes fail: %v", err)
			return nil
		}
	}
	return nil
}

func (server *Server) UnregisterResourceTemplate(uriTemplate string) {
	server.resourceTemplates.Delete(uriTemplate)
	if !server.sessionManager.IsEmpty() {
		if err := server.sendNotification4ResourceListChanges(context.Background()); err != nil {
			server.logger.Warnf("send notification resource list changes fail: %v", err)
			return
		}
	}
}

func (server *Server) Shutdown(userCtx context.Context) error {
	server.inShutdown.Store(true)

	serverCtx, cancel := context.WithCancel(userCtx)
	defer cancel()

	go func() {
		defer pkg.Recover()

		server.inFlyRequest.Wait()
		cancel()
	}()

	server.sessionManager.StopHeartbeat()

	return server.transport.Shutdown(userCtx, serverCtx)
}

func (server *Server) sessionDetection(ctx context.Context, sessionID string) error {
	if server.inShutdown.Load() {
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	if _, err := server.Ping(setSessionIDToCtx(ctx, sessionID), protocol.NewPingRequest()); err != nil {
		return err
	}
	return nil
}
