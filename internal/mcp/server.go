package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
)

// --- JSON-RPC 2.0 types ---

type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"` // null for notifications
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id"`
	Result  interface{} `json:"result,omitempty"`
	Error   *rpcError   `json:"error,omitempty"`
}

type rpcError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

// --- MCP protocol types ---

type serverInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type initializeResult struct {
	ProtocolVersion string            `json:"protocolVersion"`
	ServerInfo      serverInfo        `json:"serverInfo"`
	Capabilities    serverCapability  `json:"capabilities"`
}

type serverCapability struct {
	Tools *toolsCapability `json:"tools,omitempty"`
}

type toolsCapability struct {
	ListChanged bool `json:"listChanged"`
}

type toolDef struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema interface{} `json:"inputSchema"`
}

type toolsListResult struct {
	Tools []toolDef `json:"tools"`
}

type toolCallParams struct {
	Name      string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments,omitempty"`
}

type toolCallResult struct {
	Content []contentBlock `json:"content"`
	IsError bool           `json:"isError,omitempty"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// --- Tool registry ---

// ToolHandler is a function that handles a tool call and returns text content.
type ToolHandler func(args map[string]interface{}) (string, error)

// Tool describes a registered MCP tool.
type Tool struct {
	Name        string
	Description string
	InputSchema interface{} // JSON Schema object
	Handler     ToolHandler
}

// Server is an MCP server that speaks JSON-RPC 2.0 over stdio.
type Server struct {
	name    string
	version string

	mu    sync.RWMutex
	tools map[string]Tool

	in  io.Reader
	out io.Writer
}

// NewServer creates a new MCP server.
func NewServer(name, version string) *Server {
	return &Server{
		name:    name,
		version: version,
		tools:   make(map[string]Tool),
		in:      os.Stdin,
		out:     os.Stdout,
	}
}

// RegisterTool adds a tool to the server's registry.
func (s *Server) RegisterTool(t Tool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tools[t.Name] = t
}

// Run starts the server, reading JSON-RPC requests from stdin and writing
// responses to stdout. It blocks until stdin is closed or an error occurs.
func (s *Server) Run() error {
	scanner := bufio.NewScanner(s.in)
	// Allow large messages (1 MB)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var req jsonRPCRequest
		if err := json.Unmarshal(line, &req); err != nil {
			s.writeError(nil, -32700, "Parse error", err.Error())
			continue
		}

		s.handleRequest(&req)
	}

	return scanner.Err()
}

func (s *Server) handleRequest(req *jsonRPCRequest) {
	// Notifications have no ID — we handle them but don't respond.
	isNotification := req.ID == nil || string(req.ID) == "null"

	switch req.Method {
	case "initialize":
		if isNotification {
			return
		}
		s.writeResult(req.ID, initializeResult{
			ProtocolVersion: "2024-11-05",
			ServerInfo: serverInfo{
				Name:    s.name,
				Version: s.version,
			},
			Capabilities: serverCapability{
				Tools: &toolsCapability{ListChanged: false},
			},
		})

	case "notifications/initialized":
		// Acknowledged — no response needed for notifications.

	case "tools/list":
		if isNotification {
			return
		}
		s.mu.RLock()
		defs := make([]toolDef, 0, len(s.tools))
		for _, t := range s.tools {
			defs = append(defs, toolDef{
				Name:        t.Name,
				Description: t.Description,
				InputSchema: t.InputSchema,
			})
		}
		s.mu.RUnlock()
		s.writeResult(req.ID, toolsListResult{Tools: defs})

	case "tools/call":
		if isNotification {
			return
		}
		s.handleToolCall(req)

	default:
		if !isNotification {
			s.writeError(req.ID, -32601, "Method not found", req.Method)
		}
	}
}

func (s *Server) handleToolCall(req *jsonRPCRequest) {
	var params toolCallParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		s.writeError(req.ID, -32602, "Invalid params", err.Error())
		return
	}

	s.mu.RLock()
	tool, ok := s.tools[params.Name]
	s.mu.RUnlock()

	if !ok {
		s.writeResult(req.ID, toolCallResult{
			Content: []contentBlock{{Type: "text", Text: fmt.Sprintf("unknown tool: %s", params.Name)}},
			IsError: true,
		})
		return
	}

	text, err := tool.Handler(params.Arguments)
	if err != nil {
		s.writeResult(req.ID, toolCallResult{
			Content: []contentBlock{{Type: "text", Text: err.Error()}},
			IsError: true,
		})
		return
	}

	s.writeResult(req.ID, toolCallResult{
		Content: []contentBlock{{Type: "text", Text: text}},
	})
}

func (s *Server) writeResult(id json.RawMessage, result interface{}) {
	resp := jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	}
	s.writeJSON(resp)
}

func (s *Server) writeError(id json.RawMessage, code int, message, data string) {
	resp := jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error: &rpcError{
			Code:    code,
			Message: message,
			Data:    data,
		},
	}
	s.writeJSON(resp)
}

func (s *Server) writeJSON(v interface{}) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	data = append(data, '\n')
	s.out.Write(data)
}
