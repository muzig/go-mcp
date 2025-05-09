package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ThinkInAIXYZ/go-mcp/protocol"
	"github.com/ThinkInAIXYZ/go-mcp/server"
	"github.com/ThinkInAIXYZ/go-mcp/server/components"
	"github.com/ThinkInAIXYZ/go-mcp/transport"
)

type currentTimeReq struct {
	Timezone string `json:"timezone" description:"current time timezone"`
}

func main() {
	// new mcp server with stdio or sse transport
	srv, err := server.NewServer(
		getTransport(),
		server.WithServerInfo(protocol.Implementation{
			Name:    "current-time-v2-server",
			Version: "1.0.0",
		}),
		// 创建一个每秒5个请求，突发容量为10的限速器
		server.WithRateLimiter(components.NewTokenBucketLimiter(components.Rate{
			Limit: 5.0, // 每秒5个请求
			Burst: 10,  // 最多允许10个请求的突发
		})),
	)
	if err != nil {
		log.Fatalf("Failed to create server: %v", err)
	}

	// new protocol tool with name, descipriton and properties
	tool := protocol.NewTool("current_time", "Get current time with timezone, Asia/Shanghai is default",
		// 为指定工具设置每秒10个请求，突发容量为20的限速器
		protocol.WithRateLimit(srv.GetLimiter(), components.Rate{
			Limit: 10.0, // 每秒10个请求
			Burst: 20,   // 最多允许20个请求的突发
		}),
	)
	err = server.RegisterTool(srv, tool, currentTime)
	if err != nil {
		log.Fatalf("Failed to create tool: %v", err)
		return
	}

	// register tool and start mcp server
	// srv.RegisterResource()
	// srv.RegisterPrompt()
	// srv.RegisterResourceTemplate()

	errCh := make(chan error)
	go func() {
		errCh <- srv.Run()
	}()

	if err = signalWaiter(errCh); err != nil {
		log.Fatalf("signal waiter: %v", err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Fatalf("Shutdown error: %v", err)
	}
}

func getTransport() (t transport.ServerTransport) {
	var (
		mode string
		addr = "127.0.0.1:8080"
	)

	flag.StringVar(&mode, "transport", "stdio", "The transport to use, should be \"stdio\" or \"sse\" or \"streamable_http\"")
	flag.Parse()

	switch mode {
	case "stdio":
		log.Println("start current time mcp server with stdio transport")
		t = transport.NewStdioServerTransport()
	case "sse":
		log.Printf("start current time mcp server with sse transport, listen %s", addr)
		t, _ = transport.NewSSEServerTransport(addr)
	case "streamable_http":
		log.Printf("start current time mcp server with streamable_http transport, listen %s", addr)
		t = transport.NewStreamableHTTPServerTransport(addr)
	default:
		panic(fmt.Errorf("unknown mode: %s", mode))
	}

	return t
}

func currentTime(_ context.Context, req *currentTimeReq) (*protocol.CallToolResult, error) {
	loc, err := time.LoadLocation(req.Timezone)
	if err != nil {
		return nil, fmt.Errorf("parse timezone with error: %v", err)
	}
	text := fmt.Sprintf(`current time is %s`, time.Now().In(loc))

	return &protocol.CallToolResult{
		Content: []protocol.Content{
			protocol.TextContent{
				Type: "text",
				Text: text,
			},
		},
	}, nil
}

func signalWaiter(errCh chan error) error {
	signalToNotify := []os.Signal{syscall.SIGINT, syscall.SIGHUP, syscall.SIGTERM}
	if signal.Ignored(syscall.SIGHUP) {
		signalToNotify = []os.Signal{syscall.SIGINT, syscall.SIGTERM}
	}

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, signalToNotify...)

	select {
	case sig := <-signals:
		switch sig {
		case syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM:
			log.Printf("Received signal: %s\n", sig)
			// graceful shutdown
			return nil
		}
	case err := <-errCh:
		return err
	}

	return nil
}
