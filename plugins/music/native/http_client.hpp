#pragma once

#ifndef WIN32_LEAN_AND_MEAN
#define WIN32_LEAN_AND_MEAN
#endif
#ifndef NOMINMAX
#define NOMINMAX
#endif
#include <windows.h>
#include <wincrypt.h>
#include <winhttp.h>

#pragma comment(lib, "winhttp.lib")
#pragma comment(lib, "crypt32.lib")

#include "bvid.hpp"
#include "json.hpp"

#include <algorithm>
#include <atomic>
#include <cctype>
#include <cstdint>
#include <functional>
#include <mutex>
#include <string>
#include <vector>

namespace music {

inline std::wstring utf8_to_wide(const std::string& s) {
    if (s.empty()) return L"";
    int n = MultiByteToWideChar(CP_UTF8, 0, s.data(), static_cast<int>(s.size()), nullptr, 0);
    if (n <= 0) return L"";
    std::wstring w(static_cast<size_t>(n), L'\0');
    MultiByteToWideChar(CP_UTF8, 0, s.data(), static_cast<int>(s.size()), &w[0], n);
    return w;
}

inline std::string wide_to_utf8(const std::wstring& w) {
    if (w.empty()) return "";
    int n = WideCharToMultiByte(CP_UTF8, 0, w.data(), static_cast<int>(w.size()), nullptr, 0, nullptr, nullptr);
    if (n <= 0) return "";
    std::string s(static_cast<size_t>(n), '\0');
    WideCharToMultiByte(CP_UTF8, 0, w.data(), static_cast<int>(w.size()), &s[0], n, nullptr, nullptr);
    return s;
}

inline std::string to_lower_ascii(std::string s) {
    for (char& c : s) c = static_cast<char>(std::tolower(static_cast<unsigned char>(c)));
    return s;
}

inline bool write_file_atomic(const std::wstring& path, const void* data, size_t size) {
    std::wstring tmp = path + L".tmp";
    HANDLE f = CreateFileW(tmp.c_str(), GENERIC_WRITE, 0, nullptr, CREATE_ALWAYS, FILE_ATTRIBUTE_NORMAL, nullptr);
    if (f == INVALID_HANDLE_VALUE) return false;
    const uint8_t* ptr = static_cast<const uint8_t*>(data);
    size_t remaining = size;
    bool ok = true;
    while (remaining > 0) {
        DWORD chunk = static_cast<DWORD>(std::min<size_t>(remaining, 1 << 20));
        DWORD written = 0;
        if (!WriteFile(f, ptr, chunk, &written, nullptr) || written != chunk) {
            ok = false;
            break;
        }
        ptr += written;
        remaining -= written;
    }
    CloseHandle(f);
    if (!ok) {
        DeleteFileW(tmp.c_str());
        return false;
    }
    if (!MoveFileExW(tmp.c_str(), path.c_str(), MOVEFILE_REPLACE_EXISTING | MOVEFILE_WRITE_THROUGH)) {
        DeleteFileW(tmp.c_str());
        return false;
    }
    return true;
}

inline bool read_file_all(const std::wstring& path, std::string& out, size_t max_bytes = 16 << 20) {
    HANDLE f = CreateFileW(path.c_str(), GENERIC_READ, FILE_SHARE_READ | FILE_SHARE_WRITE | FILE_SHARE_DELETE,
                           nullptr, OPEN_EXISTING, FILE_ATTRIBUTE_NORMAL, nullptr);
    if (f == INVALID_HANDLE_VALUE) return false;
    LARGE_INTEGER sz{};
    if (!GetFileSizeEx(f, &sz) || sz.QuadPart < 0 || static_cast<uint64_t>(sz.QuadPart) > max_bytes) {
        CloseHandle(f);
        return false;
    }
    out.resize(static_cast<size_t>(sz.QuadPart));
    size_t offset = 0;
    while (offset < out.size()) {
        DWORD chunk = static_cast<DWORD>(std::min<size_t>(out.size() - offset, 1 << 20));
        DWORD got = 0;
        if (!ReadFile(f, &out[offset], chunk, &got, nullptr) || got == 0) {
            CloseHandle(f);
            return false;
        }
        offset += got;
    }
    CloseHandle(f);
    return true;
}

struct ParsedURL {
    bool valid = false;
    bool is_https = false;
    std::string host;
    INTERNET_PORT port = 0;
    std::string path_and_query;
    std::string origin;
};

inline ParsedURL parse_url(const std::string& raw) {
    ParsedURL out;
    std::wstring w = utf8_to_wide(raw);
    if (w.empty()) return out;
    URL_COMPONENTS uc{};
    uc.dwStructSize = sizeof(uc);
    uc.dwSchemeLength = static_cast<DWORD>(-1);
    uc.dwHostNameLength = static_cast<DWORD>(-1);
    uc.dwUrlPathLength = static_cast<DWORD>(-1);
    uc.dwExtraInfoLength = static_cast<DWORD>(-1);
    if (!WinHttpCrackUrl(w.c_str(), static_cast<DWORD>(w.size()), 0, &uc)) {
        return out;
    }
    if (uc.nScheme != INTERNET_SCHEME_HTTP && uc.nScheme != INTERNET_SCHEME_HTTPS) {
        return out;
    }
    out.valid = true;
    out.is_https = (uc.nScheme == INTERNET_SCHEME_HTTPS);
    out.host = wide_to_utf8(std::wstring(uc.lpszHostName, uc.dwHostNameLength));
    out.port = uc.nPort;
    std::wstring path(uc.lpszUrlPath, uc.dwUrlPathLength);
    if (uc.dwExtraInfoLength > 0) {
        path.append(uc.lpszExtraInfo, uc.dwExtraInfoLength);
    }
    if (path.empty()) path = L"/";
    out.path_and_query = wide_to_utf8(path);
    out.origin = (out.is_https ? "https://" : "http://") + out.host + ":" + std::to_string(out.port);
    return out;
}

inline std::string resolve_redirect_url(const ParsedURL& base, const std::string& location) {
    std::string loc = trim_str(location);
    if (loc.empty()) return "";
    if (loc.rfind("http://", 0) == 0 || loc.rfind("https://", 0) == 0) {
        return loc;
    }
    std::string scheme = base.is_https ? "https:" : "http:";
    if (loc.rfind("//", 0) == 0) {
        return scheme + loc;
    }
    std::string authority = scheme + "//" + base.host;
    if ((base.is_https && base.port != 443) || (!base.is_https && base.port != 80)) {
        authority += ":" + std::to_string(base.port);
    }
    if (!loc.empty() && loc[0] == '/') {
        return authority + loc;
    }
    size_t slash = base.path_and_query.rfind('/');
    std::string dir = (slash == std::string::npos) ? "/" : base.path_and_query.substr(0, slash + 1);
    return authority + dir + loc;
}

struct StoredCookie {
    std::string name;
    std::string value;
    std::string domain; // lowercased, without leading dot
    bool host_only = true;
    std::string path = "/";
    bool secure = false;
};

class CookieJar {
public:
    void set_storage_path(const std::wstring& path) {
        std::lock_guard<std::mutex> lock(m_mu);
        m_path = path;
        load_locked();
    }

