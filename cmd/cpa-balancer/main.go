// Command cpa-balancer is a CLIProxyAPI v8 native plugin (go build -buildmode=c-shared).
package main

/*
#include <stdint.h>
#include <stdlib.h>
typedef struct { void* ptr; size_t len; } cliproxy_buffer;
typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);
typedef struct { uint32_t abi_version; void* host_ctx; cliproxy_host_call_fn call; cliproxy_host_free_fn free_buffer; } cliproxy_host_api;
typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);
typedef struct { uint32_t abi_version; cliproxy_plugin_call_fn call; cliproxy_plugin_free_fn free_buffer; cliproxy_plugin_shutdown_fn shutdown; } cliproxy_plugin_api;
extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);
static const cliproxy_host_api* stored_host;
static void store_host_api(const cliproxy_host_api* host) { stored_host = host; }
static int call_host_api(const char* method, const uint8_t* request, size_t request_len, cliproxy_buffer* response) {
	if (stored_host == NULL || stored_host->call == NULL) { return 1; }
	return stored_host->call(stored_host->host_ctx, method, request, request_len, response);
}
static void free_host_buffer(void* ptr, size_t len) {
	if (stored_host != NULL && stored_host->free_buffer != NULL && ptr != NULL) { stored_host->free_buffer(ptr, len); }
}
*/
import "C"

import (
	"encoding/json"
	"fmt"
	"runtime/debug"
	"sync"
	"time"
	"unsafe"

	"github.com/adnankurt/cpa-balancer/internal/config"
	"github.com/adnankurt/cpa-balancer/internal/engine"
	"github.com/adnankurt/cpa-balancer/internal/web"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const tickInterval = 30 * time.Second

var (
	mu         sync.Mutex
	current    *engine.Engine
	configYAML string // the config current was built from
	stop       chan struct{}
	host       hostClient
)

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(h *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if plugin == nil {
		return 1
	}
	C.store_host_api(h)
	plugin.abi_version = C.uint32_t(pluginabi.ABIVersion)
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) (rc C.int) {
	var req []byte
	if request != nil && requestLen > 0 {
		req = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}
	name := C.GoString(method)
	raw := dispatch(name, req)
	buf := C.malloc(C.size_t(len(raw)))
	copy(unsafe.Slice((*byte)(buf), len(raw)), raw)
	response.ptr, response.len = buf, C.size_t(len(raw))
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, _ C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() { shutdown() }

func ok(v any) []byte {
	result, err := json.Marshal(v)
	if err != nil {
		return fail(err)
	}
	raw, _ := json.Marshal(pluginabi.Envelope{OK: true, Result: result})
	return raw
}

func fail(err error) []byte {
	raw, _ := pluginabi.NewErrorEnvelope("plugin_error", err.Error())
	return raw
}

// dispatch never lets a panic cross into the host: picks fall back to the built-in scheduler.
func dispatch(method string, req []byte) (raw []byte) {
	defer func() {
		if r := recover(); r != nil {
			host.Log("error", fmt.Sprintf("cpa-balancer: panic in %s: %v\n%s", method, r, debug.Stack()))
			if method == pluginabi.MethodSchedulerPick {
				raw = ok(pluginapi.SchedulerPickResponse{DelegateBuiltin: pluginapi.SchedulerBuiltinFillFirst, Handled: true})
				return
			}
			raw = fail(fmt.Errorf("panic: %v", r))
		}
	}()
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		return configure(req)
	case pluginabi.MethodSchedulerPick:
		var r pluginapi.SchedulerPickRequest
		if err := json.Unmarshal(req, &r); err != nil {
			return fail(err)
		}
		if e := engineNow(); e != nil {
			return ok(e.Pick(r))
		}
		return ok(pluginapi.SchedulerPickResponse{DelegateBuiltin: pluginapi.SchedulerBuiltinFillFirst, Handled: true})
	case pluginabi.MethodUsageHandle:
		var r pluginapi.UsageRecord
		if err := json.Unmarshal(req, &r); err != nil {
			return fail(err)
		}
		if e := engineNow(); e != nil {
			e.OnUsage(r)
		}
		return ok(map[string]any{})
	case pluginabi.MethodManagementRegister:
		return ok(web.Registration())
	case pluginabi.MethodManagementHandle:
		var r pluginapi.ManagementRequest
		if err := json.Unmarshal(req, &r); err != nil {
			return fail(err)
		}
		return ok(web.Handle(r, func() any {
			if e := engineNow(); e != nil {
				return e.Status()
			}
			return map[string]string{"error": "not configured"}
		}))
	case pluginabi.MethodPluginShutdown, pluginabi.MethodPluginQuiesce:
		shutdown()
		return ok(map[string]any{})
	}
	return ok(map[string]any{})
}

type lifecycleRequest struct {
	ConfigYAML []byte `json:"config_yaml"`
}

func configure(req []byte) []byte {
	var lr lifecycleRequest
	if err := json.Unmarshal(req, &lr); err != nil {
		return fail(err)
	}
	cfg, err := config.Parse(lr.ConfigYAML)
	mu.Lock()
	unchanged := current != nil && configYAML == string(lr.ConfigYAML)
	mu.Unlock()
	switch {
	case err != nil:
		host.Log("error", "cpa-balancer: "+err.Error()+"; keeping the previous configuration")
	case unchanged:
		// CPA re-sends the same config several times at startup; keep the running engine.
	default:
		e := engine.New(cfg, host, time.Now)
		mu.Lock()
		previous := current
		current, configYAML = e, string(lr.ConfigYAML)
		if stop == nil {
			stop = make(chan struct{})
			go loop(stop)
		}
		mu.Unlock()
		if previous != nil {
			previous.Shutdown()
		}
		go safeTick()
	}
	return ok(map[string]any{
		"schema_version": pluginabi.SchemaVersion,
		"metadata": pluginapi.Metadata{Name: web.PluginID, Version: "0.2.0", Author: "Adnan Kurt",
			GitHubRepository: "https://github.com/adnankurt/cpa-balancer"},
		"capabilities": map[string]bool{"scheduler": true, "scheduler_across_priorities": true,
			"usage_plugin": true, "management_api": true},
	})
}

func engineNow() *engine.Engine {
	mu.Lock()
	defer mu.Unlock()
	return current
}

func loop(done chan struct{}) {
	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			safeTick()
		case <-done:
			return
		}
	}
}

