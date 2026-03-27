package mcp

import (
	"context"
	"crypto/rand"
	"eDBG/cli"
	"eDBG/utils"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultProtocolVersion = "2025-03-26"
	sessionHeaderName      = "Mcp-Session-Id"
	defaultContinueTimeout = 10 * time.Minute
)

type Server struct {
	client      *cli.Client
	packageName string
	libName     string
	httpServer  *http.Server

	mu       sync.Mutex
	toolMu   sync.Mutex
	sessions map[string]*session
}

type session struct {
	id              string
	protocolVersion string
	initialized     bool
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id,omitempty"`
	Result  interface{} `json:"result,omitempty"`
	Error   *rpcError   `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func NewServer(client *cli.Client, packageName string, libName string) *Server {
	return &Server{
		client:      client,
		packageName: packageName,
		libName:     libName,
		sessions:    make(map[string]*session),
	}
}

func (this *Server) Serve(addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", this.handleMCP)

	this.httpServer = &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	err := this.httpServer.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func (this *Server) Shutdown(ctx context.Context) error {
	if this.httpServer == nil {
		return nil
	}
	return this.httpServer.Shutdown(ctx)
}

func (this *Server) handleMCP(w http.ResponseWriter, r *http.Request) {
	if err := validateOrigin(r); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}

	this.setCommonHeaders(w)

	switch r.Method {
	case http.MethodPost:
		this.handlePost(w, r)
	case http.MethodDelete:
		this.handleDelete(w, r)
	case http.MethodOptions:
		w.WriteHeader(http.StatusNoContent)
	case http.MethodGet:
		http.Error(w, "SSE transport is not enabled on this server", http.StatusMethodNotAllowed)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (this *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	sessionID := r.Header.Get(sessionHeaderName)
	if sessionID == "" {
		http.Error(w, "missing MCP session id", http.StatusBadRequest)
		return
	}

	this.mu.Lock()
	delete(this.sessions, sessionID)
	this.mu.Unlock()

	w.WriteHeader(http.StatusNoContent)
}

func (this *Server) handlePost(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	var req rpcRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON-RPC request body", http.StatusBadRequest)
		return
	}

	if req.JSONRPC == "" {
		req.JSONRPC = "2.0"
	}
	if req.JSONRPC != "2.0" {
		this.writeRPCError(w, req.ID, -32600, "only JSON-RPC 2.0 is supported")
		return
	}

	if req.Method == "" {
		this.writeRPCError(w, req.ID, -32600, "missing method")
		return
	}

	if req.Method == "initialize" {
		this.handleInitialize(w, req)
		return
	}

	session, err := this.getSession(r.Header.Get(sessionHeaderName))
	if err != nil {
		this.writeRPCError(w, req.ID, -32001, err.Error())
		return
	}

	w.Header().Set(sessionHeaderName, session.id)
	w.Header().Set("MCP-Protocol-Version", session.protocolVersion)

	if req.Method == "notifications/initialized" {
		session.initialized = true
		w.WriteHeader(http.StatusAccepted)
		return
	}

	if !session.initialized {
		this.writeRPCError(w, req.ID, -32002, "session is not initialized")
		return
	}

	switch req.Method {
	case "ping":
		this.writeRPCResult(w, req.ID, map[string]interface{}{})
	case "tools/list":
		this.writeRPCResult(w, req.ID, map[string]interface{}{
			"tools": this.toolDefinitions(),
		})
	case "tools/call":
		this.handleToolCall(w, req)
	default:
		this.writeRPCError(w, req.ID, -32601, "method not found")
	}
}

func (this *Server) handleInitialize(w http.ResponseWriter, req rpcRequest) {
	var params struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(req.Params, &params)

	version := chooseProtocolVersion(params.ProtocolVersion)
	sessionID, err := randomSessionID()
	if err != nil {
		this.writeRPCError(w, req.ID, -32000, fmt.Sprintf("failed to create session: %v", err))
		return
	}

	this.mu.Lock()
	this.sessions[sessionID] = &session{
		id:              sessionID,
		protocolVersion: version,
	}
	this.mu.Unlock()

	w.Header().Set(sessionHeaderName, sessionID)
	w.Header().Set("MCP-Protocol-Version", version)

	this.writeRPCResult(w, req.ID, map[string]interface{}{
		"protocolVersion": version,
		"capabilities": map[string]interface{}{
			"tools": map[string]interface{}{},
		},
		"serverInfo": map[string]interface{}{
			"name":    "eDBG MCP",
			"version": "0.1.0",
		},
		"instructions": "eDBG MCP works in two phases: standby and stopped. In standby, only breakpoint management, run, info_break, and info_file are safe. The break tool always interprets offsets as virtual offsets and maps to eDBG vbreak internally. After continue returns with a hit, register, memory, disassembly, backtrace, and thread tools become available.",
	})
}

func (this *Server) handleToolCall(w http.ResponseWriter, req rpcRequest) {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		this.writeRPCError(w, req.ID, -32602, fmt.Sprintf("invalid tool call params: %v", err))
		return
	}
	if params.Name == "" {
		this.writeRPCError(w, req.ID, -32602, "missing tool name")
		return
	}

	args := make(map[string]interface{})
	if len(params.Arguments) > 0 {
		if err := json.Unmarshal(params.Arguments, &args); err != nil {
			this.writeRPCError(w, req.ID, -32602, fmt.Sprintf("invalid tool arguments: %v", err))
			return
		}
	}

	this.toolMu.Lock()
	defer this.toolMu.Unlock()

	text, err := this.callTool(params.Name, args)
	result := map[string]interface{}{
		"content": []map[string]interface{}{
			{
				"type": "text",
				"text": text,
			},
		},
	}
	if err != nil {
		result["isError"] = true
		if strings.TrimSpace(text) == "" {
			result["content"] = []map[string]interface{}{
				{
					"type": "text",
					"text": err.Error(),
				},
			}
		}
	}

	this.writeRPCResult(w, req.ID, result)
}

func (this *Server) callTool(name string, args map[string]interface{}) (string, error) {
	switch name {
	case "break":
		address, err := requiredStringArg(args, "address")
		if err != nil {
			return "", err
		}
		output := this.client.CaptureOutput(func() {
			this.client.HandleVBreak([]string{address})
		})
		return this.combineOutput(output, this.renderBreakpoints()), nil
	case "enable_breakpoint":
		id, err := requiredIntArg(args, "id")
		if err != nil {
			return "", err
		}
		output := this.client.CaptureOutput(func() {
			this.client.HandleChangeBrk([]string{strconv.Itoa(id)}, true)
		})
		return this.combineOutput(output, this.renderBreakpoints()), nil
	case "disable_breakpoint":
		id, err := requiredIntArg(args, "id")
		if err != nil {
			return "", err
		}
		output := this.client.CaptureOutput(func() {
			this.client.HandleChangeBrk([]string{strconv.Itoa(id)}, false)
		})
		return this.combineOutput(output, this.renderBreakpoints()), nil
	case "delete_breakpoint":
		id, err := requiredIntArg(args, "id")
		if err != nil {
			return "", err
		}
		output := this.client.CaptureOutput(func() {
			this.client.HandleDelete([]string{strconv.Itoa(id)})
		})
		return this.combineOutput(output, this.renderBreakpoints()), nil
	case "info_break":
		return this.renderBreakpoints(), nil
	case "info_file":
		return this.renderFileInfo(), nil
	case "info_register":
		if err := this.requireStopped("info_register"); err != nil {
			return "", err
		}
		return this.client.CaptureOutput(func() {
			this.client.HandleInfo([]string{"register"})
		}), nil
	case "info_thread":
		if err := this.requireStopped("info_thread"); err != nil {
			return "", err
		}
		return this.client.CaptureOutput(func() {
			this.client.HandleInfo([]string{"thread"})
		}), nil
	case "run":
		return this.handleRun(args)
	case "continue":
		return this.handleContinue(args)
	case "examine":
		if err := this.requireStopped("examine"); err != nil {
			return "", err
		}
		address, err := requiredStringArg(args, "address")
		if err != nil {
			return "", err
		}
		params := []string{address}
		if extra := optionalStringArg(args, "length_or_type", ""); extra != "" {
			params = append(params, extra)
		}
		return this.client.CaptureOutput(func() {
			this.client.HandleMemory(params)
		}), nil
	case "list":
		if err := this.requireStopped("list"); err != nil {
			return "", err
		}
		var params []string
		if address := optionalStringArg(args, "address", ""); address != "" {
			params = append(params, address)
		}
		if count, ok := optionalIntArg(args, "count"); ok {
			params = append(params, strconv.Itoa(count))
		}
		return this.client.CaptureOutput(func() {
			this.client.HandleList(params)
		}), nil
	case "backtrace":
		if err := this.requireStopped("backtrace"); err != nil {
			return "", err
		}
		mode := optionalStringArg(args, "mode", "unwind")
		return this.client.CaptureOutput(func() {
			if mode == "fp" {
				this.client.HandleBacktraceByFP(nil)
				return
			}
			this.client.HandleBacktraceByUnwind(nil)
		}), nil
	case "thread":
		if err := this.requireStopped("thread"); err != nil {
			return "", err
		}
		action := optionalStringArg(args, "action", "list")
		value := optionalStringArg(args, "value", "")
		params := []string{}
		switch action {
		case "list":
		case "add", "+", "name", "+n", "del", "-", "delete":
			if value == "" {
				return "", fmt.Errorf("thread action %q requires value", action)
			}
			params = []string{action, value}
		case "all":
			params = []string{"all"}
		default:
			return "", fmt.Errorf("unsupported thread action %q", action)
		}
		return this.client.CaptureOutput(func() {
			this.client.HandleThread(params)
		}), nil
	case "set_symbol":
		if err := this.requireStopped("set_symbol"); err != nil {
			return "", err
		}
		address, err := requiredStringArg(args, "address")
		if err != nil {
			return "", err
		}
		symbolName, err := requiredStringArg(args, "name")
		if err != nil {
			return "", err
		}
		return this.client.CaptureOutput(func() {
			this.client.HandleSet([]string{address, symbolName})
		}), nil
	case "write_memory":
		if err := this.requireStopped("write_memory"); err != nil {
			return "", err
		}
		address, err := requiredStringArg(args, "address")
		if err != nil {
			return "", err
		}
		hexValue, err := requiredStringArg(args, "hex")
		if err != nil {
			return "", err
		}
		return this.client.CaptureOutput(func() {
			this.client.HandleWrite([]string{address, hexValue})
		}), nil
	case "dump":
		if err := this.requireStopped("dump"); err != nil {
			return "", err
		}
		address, err := requiredStringArg(args, "address")
		if err != nil {
			return "", err
		}
		length, err := requiredStringArg(args, "length")
		if err != nil {
			return "", err
		}
		fileName, err := requiredStringArg(args, "file")
		if err != nil {
			return "", err
		}
		return this.client.CaptureOutput(func() {
			this.client.HandleDump([]string{address, length, fileName})
		}), nil
	case "quit":
		go this.client.CleanUp()
		return "eDBG is shutting down.", nil
	default:
		return "", fmt.Errorf("unknown tool %q", name)
	}
}

func (this *Server) handleRun(args map[string]interface{}) (string, error) {
	if this.client.IsStopped() {
		return "", fmt.Errorf("run is unavailable while the target is stopped; use continue or quit first")
	}
	if !this.hasEnabledBreakpoints() {
		return "", fmt.Errorf("run requires at least one enabled breakpoint so eDBG can regain control after launch")
	}

	this.client.Process.UpdatePidList()
	if len(this.client.Process.PidList) > 0 {
		return "", fmt.Errorf("target app %s is already running; stop it before using MCP run mode", this.packageName)
	}

	activity := optionalStringArg(args, "activity", "")
	output, err := this.launchApp(activity)
	if err != nil {
		return output, err
	}

	this.client.SetRunIssued(true)
	return strings.TrimSpace(output + "\n\nUse continue to wait for the next breakpoint."), nil
}

func (this *Server) handleContinue(args map[string]interface{}) (string, error) {
	if !this.client.IsStopped() && !this.client.HasRunIssued() {
		return "", fmt.Errorf("continue is unavailable in standby before run; set breakpoints, launch the app with run, then continue")
	}
	if !this.hasEnabledBreakpoints() {
		return "", fmt.Errorf("continue requires at least one enabled breakpoint")
	}

	timeout := defaultContinueTimeout
	if timeoutMS, ok := optionalIntArg(args, "timeout_ms"); ok && timeoutMS > 0 {
		timeout = time.Duration(timeoutMS) * time.Millisecond
	}

	seq := this.client.CurrentStopSequence()

	var continueErr error
	output := this.client.CaptureOutput(func() {
		if !this.client.HandleContinue() {
			continueErr = fmt.Errorf("continue failed")
		}
	})
	if continueErr != nil {
		if strings.TrimSpace(output) != "" {
			return output, continueErr
		}
		return "", continueErr
	}

	info, err := this.client.WaitForStopAfter(seq, timeout)
	if err != nil {
		return "", err
	}

	return this.renderStopInfo(info), nil
}

func (this *Server) renderBreakpoints() string {
	var builder strings.Builder

	enabledCount := 0
	for _, brk := range this.client.BrkManager.BreakPoints {
		if !brk.Deleted && brk.Enable {
			enabledCount++
		}
	}

	fmt.Fprintf(&builder, "Breakpoints (%d enabled):\n", enabledCount)
	if len(this.client.BrkManager.BreakPoints) == 0 {
		builder.WriteString("(none)")
		return builder.String()
	}

	printed := 0
	for id, brk := range this.client.BrkManager.BreakPoints {
		if brk.Deleted {
			continue
		}
		status := "disabled"
		if brk.Enable {
			status = "enabled"
		}

		if brk.Hardware {
			fmt.Fprintf(&builder, "[%d] %s hardware @ 0x%x\n", id, status, brk.Addr.Absolute)
			printed++
			continue
		}

		virtualOffset := brk.Addr.Offset
		if converted, err := utils.ConvertFileOffsetToVirtualOffset(brk.Addr.LibInfo.LibPath, brk.Addr.Offset); err == nil {
			virtualOffset = converted
		}

		fmt.Fprintf(
			&builder,
			"[%d] %s %s+0x%x (file 0x%x)\n",
			id,
			status,
			brk.Addr.LibInfo.LibName,
			virtualOffset,
			brk.Addr.Offset,
		)
		printed++
	}

	if printed == 0 {
		builder.WriteString("(none)")
	}

	return strings.TrimSpace(builder.String())
}

func (this *Server) renderFileInfo() string {
	var builder strings.Builder

	fmt.Fprintf(&builder, "Library: %s\n", this.client.Library.LibName)
	fmt.Fprintf(&builder, "Resolved Path: %s\n", this.client.Library.RealFilePath)
	fmt.Fprintf(&builder, "Working Path: %s\n", this.client.Library.LibPath)
	if this.client.Library.NonElfOffset != 0 {
		fmt.Fprintf(&builder, "Library Offset In APK: 0x%x\n", this.client.Library.NonElfOffset)
	}
	if stat, err := os.Stat(this.client.Library.RealFilePath); err == nil {
		fmt.Fprintf(&builder, "File Size: 0x%x\n", stat.Size())
	}

	pid := this.client.Process.WorkPid
	if pid == 0 {
		this.client.Process.UpdatePidList()
		if len(this.client.Process.PidList) > 0 {
			pid = this.client.Process.PidList[0]
		}
	}

	if pid == 0 {
		builder.WriteString("Mapped Range: not available because the app is not stopped or running under eDBG yet.")
		return builder.String()
	}

	mapsContent, err := utils.ReadMapsByPid(pid)
	if err != nil {
		fmt.Fprintf(&builder, "Mapped Range: unavailable (%v)", err)
		return builder.String()
	}

	var minAddr uint64
	var maxAddr uint64
	found := false
	for _, line := range strings.Split(mapsContent, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 6 || fields[5] != this.client.Library.RealFilePath {
			continue
		}
		addressParts := strings.Split(fields[0], "-")
		if len(addressParts) != 2 {
			continue
		}
		start, errStart := strconv.ParseUint(addressParts[0], 16, 64)
		end, errEnd := strconv.ParseUint(addressParts[1], 16, 64)
		if errStart != nil || errEnd != nil {
			continue
		}
		if !found {
			minAddr = start
			found = true
		}
		if end > maxAddr {
			maxAddr = end
		}
	}

	if !found {
		builder.WriteString("Mapped Range: not currently mapped in the selected process.")
		return builder.String()
	}

	fmt.Fprintf(&builder, "Mapped Base: 0x%x\n", minAddr)
	fmt.Fprintf(&builder, "Mapped End: 0x%x\n", maxAddr)
	fmt.Fprintf(&builder, "Mapped Size: 0x%x", maxAddr-minAddr)

	return strings.TrimSpace(builder.String())
}

func (this *Server) renderStopInfo(info *cli.StopInfo) string {
	var builder strings.Builder

	fmt.Fprintf(&builder, "Breakpoint hit.\n")
	if info.BreakpointID >= 0 {
		fmt.Fprintf(&builder, "Breakpoint ID: %d\n", info.BreakpointID)
	}
	fmt.Fprintf(&builder, "PID/TID: %d/%d\n", info.Pid, info.Tid)
	fmt.Fprintf(&builder, "PC: 0x%x\n", info.PC)
	fmt.Fprintf(&builder, "LR: 0x%x\n", info.LR)
	fmt.Fprintf(&builder, "SP: 0x%x\n", info.SP)
	if info.Library != "" {
		fmt.Fprintf(&builder, "Location: %s+0x%x (file 0x%x)\n", info.Library, info.VirtualOffset, info.FileOffset)
	}

	return strings.TrimSpace(builder.String())
}

func (this *Server) combineOutput(output string, fallback string) string {
	output = strings.TrimSpace(output)
	fallback = strings.TrimSpace(fallback)
	switch {
	case output == "":
		return fallback
	case fallback == "":
		return output
	default:
		return output + "\n\n" + fallback
	}
}

func (this *Server) requireStopped(toolName string) error {
	if this.client.IsStopped() {
		return nil
	}
	return fmt.Errorf("%s is only available after a breakpoint has been hit", toolName)
}

func (this *Server) hasEnabledBreakpoints() bool {
	for _, brk := range this.client.BrkManager.BreakPoints {
		if !brk.Deleted && brk.Enable {
			return true
		}
	}
	return false
}

func (this *Server) launchApp(activity string) (string, error) {
	if activity != "" {
		component := activity
		if !strings.Contains(component, "/") {
			component = this.packageName + "/" + component
		}
		return utils.RunCommand("am", "start", "-W", "-n", component)
	}

	resolved, err := utils.RunCommand("cmd", "package", "resolve-activity", "--brief", this.packageName)
	if err == nil {
		for _, line := range strings.Split(resolved, "\n") {
			line = strings.TrimSpace(line)
			if strings.Contains(line, "/") {
				return utils.RunCommand("am", "start", "-W", "-n", line)
			}
		}
	}

	return utils.RunCommand("monkey", "-p", this.packageName, "-c", "android.intent.category.LAUNCHER", "1")
}

func (this *Server) toolDefinitions() []map[string]interface{} {
	return []map[string]interface{}{
		toolDefinition("break", "按虚拟偏移设置断点，等价于 eDBG 的 vbreak。", map[string]interface{}{
			"type":     "object",
			"required": []string{"address"},
			"properties": map[string]interface{}{
				"address": map[string]interface{}{
					"type":        "string",
					"description": "虚拟偏移、绝对地址或 library.so+0xOFFSET。",
				},
			},
		}),
		toolDefinition("enable_breakpoint", "启用指定断点。", intIDSchema()),
		toolDefinition("disable_breakpoint", "禁用指定断点。", intIDSchema()),
		toolDefinition("delete_breakpoint", "删除指定断点。", intIDSchema()),
		toolDefinition("info_break", "查看当前断点列表。", emptySchema()),
		toolDefinition("info_file", "查看目标库文件信息；待命态也可用。", emptySchema()),
		toolDefinition("info_register", "查看当前寄存器信息，仅在断点命中后可用。", emptySchema()),
		toolDefinition("info_thread", "查看当前线程信息，仅在断点命中后可用。", emptySchema()),
		toolDefinition("run", "启动目标 app。为了避免失控，至少需要先设置一个启用的断点。", map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"activity": map[string]interface{}{
					"type":        "string",
					"description": "可选。显式指定 Activity；不传时自动解析 launcher activity。",
				},
			},
		}),
		toolDefinition("continue", "继续运行并阻塞等待直到下一个断点命中。", map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"timeout_ms": map[string]interface{}{
					"type":        "integer",
					"description": "可选。最长等待时间，默认 600000 毫秒。",
				},
			},
		}),
		toolDefinition("examine", "读取内存，仅在断点命中后可用。", map[string]interface{}{
			"type":     "object",
			"required": []string{"address"},
			"properties": map[string]interface{}{
				"address": map[string]interface{}{"type": "string"},
				"length_or_type": map[string]interface{}{
					"type":        "string",
					"description": "可选。长度表达式，或 ptr/str/int。",
				},
			},
		}),
		toolDefinition("list", "反汇编当前或指定地址，仅在断点命中后可用。", map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"address": map[string]interface{}{"type": "string"},
				"count":   map[string]interface{}{"type": "integer"},
			},
		}),
		toolDefinition("backtrace", "查看调用栈，仅在断点命中后可用。", map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"mode": map[string]interface{}{
					"type":        "string",
					"description": "unwind 或 fp，默认 unwind。",
				},
			},
		}),
		toolDefinition("thread", "管理线程过滤器，仅在断点命中后可用。", map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"action": map[string]interface{}{
					"type":        "string",
					"description": "list/add/name/del/all。",
				},
				"value": map[string]interface{}{
					"type":        "string",
					"description": "动作需要的值，例如线程列表里的索引或线程名。",
				},
			},
		}),
		toolDefinition("set_symbol", "为地址设置符号名，仅在断点命中后可用。", map[string]interface{}{
			"type":     "object",
			"required": []string{"address", "name"},
			"properties": map[string]interface{}{
				"address": map[string]interface{}{"type": "string"},
				"name":    map[string]interface{}{"type": "string"},
			},
		}),
		toolDefinition("write_memory", "向内存写入 hex 数据，仅在断点命中后可用。", map[string]interface{}{
			"type":     "object",
			"required": []string{"address", "hex"},
			"properties": map[string]interface{}{
				"address": map[string]interface{}{"type": "string"},
				"hex":     map[string]interface{}{"type": "string"},
			},
		}),
		toolDefinition("dump", "导出内存到文件，仅在断点命中后可用。", map[string]interface{}{
			"type":     "object",
			"required": []string{"address", "length", "file"},
			"properties": map[string]interface{}{
				"address": map[string]interface{}{"type": "string"},
				"length":  map[string]interface{}{"type": "string"},
				"file":    map[string]interface{}{"type": "string"},
			},
		}),
		toolDefinition("quit", "退出 eDBG。", emptySchema()),
	}
}

func (this *Server) setCommonHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Access-Control-Allow-Methods", "POST, GET, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Accept, Origin, "+sessionHeaderName)
}

func (this *Server) getSession(sessionID string) (*session, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("missing MCP session id")
	}

	this.mu.Lock()
	defer this.mu.Unlock()

	session, ok := this.sessions[sessionID]
	if !ok {
		return nil, fmt.Errorf("unknown MCP session id")
	}
	return session, nil
}

func (this *Server) writeRPCResult(w http.ResponseWriter, id json.RawMessage, result interface{}) {
	response := rpcResponse{
		JSONRPC: "2.0",
		ID:      parseID(id),
		Result:  result,
	}

	_ = json.NewEncoder(w).Encode(response)
}

func (this *Server) writeRPCError(w http.ResponseWriter, id json.RawMessage, code int, message string) {
	response := rpcResponse{
		JSONRPC: "2.0",
		ID:      parseID(id),
		Error: &rpcError{
			Code:    code,
			Message: message,
		},
	}

	_ = json.NewEncoder(w).Encode(response)
}

func toolDefinition(name string, description string, schema map[string]interface{}) map[string]interface{} {
	return map[string]interface{}{
		"name":        name,
		"description": description,
		"inputSchema": schema,
	}
}

func emptySchema() map[string]interface{} {
	return map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{},
	}
}

func intIDSchema() map[string]interface{} {
	return map[string]interface{}{
		"type":     "object",
		"required": []string{"id"},
		"properties": map[string]interface{}{
			"id": map[string]interface{}{
				"type": "integer",
			},
		},
	}
}

func parseID(raw json.RawMessage) interface{} {
	if len(raw) == 0 {
		return nil
	}

	var value interface{}
	if err := json.Unmarshal(raw, &value); err != nil {
		return string(raw)
	}
	return value
}

func requiredStringArg(args map[string]interface{}, key string) (string, error) {
	value, ok := args[key]
	if !ok {
		return "", fmt.Errorf("missing required argument %q", key)
	}
	str, ok := value.(string)
	if !ok || strings.TrimSpace(str) == "" {
		return "", fmt.Errorf("argument %q must be a non-empty string", key)
	}
	return strings.TrimSpace(str), nil
}

func optionalStringArg(args map[string]interface{}, key string, fallback string) string {
	value, ok := args[key]
	if !ok {
		return fallback
	}
	str, ok := value.(string)
	if !ok {
		return fallback
	}
	str = strings.TrimSpace(str)
	if str == "" {
		return fallback
	}
	return str
}

func requiredIntArg(args map[string]interface{}, key string) (int, error) {
	value, ok := args[key]
	if !ok {
		return 0, fmt.Errorf("missing required argument %q", key)
	}
	switch typed := value.(type) {
	case float64:
		return int(typed), nil
	case int:
		return typed, nil
	default:
		return 0, fmt.Errorf("argument %q must be an integer", key)
	}
}

func optionalIntArg(args map[string]interface{}, key string) (int, bool) {
	value, ok := args[key]
	if !ok {
		return 0, false
	}
	switch typed := value.(type) {
	case float64:
		return int(typed), true
	case int:
		return typed, true
	default:
		return 0, false
	}
}

func chooseProtocolVersion(requested string) string {
	switch requested {
	case "", defaultProtocolVersion:
		return defaultProtocolVersion
	default:
		return defaultProtocolVersion
	}
}

func randomSessionID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func validateOrigin(r *http.Request) error {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" || origin == "null" {
		return nil
	}

	parsed, err := url.Parse(origin)
	if err != nil {
		return fmt.Errorf("invalid Origin header")
	}

	host := parsed.Hostname()
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}

	return fmt.Errorf("Origin %q is not allowed", origin)
}