    void clear() {
        std::lock_guard<std::mutex> lock(m_mu);
        m_cookies.clear();
        if (!m_path.empty()) {
            DeleteFileW(m_path.c_str());
        }
    }

    void store_from_header(const ParsedURL& req_url, const std::string& set_cookie) {
        std::lock_guard<std::mutex> lock(m_mu);
        size_t semi = set_cookie.find(';');
        std::string pair = trim_str(semi == std::string::npos ? set_cookie : set_cookie.substr(0, semi));
        size_t eq = pair.find('=');
        if (eq == std::string::npos || eq == 0) return;

        StoredCookie c;
        c.name = trim_str(pair.substr(0, eq));
        c.value = trim_str(pair.substr(eq + 1));
        c.domain = to_lower_ascii(req_url.host);
        c.host_only = true;
        c.path = "/";
        bool expired = false;

        if (semi != std::string::npos) {
            size_t pos = semi + 1;
            while (pos < set_cookie.size()) {
                size_t next = set_cookie.find(';', pos);
                std::string attr = trim_str(next == std::string::npos ? set_cookie.substr(pos) : set_cookie.substr(pos, next - pos));
                pos = (next == std::string::npos) ? set_cookie.size() : next + 1;
                size_t aeq = attr.find('=');
                std::string ak = to_lower_ascii(trim_str(aeq == std::string::npos ? attr : attr.substr(0, aeq)));
                std::string av = aeq == std::string::npos ? "" : trim_str(attr.substr(aeq + 1));
                if (ak == "domain" && !av.empty()) {
                    std::string dom = to_lower_ascii(av);
                    while (!dom.empty() && dom.front() == '.') dom.erase(dom.begin());
                    if (!dom.empty()) {
                        c.domain = dom;
                        c.host_only = false;
                    }
                } else if (ak == "path" && !av.empty()) {
                    c.path = av;
                } else if (ak == "secure") {
                    c.secure = true;
                } else if (ak == "max-age") {
                    if (std::strtoll(av.c_str(), nullptr, 10) <= 0) expired = true;
                }
            }
        }

        // Remove any existing cookie with the same (name, domain, path).
        m_cookies.erase(
            std::remove_if(m_cookies.begin(), m_cookies.end(), [&](const StoredCookie& existing) {
                return existing.name == c.name && existing.domain == c.domain && existing.path == c.path;
            }),
            m_cookies.end());

        if (!expired) {
            m_cookies.push_back(std::move(c));
        }
        save_locked();
    }

