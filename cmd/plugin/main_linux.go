//go:build linux && cgo

package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	cliproxy_host_call_fn call;
	cliproxy_host_free_fn free_buffer;
} cliproxy_host_api;

typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);

typedef struct {
	uint32_t abi_version;
	cliproxy_plugin_call_fn call;
	cliproxy_plugin_free_fn free_buffer;
	cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);

static cliproxy_host_api stored_host;
static int stored_host_valid = 0;

static int host_api_valid(const cliproxy_host_api* host) {
	return host != NULL && host->abi_version == 1 && host->call != NULL && host->free_buffer != NULL;
}

static void store_host_api(const cliproxy_host_api* host) {
	stored_host = *host;
	stored_host_valid = 1;
}

static void clear_host_api(void) {
	stored_host_valid = 0;
	stored_host.abi_version = 0;
	stored_host.host_ctx = NULL;
	stored_host.call = NULL;
	stored_host.free_buffer = NULL;
}


static int snapshot_host_api(cliproxy_host_api* snapshot) {
	if (snapshot == NULL || !stored_host_valid || stored_host.call == NULL || stored_host.free_buffer == NULL) {
		return 0;
	}
	*snapshot = stored_host;
	return 1;
}

static int call_host_snapshot(const cliproxy_host_api* snapshot, const char* method, const uint8_t* request, size_t request_len, cliproxy_buffer* response) {
	if (snapshot == NULL || snapshot->call == NULL) {
		return 1;
	}
	return snapshot->call(snapshot->host_ctx, method, request, request_len, response);
}

static void free_host_snapshot(const cliproxy_host_api* snapshot, void* ptr, size_t len) {
	if (snapshot != NULL && snapshot->free_buffer != NULL && ptr != NULL) {
		snapshot->free_buffer(ptr, len);
	}
}
*/
import "C"

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"sync"
	"unsafe"

	"github.com/sealofyou/cpa-quota-alert-plugin/internal/abi"
	"github.com/sealofyou/cpa-quota-alert-plugin/internal/pluginapp"
)

const abiVersion uint32 = 1

