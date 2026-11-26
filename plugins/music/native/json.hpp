#pragma once

#include <cmath>
#include <cstdint>
#include <cstdio>
#include <cstdlib>
#include <map>
#include <sstream>
#include <string>
#include <utility>
#include <vector>

namespace music {

class Json {
public:
    enum class Type { Null, Bool, Number, String, Array, Object };

    using Array = std::vector<Json>;
    using Object = std::map<std::string, Json>;

    Json() : m_type(Type::Null) {}
    Json(std::nullptr_t) : m_type(Type::Null) {}
    Json(bool b) : m_type(Type::Bool), m_bool(b) {}
    Json(int v) : m_type(Type::Number), m_num(static_cast<double>(v)), m_int(v), m_is_int(true) {}
    Json(int64_t v) : m_type(Type::Number), m_num(static_cast<double>(v)), m_int(v), m_is_int(true) {}
    Json(uint64_t v) : m_type(Type::Number), m_num(static_cast<double>(v)), m_int(static_cast<int64_t>(v)), m_is_int(true) {}
    Json(double v) : m_type(Type::Number), m_num(v), m_int(static_cast<int64_t>(v)), m_is_int(false) {}
    Json(const char* s) : m_type(Type::String), m_str(s ? s : "") {}
    Json(std::string s) : m_type(Type::String), m_str(std::move(s)) {}
    Json(Array a) : m_type(Type::Array), m_arr(std::move(a)) {}
    Json(Object o) : m_type(Type::Object), m_obj(std::move(o)) {}

    static Json array() { return Json(Array{}); }
    static Json object() { return Json(Object{}); }

    Type type() const { return m_type; }
    bool is_null() const { return m_type == Type::Null; }
    bool is_bool() const { return m_type == Type::Bool; }
    bool is_number() const { return m_type == Type::Number; }
    bool is_string() const { return m_type == Type::String; }
    bool is_array() const { return m_type == Type::Array; }
    bool is_object() const { return m_type == Type::Object; }

    bool as_bool(bool fallback = false) const {
        return m_type == Type::Bool ? m_bool : fallback;
    }
    double as_num(double fallback = 0.0) const {
        if (m_type == Type::Number) return m_num;
        if (m_type == Type::String && !m_str.empty()) {
            char* end = nullptr;
            double v = std::strtod(m_str.c_str(), &end);
            if (end && *end == '\0') return v;
        }
        return fallback;
    }
    int64_t as_i64(int64_t fallback = 0) const {
        if (m_type == Type::Number) return m_is_int ? m_int : static_cast<int64_t>(m_num);
        if (m_type == Type::String && !m_str.empty()) {
            char* end = nullptr;
            long long v = std::strtoll(m_str.c_str(), &end, 10);
            if (end && *end == '\0') return static_cast<int64_t>(v);
        }
        return fallback;
    }
    const std::string& as_str() const {
        static const std::string empty;
        return m_type == Type::String ? m_str : empty;
    }
    std::string to_str(const std::string& fallback = "") const {
        if (m_type == Type::String) return m_str;
        if (m_type == Type::Number) {
            if (m_is_int) return std::to_string(m_int);
            char buf[64];
            std::snprintf(buf, sizeof(buf), "%.15g", m_num);
            return buf;
        }
        if (m_type == Type::Bool) return m_bool ? "true" : "false";
        return fallback;
    }
    const Array& as_array() const {
        static const Array empty;
        return m_type == Type::Array ? m_arr : empty;
    }
    Array& as_array_mut() {
        if (m_type != Type::Array) {
            m_type = Type::Array;
            m_arr.clear();
        }
        return m_arr;
    }
    const Object& as_object() const {
        static const Object empty;
        return m_type == Type::Object ? m_obj : empty;
    }
    Object& as_object_mut() {
        if (m_type != Type::Object) {
            m_type = Type::Object;
            m_obj.clear();
        }
        return m_obj;
    }

    bool has(const std::string& key) const {
        return m_type == Type::Object && m_obj.find(key) != m_obj.end();
    }
    const Json& get(const std::string& key) const {
        static const Json null_val;
        if (m_type != Type::Object) return null_val;
        auto it = m_obj.find(key);
        return it == m_obj.end() ? null_val : it->second;
    }
    Json& operator[](const std::string& key) {
        return as_object_mut()[key];
    }
    void push_back(Json v) {
        as_array_mut().push_back(std::move(v));
    }

    std::string str(const std::string& key, const std::string& fallback = "") const {
        const Json& v = get(key);
        return v.is_string() ? v.as_str() : fallback;
    }
    int64_t i64(const std::string& key, int64_t fallback = 0) const {
        const Json& v = get(key);
        return v.is_null() ? fallback : v.as_i64(fallback);
    }
    double num(const std::string& key, double fallback = 0.0) const {
        const Json& v = get(key);
        return v.is_null() ? fallback : v.as_num(fallback);
    }
    bool boolean(const std::string& key, bool fallback = false) const {
        const Json& v = get(key);
        return v.is_bool() ? v.as_bool() : fallback;
    }

    std::string dump() const {
        std::string out;
        out.reserve(256);
        write_to(out);
        return out;
    }