    std::string header_for(const ParsedURL& url) const {
        std::lock_guard<std::mutex> lock(m_mu);
        std::string host = to_lower_ascii(url.host);
        std::string req_path = url.path_and_query;
        size_t q = req_path.find('?');
        if (q != std::string::npos) req_path.resize(q);
        if (req_path.empty()) req_path = "/";

        std::string out;
        for (const StoredCookie& c : m_cookies) {
            if (c.secure && !url.is_https) continue;
            bool domain_ok = false;
            if (c.host_only) {
                domain_ok = (host == c.domain);
            } else {
                domain_ok = (host == c.domain) ||
                            (host.size() > c.domain.size() &&
                             host.compare(host.size() - c.domain.size(), c.domain.size(), c.domain) == 0 &&
                             host[host.size() - c.domain.size() - 1] == '.');
            }
            if (!domain_ok) continue;
            if (req_path.rfind(c.path, 0) != 0) continue;
            if (!out.empty()) out.append("; ");
            out.append(c.name);
            out.push_back('=');
            out.append(c.value);
        }
        return out;
    }

private:
    static constexpr const char* kEntropy = "quailcrate.music.cookies.v1";
    mutable std::mutex m_mu;
    std::wstring m_path;
    std::vector<StoredCookie> m_cookies;