var (
	lifecycleMu sync.Mutex
	globalMu    sync.RWMutex
	globalApp   *pluginapp.App

	errHostCallback = errors.New("host callback failed")
)

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) (result C.int) {
	defer func() {
		if recover() != nil {
			result = 1
		}
	}()
	if plugin == nil || C.host_api_valid(host) == 0 {
		return 1
	}
	hostSnapshot := *host

	lifecycleMu.Lock()
	defer lifecycleMu.Unlock()

	globalMu.Lock()
	old := globalApp
	globalApp = nil
	globalMu.Unlock()
	if old != nil {
		// CPA v7.2.83/v7.2.120 guards native plugins by marking the
		// client closed and waiting for active plugin.call entries before
		// invoking native shutdown. Re-init follows the same non-reentrant
		// lifecycle boundary here: detach the old app without holding
		// globalMu, drain plugin-owned management work, then install the new
		// host snapshot. Do not generalize this blocking wait to unknown
		// hosts that might synchronously re-enter lifecycle callbacks.
		old.ShutdownAndWait()
	}

	app := pluginapp.NewWithHost(os.Getenv, newHostClientFactory(hostSnapshot))
	globalMu.Lock()
	C.store_host_api(&hostSnapshot)
	globalApp = app
	globalMu.Unlock()

	plugin.abi_version = C.uint32_t(abiVersion)
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) (result C.int) {
	if response != nil {
		response.ptr = nil
		response.len = 0
	}
	defer func() {
		if recover() != nil {
			_ = writeABIResponse(response, abiErrorEnvelope("plugin_panic", "plugin call failed"))
			result = 1
		}
	}()
	if response == nil {
		return 1
	}
	if method == nil {
		_ = writeABIResponse(response, abiErrorEnvelope("invalid_method", "method is required"))
		return 1
	}
	if uint64(requestLen) > uint64(math.MaxInt32) || (requestLen > 0 && request == nil) {
		_ = writeABIResponse(response, abiErrorEnvelope("invalid_request", "request is invalid"))
		return 1
	}

	var requestBytes []byte
	if requestLen > 0 {
		requestBytes = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}
	app := currentApp()
	if app == nil {
		_ = writeABIResponse(response, abiErrorEnvelope("plugin_unavailable", "plugin is unavailable"))
		return 1
	}
	raw, callCode := app.Call(C.GoString(method), requestBytes)
	if !writeABIResponse(response, raw) {
		return 1
	}
	return C.int(callCode)
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, length C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
	_ = length
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {
	defer func() { _ = recover() }()
	lifecycleMu.Lock()
	defer lifecycleMu.Unlock()

	globalMu.Lock()
	app := globalApp
	globalApp = nil
	globalMu.Unlock()
	if app != nil {
		// Official CPA v7.2.83/v7.2.120 drains active plugin.call entries
		// before calling this native shutdown hook, and the Unix dynamic
		// library client deletes the host callback entry, frees hostCtx, and
		// dlcloses only after shutdown returns. Therefore native shutdown
		// must synchronously drain plugin-owned management work here so no
		// plugin goroutine can run after the .so may be unloaded.
		app.ShutdownAndWait()
	}
	C.clear_host_api()
}

func currentApp() *pluginapp.App {
	globalMu.RLock()
	defer globalMu.RUnlock()
	return globalApp
}

type nativeHostCaller struct {
	snapshot C.cliproxy_host_api
}

func (c nativeHostCaller) Call(ctx context.Context, method string, request []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if method == "" || len(request) > math.MaxInt32 {
		return nil, errHostCallback
	}

	// The CPA ABI is synchronous and has no per-call cancellation or timeout
	// field. Each app owns the host snapshot captured during init, so in-flight
	// management calls keep using their original host even across re-init.
	snapshot := c.snapshot
	if snapshot.call == nil || snapshot.free_buffer == nil {
		return nil, errHostCallback
	}

	cMethod := C.CString(method)
	if cMethod == nil {
		return nil, errHostCallback
	}
	defer C.free(unsafe.Pointer(cMethod))

	var requestPtr *C.uint8_t
	if len(request) > 0 {
		allocated := C.CBytes(request)
		if allocated == nil {
			return nil, errHostCallback
		}
		defer C.free(allocated)
		requestPtr = (*C.uint8_t)(allocated)
	}

	var response C.cliproxy_buffer
	callCode := C.call_host_snapshot(&snapshot, cMethod, requestPtr, C.size_t(len(request)), &response)
	if response.ptr != nil {
		defer C.free_host_snapshot(&snapshot, response.ptr, response.len)
	}
	if callCode != 0 || response.ptr == nil || response.len == 0 || uint64(response.len) > uint64(math.MaxInt32) {
		return nil, errHostCallback
	}
	raw := C.GoBytes(response.ptr, C.int(response.len))
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return raw, nil
}

func newHostClient(hostCallbackID string) *abi.Client {
	return newHostClientFactory(snapshotStoredHost())(hostCallbackID)
}

func newHostClientFactory(snapshot C.cliproxy_host_api) pluginapp.HostClientFactory {
	return func(hostCallbackID string) *abi.Client {
		return abi.NewClient(nativeHostCaller{snapshot: snapshot}, hostCallbackID)
	}
}

func snapshotStoredHost() C.cliproxy_host_api {
	var snapshot C.cliproxy_host_api
	globalMu.RLock()
	_ = C.snapshot_host_api(&snapshot)
	globalMu.RUnlock()
	return snapshot
}

func writeABIResponse(response *C.cliproxy_buffer, raw []byte) bool {
	if response == nil || len(raw) == 0 {
		return false
	}
	ptr := C.CBytes(raw)
	if ptr == nil {
		return false
	}
	response.ptr = ptr
	response.len = C.size_t(len(raw))
	return true
}

func abiErrorEnvelope(code, message string) []byte {
	raw, err := json.Marshal(pluginapp.Envelope{OK: false, Error: &pluginapp.EnvelopeError{Code: code, Message: message}})
	if err != nil {
		return []byte(`{"ok":false,"error":{"code":"plugin_error","message":"plugin response failed"}}`)
	}
	return raw
}
