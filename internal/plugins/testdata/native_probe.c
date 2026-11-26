#define QUAILCRATE_PLUGIN_BUILD
#include "quailcrate_plugin.h"
#define WIN32_LEAN_AND_MEAN
#include <winsock2.h>
#include <windows.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#pragma comment(lib, "ws2_32.lib")
#pragma comment(lib, "user32.lib")

static quailcrate_host_notify_fn g_host_notify = NULL;
static volatile LONG g_push_counter = 0;

typedef int32_t (*companion_value_fn)(void);

typedef struct {
    uint8_t *payload;
    uint32_t length;
} async_push_job;

static DWORD WINAPI async_push_thread(LPVOID param)
{
    async_push_job *job = (async_push_job *)param;
    Sleep(15);
    if (g_host_notify != NULL && job != NULL && job->payload != NULL && job->length > 0) {
        g_host_notify(job->payload, job->length);
    }
    if (job != NULL) {
        free(job->payload);
        free(job);
    }
    return 0;
}

static DWORD schedule_push(const uint8_t *payload, uint32_t length)
{
    if (g_host_notify == NULL || payload == NULL || length == 0) {
        return ERROR_INVALID_STATE;
    }
    async_push_job *job = (async_push_job *)malloc(sizeof(async_push_job));
    if (job == NULL) return ERROR_OUTOFMEMORY;
    job->payload = (uint8_t *)malloc(length);
    if (job->payload == NULL) {
        free(job);
        return ERROR_OUTOFMEMORY;
    }
    memcpy(job->payload, payload, length);
    job->length = length;
    HANDLE thread = CreateThread(NULL, 0, async_push_thread, job, 0, NULL);
    if (thread == NULL) {
        DWORD err = GetLastError();
        free(job->payload);
        free(job);
        return err;
    }
    CloseHandle(thread);
    return 0;
}

static DWORD loopback(const uint8_t *digits, uint32_t length)
{
    unsigned long port = 0;
    for (uint32_t i = 0; i < length; ++i) {
        if (digits[i] < '0' || digits[i] > '9') return ERROR_INVALID_PARAMETER;
        port = 10 * port + digits[i] - '0';
    }
    WSADATA data;
    int result = WSAStartup(MAKEWORD(2, 2), &data);
    if (result != 0) return (DWORD)result;
    SOCKET sock = socket(AF_INET, SOCK_STREAM, IPPROTO_TCP);
    if (sock == INVALID_SOCKET) {
        result = WSAGetLastError();
    } else {
        struct sockaddr_in target = {0};
        target.sin_family = AF_INET;
        target.sin_addr.s_addr = htonl(INADDR_LOOPBACK);
        target.sin_port = htons((unsigned short)port);
        if (connect(sock, (struct sockaddr *)&target, sizeof(target)) == SOCKET_ERROR) {
            result = WSAGetLastError();
        }
        closesocket(sock);
    }
    WSACleanup();
    return (DWORD)result;
}

static DWORD spawn_child_process(void)
{
    char cmdline[] = "cmd.exe /c exit 0";
    STARTUPINFOA si;
    PROCESS_INFORMATION pi;
    memset(&si, 0, sizeof(si));
    memset(&pi, 0, sizeof(pi));
    si.cb = sizeof(si);
    if (!CreateProcessA(NULL, cmdline, NULL, NULL, FALSE, CREATE_NO_WINDOW, NULL, NULL, &si, &pi)) {
        return GetLastError();
    }
    DWORD wait = WaitForSingleObject(pi.hProcess, 3000);
    DWORD code = 1;
    if (wait == WAIT_OBJECT_0) {
        GetExitCodeProcess(pi.hProcess, &code);
    } else {
        TerminateProcess(pi.hProcess, 1);
        code = ERROR_TIMEOUT;
    }
    CloseHandle(pi.hThread);
    CloseHandle(pi.hProcess);
    return code;
}

static int32_t respond_text(const char *text, uint8_t **response, uint32_t *size)
{
    size_t len = strlen(text);
    char *buffer = (char *)malloc(len + 1);
    if (buffer == NULL) return -1;
    memcpy(buffer, text, len);
    *response = (uint8_t *)buffer;
    *size = (uint32_t)len;
    return 0;
}