    void load_locked() {
        m_cookies.clear();
        if (m_path.empty()) return;
        std::string encrypted;
        if (!read_file_all(m_path, encrypted, 2 << 20) || encrypted.empty()) return;

        DATA_BLOB in_blob{ static_cast<DWORD>(encrypted.size()), reinterpret_cast<BYTE*>(&encrypted[0]) };
        DATA_BLOB ent_blob{ static_cast<DWORD>(std::strlen(kEntropy)), const_cast<BYTE*>(reinterpret_cast<const BYTE*>(kEntropy)) };
        DATA_BLOB out_blob{};
        if (!CryptUnprotectData(&in_blob, nullptr, &ent_blob, nullptr, nullptr, CRYPTPROTECT_UI_FORBIDDEN, &out_blob)) {
            return;
        }
        std::string plain(reinterpret_cast<const char*>(out_blob.pbData), out_blob.cbData);
        LocalFree(out_blob.pbData);

        Json parsed;
        if (!Json::parse(plain, parsed) || !parsed.is_array()) return;
        for (const Json& item : parsed.as_array()) {
            if (!item.is_object()) continue;
            StoredCookie c;
            c.name = item.str("name");
            c.value = item.str("value");
            c.domain = item.str("domain");
            c.host_only = item.boolean("hostOnly", true);
            c.path = item.str("path", "/");
            c.secure = item.boolean("secure", false);
            if (!c.name.empty() && !c.domain.empty()) {
                m_cookies.push_back(std::move(c));
            }
        }
    }

    void save_locked() const {
        if (m_path.empty()) return;
        Json arr = Json::array();
        for (const StoredCookie& c : m_cookies) {
            Json o = Json::object();
            o["name"] = c.name;
            o["value"] = c.value;
            o["domain"] = c.domain;
            o["hostOnly"] = c.host_only;
            o["path"] = c.path;
            o["secure"] = c.secure;
            arr.push_back(std::move(o));
        }
        std::string plain = arr.dump();
        DATA_BLOB in_blob{ static_cast<DWORD>(plain.size()), reinterpret_cast<BYTE*>(&plain[0]) };
        DATA_BLOB ent_blob{ static_cast<DWORD>(std::strlen(kEntropy)), const_cast<BYTE*>(reinterpret_cast<const BYTE*>(kEntropy)) };
        DATA_BLOB out_blob{};
        if (!CryptProtectData(&in_blob, L"QuailCrateMusicCookies", &ent_blob, nullptr, nullptr, CRYPTPROTECT_UI_FORBIDDEN, &out_blob)) {
            return;
        }
        write_file_atomic(m_path, out_blob.pbData, out_blob.cbData);
        LocalFree(out_blob.pbData);
    }
};

struct HttpResponse {
    bool ok = false;
    int status = 0;
    std::string final_url;
    std::string body;
    std::string error_code;
};

class HttpClient {
public:
    HttpClient() {
        m_session = WinHttpOpen(
            utf8_to_wide(bili::kUserAgent).c_str(),
            WINHTTP_ACCESS_TYPE_AUTOMATIC_PROXY,
            WINHTTP_NO_PROXY_NAME,
            WINHTTP_NO_PROXY_BYPASS,
            0);
        if (m_session) {
            DWORD opt = WINHTTP_DECOMPRESSION_FLAG_ALL;
            WinHttpSetOption(m_session, WINHTTP_OPTION_DECOMPRESSION, &opt, sizeof(opt));
        }
    }

    ~HttpClient() {
        if (m_session) WinHttpCloseHandle(m_session);
    }

    CookieJar& cookies() { return m_cookies; }

