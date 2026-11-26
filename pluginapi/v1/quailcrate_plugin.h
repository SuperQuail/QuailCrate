#ifndef QUAILCRATE_PLUGIN_V1_H
#define QUAILCRATE_PLUGIN_V1_H

#include <stdint.h>

#if defined(_WIN32) && defined(QUAILCRATE_PLUGIN_BUILD)
#define QUAILCRATE_PLUGIN_EXPORT __declspec(dllexport)
#else
#define QUAILCRATE_PLUGIN_EXPORT
#endif

#ifdef __cplusplus
extern "C" {
#endif

#define QUAILCRATE_ABI_VERSION 1
#define QUAILCRATE_ABI_VERSION_2 2

/*
 * Thread-safe host notification callback provided via quailcrate_bind_host.
 * Plugins may invoke this from any background thread to push a UTF-8 JSON
 * Notification (such as widget.refresh, page.notify, event.publish, or
 * state.set) to the host. The plugin owns the buffer for the duration of this
 * call. Returns 0 on success, or non-zero if the payload is invalid or the
 * worker is shutting down.
 */
typedef int32_t (*quailcrate_host_notify_fn)(
    const uint8_t *notification,
    uint32_t notification_length);

QUAILCRATE_PLUGIN_EXPORT uint32_t quailcrate_abi_version(void);

/*
 * Optional export (supported in ABI v1 and v2). When exported by the plugin
 * DLL, the worker invokes this once after loading the module and before the
 * first quailcrate_invoke("init") call.
 */
QUAILCRATE_PLUGIN_EXPORT void quailcrate_bind_host(quailcrate_host_notify_fn notify);

/*
 * Request and response bodies contain UTF-8 JSON. The host owns request
 * through the duration of this call. On success it uses quailcrate_free to
 * release the response buffer returned by the plugin.
 */
QUAILCRATE_PLUGIN_EXPORT int32_t quailcrate_invoke(
    const uint8_t *request,
    uint32_t request_length,
    uint8_t **response,
    uint32_t *response_length);

QUAILCRATE_PLUGIN_EXPORT void quailcrate_free(uint8_t *response);

#ifdef __cplusplus
}
#endif

#endif