    static bool parse(const char* data, size_t len, Json& out) {
        Parser p(data, len);
        p.skip_ws();
        if (!p.parse_value(out, 0)) return false;
        p.skip_ws();
        return p.pos == p.len;
    }
    static bool parse(const std::string& s, Json& out) {
        return parse(s.data(), s.size(), out);
    }

private:
    Type m_type = Type::Null;
    bool m_bool = false;
    double m_num = 0.0;
    int64_t m_int = 0;
    bool m_is_int = false;
    std::string m_str;
    Array m_arr;
    Object m_obj;

    static void append_escaped(std::string& out, const std::string& s) {
        out.push_back('"');
        for (unsigned char c : s) {
            switch (c) {
            case '"': out.append("\\\""); break;
            case '\\': out.append("\\\\"); break;
            case '\b': out.append("\\b"); break;
            case '\f': out.append("\\f"); break;
            case '\n': out.append("\\n"); break;
            case '\r': out.append("\\r"); break;
            case '\t': out.append("\\t"); break;
            default:
                if (c < 0x20) {
                    char buf[8];
                    std::snprintf(buf, sizeof(buf), "\\u%04x", static_cast<unsigned int>(c));
                    out.append(buf);
                } else {
                    out.push_back(static_cast<char>(c));
                }
                break;
            }
        }
        out.push_back('"');
    }

    void write_to(std::string& out) const {
        switch (m_type) {
        case Type::Null:
            out.append("null");
            break;
        case Type::Bool:
            out.append(m_bool ? "true" : "false");
            break;
        case Type::Number:
            if (m_is_int) {
                out.append(std::to_string(m_int));
            } else if (!std::isfinite(m_num)) {
                out.append("0");
            } else {
                char buf[64];
                std::snprintf(buf, sizeof(buf), "%.15g", m_num);
                out.append(buf);
            }
            break;
        case Type::String:
            append_escaped(out, m_str);
            break;
        case Type::Array:
            out.push_back('[');
            for (size_t i = 0; i < m_arr.size(); ++i) {
                if (i > 0) out.push_back(',');
                m_arr[i].write_to(out);
            }
            out.push_back(']');
            break;
        case Type::Object:
            out.push_back('{');
            bool first = true;
            for (const auto& kv : m_obj) {
                if (!first) out.push_back(',');
                first = false;
                append_escaped(out, kv.first);
                out.push_back(':');
                kv.second.write_to(out);
            }
            out.push_back('}');
            break;
        }
    }

    struct Parser {
        const char* data;
        size_t len;
        size_t pos = 0;

        Parser(const char* d, size_t l) : data(d), len(l) {}

        void skip_ws() {
            while (pos < len) {
                char c = data[pos];
                if (c == ' ' || c == '\t' || c == '\r' || c == '\n') {
                    ++pos;
                } else {
                    break;
                }
            }
        }

        static void append_utf8(std::string& out, uint32_t cp) {
            if (cp <= 0x7F) {
                out.push_back(static_cast<char>(cp));
            } else if (cp <= 0x7FF) {
                out.push_back(static_cast<char>(0xC0 | ((cp >> 6) & 0x1F)));
                out.push_back(static_cast<char>(0x80 | (cp & 0x3F)));
            } else if (cp <= 0xFFFF) {
                out.push_back(static_cast<char>(0xE0 | ((cp >> 12) & 0x0F)));
                out.push_back(static_cast<char>(0x80 | ((cp >> 6) & 0x3F)));
                out.push_back(static_cast<char>(0x80 | (cp & 0x3F)));
            } else if (cp <= 0x10FFFF) {
                out.push_back(static_cast<char>(0xF0 | ((cp >> 18) & 0x07)));
                out.push_back(static_cast<char>(0x80 | ((cp >> 12) & 0x3F)));
                out.push_back(static_cast<char>(0x80 | ((cp >> 6) & 0x3F)));
                out.push_back(static_cast<char>(0x80 | (cp & 0x3F)));
            }
        }

        bool parse_hex4(uint32_t& out) {
            if (pos + 4 > len) return false;
            uint32_t val = 0;
            for (int i = 0; i < 4; ++i) {
                char c = data[pos++];
                val <<= 4;
                if (c >= '0' && c <= '9') val |= static_cast<uint32_t>(c - '0');
                else if (c >= 'a' && c <= 'f') val |= static_cast<uint32_t>(10 + c - 'a');
                else if (c >= 'A' && c <= 'F') val |= static_cast<uint32_t>(10 + c - 'A');
                else return false;
            }
            out = val;
            return true;
        }