    HttpResponse get(const std::string& url, const std::atomic<bool>* cancel = nullptr, int timeout_ms = 15000) {
        HttpResponse resp;
        if (!m_session) {
            resp.error_code = "network_error";
            return resp;
        }
        std::string current = url;
        for (int hop = 0; hop < 6; ++hop) {
            if (cancel && cancel->load()) {
                resp.error_code = "canceled";
                return resp;
            }
            ParsedURL parsed = parse_url(current);
            if (!parsed.valid) {
                resp.error_code = "invalid_url";
                return resp;
            }
            HINTERNET conn = WinHttpConnect(m_session, utf8_to_wide(parsed.host).c_str(), parsed.port, 0);
            if (!conn) {
                resp.error_code = "network_error";
                return resp;
            }
            DWORD flags = parsed.is_https ? WINHTTP_FLAG_SECURE : 0;
            HINTERNET req = WinHttpOpenRequest(conn, L"GET", utf8_to_wide(parsed.path_and_query).c_str(),
                                               nullptr, WINHTTP_NO_REFERER, WINHTTP_DEFAULT_ACCEPT_TYPES, flags);
            if (!req) {
                WinHttpCloseHandle(conn);
                resp.error_code = "network_error";
                return resp;
            }
            DWORD redirect_policy = WINHTTP_OPTION_REDIRECT_POLICY_NEVER;
            WinHttpSetOption(req, WINHTTP_OPTION_REDIRECT_POLICY, &redirect_policy, sizeof(redirect_policy));
            DWORD disable_cookies = WINHTTP_DISABLE_COOKIES;
            WinHttpSetOption(req, WINHTTP_OPTION_DISABLE_FEATURE, &disable_cookies, sizeof(disable_cookies));
            WinHttpSetTimeouts(req, timeout_ms, timeout_ms, timeout_ms, timeout_ms);

            std::string hdrs = std::string("Referer: ") + bili::kReferer + "\r\n";
            std::string cookie = m_cookies.header_for(parsed);
            if (!cookie.empty()) {
                hdrs += "Cookie: " + cookie + "\r\n";
            }
            std::wstring whdrs = utf8_to_wide(hdrs);
            WinHttpAddRequestHeaders(req, whdrs.c_str(), static_cast<DWORD>(whdrs.size()),
                                     WINHTTP_ADDREQ_FLAG_ADD | WINHTTP_ADDREQ_FLAG_REPLACE);

            bool sent = WinHttpSendRequest(req, WINHTTP_NO_ADDITIONAL_HEADERS, 0,
                                           WINHTTP_NO_REQUEST_DATA, 0, 0, 0) &&
                        WinHttpReceiveResponse(req, nullptr);
            if (!sent) {
                DWORD err = GetLastError();
                WinHttpCloseHandle(req);
                WinHttpCloseHandle(conn);
                resp.error_code = (err == ERROR_WINHTTP_TIMEOUT) ? "timeout" : "network_error";
                return resp;
            }

            DWORD status = 0;
            DWORD status_sz = sizeof(status);
            WinHttpQueryHeaders(req, WINHTTP_QUERY_STATUS_CODE | WINHTTP_QUERY_FLAG_NUMBER,
                                WINHTTP_HEADER_NAME_BY_INDEX, &status, &status_sz, WINHTTP_NO_HEADER_INDEX);
            capture_cookies(req, parsed);

            if (status == 301 || status == 302 || status == 303 || status == 307 || status == 308) {
                std::string loc = query_header_utf8(req, WINHTTP_QUERY_LOCATION);
                WinHttpCloseHandle(req);
                WinHttpCloseHandle(conn);
                std::string next = resolve_redirect_url(parsed, loc);
                if (next.empty()) {
                    resp.status = static_cast<int>(status);
                    resp.final_url = current;
                    resp.ok = true;
                    return resp;
                }
                current = next;
                continue;
            }

            std::string body;
            char buf[16384];
            for (;;) {
                if (cancel && cancel->load()) {
                    WinHttpCloseHandle(req);
                    WinHttpCloseHandle(conn);
                    resp.error_code = "canceled";
                    return resp;
                }
                DWORD read = 0;
                if (!WinHttpReadData(req, buf, sizeof(buf), &read)) {
                    WinHttpCloseHandle(req);
                    WinHttpCloseHandle(conn);
                    resp.error_code = "network_error";
                    return resp;
                }
                if (read == 0) break;
                if (body.size() + read > (8u << 20)) {
                    WinHttpCloseHandle(req);
                    WinHttpCloseHandle(conn);
                    resp.error_code = "response_too_large";
                    return resp;
                }
                body.append(buf, read);
            }
            WinHttpCloseHandle(req);
            WinHttpCloseHandle(conn);
            resp.ok = true;
            resp.status = static_cast<int>(status);
            resp.final_url = current;
            resp.body = std::move(body);
            return resp;
        }
        resp.error_code = "too_many_redirects";
        return resp;
    }