// safeTick recovers here because a panic in a plugin goroutine would take down CPA itself.
func safeTick() {
	defer func() {
		if r := recover(); r != nil {
			host.Log("error", fmt.Sprintf("cpa-balancer: panic in tick: %v\n%s", r, debug.Stack()))
		}
	}()
	if e := engineNow(); e != nil {
		e.Tick()
	}
}

func shutdown() {
	mu.Lock()
	e, done := current, stop
	stop = nil
	mu.Unlock()
	if done != nil {
		close(done)
	}
	if e != nil {
		e.Shutdown()
	}
}

// callHost invokes a host callback; it lives here because the C helpers are static to this file.
func callHost(method string, payload []byte) ([]byte, error) {
	cMethod := C.CString(method)
	defer C.free(unsafe.Pointer(cMethod))
	var reqPtr *C.uint8_t
	if len(payload) > 0 {
		reqPtr = (*C.uint8_t)(C.CBytes(payload))
		defer C.free(unsafe.Pointer(reqPtr))
	}
	var resp C.cliproxy_buffer
	rc := C.call_host_api(cMethod, reqPtr, C.size_t(len(payload)), &resp)
	var raw []byte
	if resp.ptr != nil {
		raw = C.GoBytes(resp.ptr, C.int(resp.len))
		C.free_host_buffer(resp.ptr, resp.len)
	}
	if rc != 0 && len(raw) == 0 {
		return nil, fmt.Errorf("host call %s failed (rc=%d)", method, int(rc))
	}
	return raw, nil
}