        bool parse_string(std::string& out) {
            if (pos >= len || data[pos] != '"') return false;
            ++pos;
            out.clear();
            while (pos < len) {
                unsigned char c = static_cast<unsigned char>(data[pos++]);
                if (c == '"') return true;
                if (c < 0x20) return false;
                if (c == '\\') {
                    if (pos >= len) return false;
                    char esc = data[pos++];
                    switch (esc) {
                    case '"': out.push_back('"'); break;
                    case '\\': out.push_back('\\'); break;
                    case '/': out.push_back('/'); break;
                    case 'b': out.push_back('\b'); break;
                    case 'f': out.push_back('\f'); break;
                    case 'n': out.push_back('\n'); break;
                    case 'r': out.push_back('\r'); break;
                    case 't': out.push_back('\t'); break;
                    case 'u': {
                        uint32_t cp = 0;
                        if (!parse_hex4(cp)) return false;
                        if (cp >= 0xD800 && cp <= 0xDBFF) {
                            if (pos + 2 <= len && data[pos] == '\\' && data[pos + 1] == 'u') {
                                pos += 2;
                                uint32_t low = 0;
                                if (!parse_hex4(low)) return false;
                                if (low >= 0xDC00 && low <= 0xDFFF) {
                                    cp = 0x10000 + (((cp - 0xD800) << 10) | (low - 0xDC00));
                                }
                            }
                        }
                        append_utf8(out, cp);
                        break;
                    }
                    default:
                        return false;
                    }
                } else {
                    out.push_back(static_cast<char>(c));
                }
            }
            return false;
        }

        bool parse_number(Json& out) {
            size_t start = pos;
            bool is_float = false;
            if (pos < len && data[pos] == '-') ++pos;
            if (pos >= len) return false;
            if (data[pos] == '0') {
                ++pos;
            } else if (data[pos] >= '1' && data[pos] <= '9') {
                while (pos < len && data[pos] >= '0' && data[pos] <= '9') ++pos;
            } else {
                return false;
            }
            if (pos < len && data[pos] == '.') {
                is_float = true;
                ++pos;
                if (pos >= len || data[pos] < '0' || data[pos] > '9') return false;
                while (pos < len && data[pos] >= '0' && data[pos] <= '9') ++pos;
            }
            if (pos < len && (data[pos] == 'e' || data[pos] == 'E')) {
                is_float = true;
                ++pos;
                if (pos < len && (data[pos] == '+' || data[pos] == '-')) ++pos;
                if (pos >= len || data[pos] < '0' || data[pos] > '9') return false;
                while (pos < len && data[pos] >= '0' && data[pos] <= '9') ++pos;
            }
            std::string token(data + start, pos - start);
            if (!is_float) {
                char* end = nullptr;
                errno = 0;
                long long v = std::strtoll(token.c_str(), &end, 10);
                if (errno == 0 && end && *end == '\0') {
                    out = Json(static_cast<int64_t>(v));
                    return true;
                }
            }
            char* end = nullptr;
            double d = std::strtod(token.c_str(), &end);
            if (!end || *end != '\0') return false;
            out = Json(d);
            return true;
        }

        bool parse_value(Json& out, int depth) {
            if (depth > 64) return false;
            skip_ws();
            if (pos >= len) return false;
            char c = data[pos];
            if (c == 'n') {
                if (pos + 4 <= len && std::memcmp(data + pos, "null", 4) == 0) {
                    pos += 4;
                    out = Json(nullptr);
                    return true;
                }
                return false;
            }
            if (c == 't') {
                if (pos + 4 <= len && std::memcmp(data + pos, "true", 4) == 0) {
                    pos += 4;
                    out = Json(true);
                    return true;
                }
                return false;
            }
            if (c == 'f') {
                if (pos + 5 <= len && std::memcmp(data + pos, "false", 5) == 0) {
                    pos += 5;
                    out = Json(false);
                    return true;
                }
                return false;
            }
            if (c == '"') {
                std::string s;
                if (!parse_string(s)) return false;
                out = Json(std::move(s));
                return true;
            }
            if (c == '[') {
                ++pos;
                Array arr;
                skip_ws();
                if (pos < len && data[pos] == ']') {
                    ++pos;
                    out = Json(std::move(arr));
                    return true;
                }
                while (pos < len) {
                    Json item;
                    if (!parse_value(item, depth + 1)) return false;
                    arr.push_back(std::move(item));
                    skip_ws();
                    if (pos < len && data[pos] == ',') {
                        ++pos;
                        continue;
                    }
                    if (pos < len && data[pos] == ']') {
                        ++pos;
                        out = Json(std::move(arr));
                        return true;
                    }
                    return false;
                }
                return false;
            }
            if (c == '{') {
                ++pos;
                Object obj;
                skip_ws();
                if (pos < len && data[pos] == '}') {
                    ++pos;
                    out = Json(std::move(obj));
                    return true;
                }
                while (pos < len) {
                    skip_ws();
                    std::string key;
                    if (!parse_string(key)) return false;
                    skip_ws();
                    if (pos >= len || data[pos] != ':') return false;
                    ++pos;
                    Json val;
                    if (!parse_value(val, depth + 1)) return false;
                    obj[std::move(key)] = std::move(val);
                    skip_ws();
                    if (pos < len && data[pos] == ',') {
                        ++pos;
                        continue;
                    }
                    if (pos < len && data[pos] == '}') {
                        ++pos;
                        out = Json(std::move(obj));
                        return true;
                    }
                    return false;
                }
                return false;
            }
            if (c == '-' || (c >= '0' && c <= '9')) {
                return parse_number(out);
            }
            return false;
        }
    };
};

} // namespace music