    // Stream binary media to a callback. Returns true when EOF reached cleanly with HTTP 2xx.
    // The callback returns false to abort transfer (e.g. disk full or canceled).
    bool stream_get(const std::string& url,
                    const std::atomic<bool>& cancel,
                    const std::function<bool(const uint8_t*, size_t, int64_t)>& on_chunk,
                    std::string& out_error,
                    int timeout_ms = 20000) {
        if (!m_session) {
            out_error = "network_error";
            return false;
        }
        std::string current = url;
        for (int hop = 0; hop < 6; ++hop) {
            if (cancel.load()) {
                out_error = "canceled";
                return false;
            }
            ParsedURL parsed = parse_url(current);
            if (!parsed.valid) {
                out_error = "invalid_url";
                return false;
            }
            HINTERNET conn = WinHttpConnect(m_session, utf8_to_wide(parsed.host).c_str(), parsed.port, 0);
            if (!conn) {
                out_error = "network_error";
                return false;
            }
            DWORD flags = parsed.is_https ? WINHTTP_FLAG_SECURE : 0;
            HINTERNET req = WinHttpOpenRequest(conn, L"GET", utf8_to_wide(parsed.path_and_query).c_str(),
                                               nullptr, WINHTTP_NO_REFERER, WINHTTP_DEFAULT_ACCEPT_TYPES, flags);
            if (!req) {
                WinHttpCloseHandle(conn);
                out_error = "network_error";
                return false;
            }
            DWORD redirect_policy = WINHTTP_OPTION_REDIRECT_POLICY_NEVER;
            WinHttpSetOption(req, WINHTTP_OPTION_REDIRECT_POLICY, &redirect_policy, sizeof(redirect_policy));
            DWORD disable_cookies = WINHTTP_DISABLE_COOKIES;
            WinHttpSetOption(req, WINHTTP_OPTION_DISABLE_FEATURE, &disable_cookies, sizeof(disable_cookies));
            WinHttpSetTimeouts(req, timeout_ms, timeout_ms, timeout_ms, timeout_ms);

            std::string hdrs = std::string("Referer: ") + bili::kReferer + "\r\n";
            std::string cookie = m_cookies.header_for(parsed);
            if (!cookie.empty()) {
                hdrs += "Cookie: " + cookie + "\r\n";
            }
            std::wstring whdrs = utf8_to_wide(hdrs);
            WinHttpAddRequestHeaders(req, whdrs.c_str(), static_cast<DWORD>(whdrs.size()),
                                     WINHTTP_ADDREQ_FLAG_ADD | WINHTTP_ADDREQ_FLAG_REPLACE);

            if (!WinHttpSendRequest(req, WINHTTP_NO_ADDITIONAL_HEADERS, 0, WINHTTP_NO_REQUEST_DATA, 0, 0, 0) ||
                !WinHttpReceiveResponse(req, nullptr)) {
                DWORD err = GetLastError();
                WinHttpCloseHandle(req);
                WinHttpCloseHandle(conn);
                out_error = (err == ERROR_WINHTTP_TIMEOUT) ? "timeout" : "network_error";
                return false;
            }

            DWORD status = 0;
            DWORD status_sz = sizeof(status);
            WinHttpQueryHeaders(req, WINHTTP_QUERY_STATUS_CODE | WINHTTP_QUERY_FLAG_NUMBER,
                                WINHTTP_HEADER_NAME_BY_INDEX, &status, &status_sz, WINHTTP_NO_HEADER_INDEX);
            capture_cookies(req, parsed);

            if (status == 301 || status == 302 || status == 303 || status == 307 || status == 308) {
                std::string loc = query_header_utf8(req, WINHTTP_QUERY_LOCATION);
                WinHttpCloseHandle(req);
                WinHttpCloseHandle(conn);
                std::string next = resolve_redirect_url(parsed, loc);
                if (next.empty()) {
                    out_error = "network_error";
                    return false;
                }
                current = next;
                continue;
            }

            if (status < 200 || status >= 300) {
                WinHttpCloseHandle(req);
                WinHttpCloseHandle(conn);
                out_error = "http_" + std::to_string(status);
                return false;
            }

            int64_t total_len = -1;
            std::string cl = query_header_utf8(req, WINHTTP_QUERY_CONTENT_LENGTH);
            if (!cl.empty()) {
                total_len = std::strtoll(cl.c_str(), nullptr, 10);
            }

            uint8_t buf[32768];
            for (;;) {
                if (cancel.load()) {
                    WinHttpCloseHandle(req);
                    WinHttpCloseHandle(conn);
                    out_error = "canceled";
                    return false;
                }
                DWORD avail = 0;
                if (!WinHttpQueryDataAvailable(req, &avail)) {
                    WinHttpCloseHandle(req);
                    WinHttpCloseHandle(conn);
                    out_error = cancel.load() ? "canceled" : "network_error";
                    return false;
                }
                if (avail == 0) break;
                DWORD to_read = std::min<DWORD>(avail, static_cast<DWORD>(sizeof(buf)));
                DWORD read = 0;
                if (!WinHttpReadData(req, buf, to_read, &read)) {
                    WinHttpCloseHandle(req);
                    WinHttpCloseHandle(conn);
                    out_error = cancel.load() ? "canceled" : "network_error";
                    return false;
                }
                if (read == 0) break;
                if (!on_chunk(buf, read, total_len)) {
                    WinHttpCloseHandle(req);
                    WinHttpCloseHandle(conn);
                    if (out_error.empty()) out_error = cancel.load() ? "canceled" : "storage_unavailable";
                    return false;
                }
            }
            WinHttpCloseHandle(req);
            WinHttpCloseHandle(conn);
            return true;
        }
        out_error = "too_many_redirects";
        return false;
    }

private:
    HINTERNET m_session = nullptr;
    CookieJar m_cookies;