static int extract_json_string(const char *json, size_t len, const char *key, char *out, size_t out_cap)
{
    char needle[64];
    int n = snprintf(needle, sizeof(needle), "\"%s\":\"", key);
    if (n <= 0 || (size_t)n >= sizeof(needle)) return 0;
    const char *p = json;
    const char *end = json + len;
    while (p + n < end) {
        if (memcmp(p, needle, (size_t)n) == 0) {
            p += n;
            size_t i = 0;
            while (p < end && *p != '"' && i + 1 < out_cap) {
                out[i++] = *p++;
            }
            out[i] = '\0';
            return 1;
        }
        ++p;
    }
    return 0;
}

uint32_t quailcrate_abi_version(void) { return QUAILCRATE_ABI_VERSION_2; }

void quailcrate_bind_host(quailcrate_host_notify_fn notify)
{
    g_host_notify = notify;
}

int32_t quailcrate_invoke(const uint8_t *request, uint32_t length, uint8_t **response, uint32_t *size)
{
    static const char icon_request[] = "{\"requestId\":\"icon-policy\",\"op\":\"search\"";
    if (length >= sizeof(icon_request) - 1 && memcmp(request, icon_request, sizeof(icon_request) - 1) == 0) {
        static const char result[] = "{\"requestId\":\"icon-policy\",\"ok\":true,\"data\":{\"results\":["
            "{\"id\":\"test\",\"title\":\"Icon fixture\",\"icon\":\"favicon:https://example.invalid/fixture\","
            "\"command\":{\"id\":\"select\"}}]}}";
        return respond_text(result, response, size);
    }
    if (length > 0 && request[0] == '{') {
        char req_id[128] = {0};
        char op[64] = {0};
        if (extract_json_string((const char *)request, length, "requestId", req_id, sizeof(req_id)) &&
            extract_json_string((const char *)request, length, "op", op, sizeof(op))) {
            char body[1024];
            if (strcmp(op, "init") == 0) {
                snprintf(body, sizeof(body),
                    "{\"requestId\":\"%s\",\"ok\":true,\"data\":{"
                    "\"commands\":[{\"id\":\"push\",\"title\":\"Push\"}],"
                    "\"widgets\":[{\"id\":\"live\",\"title\":\"Live\",\"width\":200,\"height\":120,\"default\":true}],"
                    "\"appTabs\":[{\"id\":\"panel\",\"title\":\"Panel\"}]"
                    "}}", req_id);
                return respond_text(body, response, size);
            }
            if (strcmp(op, "widget_state") == 0) {
                long count = InterlockedCompareExchange(&g_push_counter, 0, 0);
                snprintf(body, sizeof(body),
                    "{\"requestId\":\"%s\",\"ok\":true,\"data\":{\"view\":{"
                    "\"title\":\"Live %ld\",\"controls\":[{\"id\":\"status\",\"type\":\"text\",\"text\":\"count=%ld\"}]"
                    "}}}", req_id, count, count);
                return respond_text(body, response, size);
            }
            if (strcmp(op, "tab_state") == 0) {
                long count = InterlockedCompareExchange(&g_push_counter, 0, 0);
                snprintf(body, sizeof(body),
                    "{\"requestId\":\"%s\",\"ok\":true,\"data\":{\"view\":{"
                    "\"title\":\"Panel %ld\",\"controls\":[{\"id\":\"status\",\"type\":\"text\",\"text\":\"count=%ld\"}]"
                    "}}}", req_id, count, count);
                return respond_text(body, response, size);
            }
            if (strcmp(op, "command") == 0) {
                long next = InterlockedIncrement(&g_push_counter);
                char note1[256];
                int n1 = snprintf(note1, sizeof(note1),
                    "{\"type\":\"widget.refresh\",\"widgetId\":\"live\",\"state\":{\"count\":%ld}}", next);
                if (n1 > 0) schedule_push((const uint8_t *)note1, (uint32_t)n1);
                char note2[256];
                int n2 = snprintf(note2, sizeof(note2),
                    "{\"type\":\"event.publish\",\"topic\":\"test.native-push.tick\",\"args\":{\"count\":%ld}}", next);
                if (n2 > 0) schedule_push((const uint8_t *)note2, (uint32_t)n2);
                snprintf(body, sizeof(body), "{\"requestId\":\"%s\",\"ok\":true,\"data\":{}}", req_id);
                return respond_text(body, response, size);
            }
            snprintf(body, sizeof(body), "{\"requestId\":\"%s\",\"ok\":true,\"data\":{}}", req_id);
            return respond_text(body, response, size);
        }
    }
    if (length == 4 && memcmp(request, "wait", 4) == 0) {
        Sleep(INFINITE);
    }
    if (length == 5 && memcmp(request, "brief", 5) == 0) {
        Sleep(120);
    }
    if (length == 4 && memcmp(request, "exit", 4) == 0) {
        ExitProcess(17);
    }
    if (length == 6 && memcmp(request, "user32", 6) == 0) {
        int cx = GetSystemMetrics(SM_CXSCREEN);
        return respond_text(cx > 0 ? "0" : "1", response, size);
    }
    if (length == 5 && memcmp(request, "child", 5) == 0) {
        char buf[16];
        snprintf(buf, sizeof(buf), "%lu", spawn_child_process());
        return respond_text(buf, response, size);
    }
    if (length > 4 && memcmp(request, "env:", 4) == 0) {
        char name[128];
        size_t n = length - 4;
        if (n >= sizeof(name)) return -1;
        memcpy(name, request + 4, n);
        name[n] = '\0';
        char value[4096];
        DWORD got = GetEnvironmentVariableA(name, value, (DWORD)sizeof(value));
        if (got == 0 || got >= sizeof(value)) {
            return respond_text("<missing>", response, size);
        }
        return respond_text(value, response, size);
    }
    if (length > 5 && memcmp(request, "comp:", 5) == 0) {
        char dllname[256];
        size_t n = length - 5;
        if (n >= sizeof(dllname)) return -1;
        memcpy(dllname, request + 5, n);
        dllname[n] = '\0';
        HMODULE mod = LoadLibraryA(dllname);
        if (mod == NULL) {
            char buf[32];
            snprintf(buf, sizeof(buf), "err:%lu", GetLastError());
            return respond_text(buf, response, size);
        }
        companion_value_fn fn = (companion_value_fn)GetProcAddress(mod, "companion_value");
        int32_t val = fn != NULL ? fn() : -1;
        FreeLibrary(mod);
        char buf[32];
        snprintf(buf, sizeof(buf), "%d", val);
        return respond_text(buf, response, size);
    }
    if (length > 5 && memcmp(request, "push:", 5) == 0) {
        DWORD err = schedule_push(request + 5, length - 5);
        char buf[16];
        snprintf(buf, sizeof(buf), "%lu", err);
        return respond_text(buf, response, size);
    }
    DWORD error = 0;
    if (length > 5 && length <= 10 && memcmp(request, "loop:", 5) == 0) {
        error = loopback(request + 5, length - 5);
    } else if (length > 5 && (memcmp(request, "read:", 5) == 0 || memcmp(request, "writ:", 5) == 0)) {
        wchar_t path[32768];
        int count = MultiByteToWideChar(CP_UTF8, MB_ERR_INVALID_CHARS, (const char *)request + 5,
            (int)length - 5, path, (int)(sizeof(path) / sizeof(path[0]) - 1));
        if (count <= 0) return -1;
        path[count] = L'\0';
        DWORD access = request[0] == 'r' ? GENERIC_READ : GENERIC_WRITE;
        HANDLE file = CreateFileW(path, access, FILE_SHARE_READ | FILE_SHARE_WRITE, NULL, OPEN_EXISTING, 0, NULL);
        if (file == INVALID_HANDLE_VALUE) error = GetLastError();
        else CloseHandle(file);
    }
    char buffer[16];
    snprintf(buffer, sizeof(buffer), "%lu", error);
    return respond_text(buffer, response, size);
}

void quailcrate_free(uint8_t *response) { free(response); }