    static std::string query_header_utf8(HINTERNET req, DWORD query_flag) {
        DWORD sz = 0;
        WinHttpQueryHeaders(req, query_flag, WINHTTP_HEADER_NAME_BY_INDEX, nullptr, &sz, WINHTTP_NO_HEADER_INDEX);
        if (sz == 0) return "";
        std::wstring buf(sz / sizeof(wchar_t), L'\0');
        if (!WinHttpQueryHeaders(req, query_flag, WINHTTP_HEADER_NAME_BY_INDEX, &buf[0], &sz, WINHTTP_NO_HEADER_INDEX)) {
            return "";
        }
        while (!buf.empty() && buf.back() == L'\0') buf.pop_back();
        return wide_to_utf8(buf);
    }

    void capture_cookies(HINTERNET req, const ParsedURL& url) {
        DWORD index = 0;
        for (;;) {
            DWORD sz = 0;
            WinHttpQueryHeaders(req, WINHTTP_QUERY_SET_COOKIE, WINHTTP_HEADER_NAME_BY_INDEX, nullptr, &sz, &index);
            if (GetLastError() != ERROR_INSUFFICIENT_BUFFER || sz == 0) break;
            std::wstring buf(sz / sizeof(wchar_t), L'\0');
            DWORD next_idx = index;
            if (!WinHttpQueryHeaders(req, WINHTTP_QUERY_SET_COOKIE, WINHTTP_HEADER_NAME_BY_INDEX, &buf[0], &sz, &next_idx)) {
                break;
            }
            index = next_idx;
            while (!buf.empty() && buf.back() == L'\0') buf.pop_back();
            m_cookies.store_from_header(url, wide_to_utf8(buf));
        }
    }
};

} // namespace music
