#pragma once

#include "json.hpp"

#include <algorithm>
#include <cctype>
#include <cstdint>
#include <cstdio>
#include <cstring>
#include <map>
#include <set>
#include <string>
#include <utility>
#include <vector>

namespace music {

inline std::string trim_str(const std::string& s) {
    size_t start = 0;
    while (start < s.size() && std::isspace(static_cast<unsigned char>(s[start]))) ++start;
    size_t end = s.size();
    while (end > start && std::isspace(static_cast<unsigned char>(s[end - 1]))) --end;
    return s.substr(start, end - start);
}

inline std::string md5_hex(const std::string& input) {
    static const uint32_t k[64] = {
        0xd76aa478, 0xe8c7b756, 0x242070db, 0xc1bdceee, 0xf57c0faf, 0x4787c62a, 0xa8304613, 0xfd469501,
        0x698098d8, 0x8b44f7af, 0xffff5bb1, 0x895cd7be, 0x6b901122, 0xfd987193, 0xa679438e, 0x49b40821,
        0xf61e2562, 0xc040b340, 0x265e5a51, 0xe9b6c7aa, 0xd62f105d, 0x02441453, 0xd8a1e681, 0xe7d3fbc8,
        0x21e1cde6, 0xc33707d6, 0xf4d50d87, 0x455a14ed, 0xa9e3e905, 0xfcefa3f8, 0x676f02d9, 0x8d2a4c8a,
        0xfffa3942, 0x8771f681, 0x6d9d6122, 0xfde5380c, 0xa4beea44, 0x4bdecfa9, 0xf6bb4b60, 0xbebfbc70,
        0x289b7ec6, 0xeaa127fa, 0xd4ef3085, 0x04881d05, 0xd9d4d039, 0xe6db99e5, 0x1fa27cf8, 0xc4ac5665,
        0xf4292244, 0x432aff97, 0xab9423a7, 0xfc93a039, 0x655b59c3, 0x8f0ccc92, 0xffeff47d, 0x85845dd1,
        0x6fa87e4f, 0xfe2ce6e0, 0xa3014314, 0x4e0811a1, 0xf7537e82, 0xbd3af235, 0x2ad7d2bb, 0xeb86d391,
    };
    static const uint32_t r[64] = {
        7, 12, 17, 22, 7, 12, 17, 22, 7, 12, 17, 22, 7, 12, 17, 22,
        5,  9, 14, 20, 5,  9, 14, 20, 5,  9, 14, 20, 5,  9, 14, 20,
        4, 11, 16, 23, 4, 11, 16, 23, 4, 11, 16, 23, 4, 11, 16, 23,
        6, 10, 15, 21, 6, 10, 15, 21, 6, 10, 15, 21, 6, 10, 15, 21,
    };
    auto rotl = [](uint32_t x, uint32_t c) -> uint32_t { return (x << c) | (x >> (32 - c)); };

    std::vector<uint8_t> msg(input.begin(), input.end());
    uint64_t bit_len = static_cast<uint64_t>(msg.size()) * 8ULL;
    msg.push_back(0x80);
    while ((msg.size() % 64) != 56) msg.push_back(0);
    for (int i = 0; i < 8; ++i) {
        msg.push_back(static_cast<uint8_t>((bit_len >> (8 * i)) & 0xFF));
    }

    uint32_t h0 = 0x67452301;
    uint32_t h1 = 0xefcdab89;
    uint32_t h2 = 0x98badcfe;
    uint32_t h3 = 0x10325476;

    for (size_t offset = 0; offset < msg.size(); offset += 64) {
        uint32_t w[16];
        for (int i = 0; i < 16; ++i) {
            size_t p = offset + static_cast<size_t>(i) * 4;
            w[i] = static_cast<uint32_t>(msg[p]) |
                   (static_cast<uint32_t>(msg[p + 1]) << 8) |
                   (static_cast<uint32_t>(msg[p + 2]) << 16) |
                   (static_cast<uint32_t>(msg[p + 3]) << 24);
        }
        uint32_t a = h0, b = h1, c = h2, d = h3;
        for (uint32_t i = 0; i < 64; ++i) {
            uint32_t f = 0, g = 0;
            if (i < 16) {
                f = (b & c) | ((~b) & d);
                g = i;
            } else if (i < 32) {
                f = (d & b) | ((~d) & c);
                g = (5 * i + 1) % 16;
            } else if (i < 48) {
                f = b ^ c ^ d;
                g = (3 * i + 5) % 16;
            } else {
                f = c ^ (b | (~d));
                g = (7 * i) % 16;
            }
            uint32_t temp = d;
            d = c;
            c = b;
            b = b + rotl(a + f + k[i] + w[g], r[i]);
            a = temp;
        }
        h0 += a;
        h1 += b;
        h2 += c;
        h3 += d;
    }

    uint32_t digest[4] = { h0, h1, h2, h3 };
    char out[33];
    for (int i = 0; i < 4; ++i) {
        std::snprintf(out + i * 8, 9, "%02x%02x%02x%02x",
            digest[i] & 0xFF, (digest[i] >> 8) & 0xFF,
            (digest[i] >> 16) & 0xFF, (digest[i] >> 24) & 0xFF);
    }
    return std::string(out, 32);
}

inline std::string url_encode(const std::string& str) {
    std::string out;
    out.reserve(str.size() * 2);
    for (unsigned char c : str) {
        if ((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
            c == '-' || c == '_' || c == '.' || c == '~') {
            out.push_back(static_cast<char>(c));
        } else {
            char buf[4];
            std::snprintf(buf, sizeof(buf), "%%%02X", static_cast<unsigned int>(c));
            out.append(buf);
        }
    }
    return out;
}

namespace bili {

static constexpr const char* kUserAgent =
    "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36";
static constexpr const char* kReferer = "https://www.bilibili.com/";

static constexpr uint64_t kXorCode = 23442827791579ULL;
static constexpr uint64_t kBit51 = 2251799813685248ULL; // 2^51
static constexpr uint64_t kBase = 58ULL;
static constexpr const char* kAlphabet = "FcwAPNKTMug3GV5Lj7EJnHpWsx4tb8haYeviqBz6rkCy12mUSDQX9RdoZf";

inline int alphabet_index(char c) {
    const char* p = std::strchr(kAlphabet, c);
    return p ? static_cast<int>(p - kAlphabet) : -1;
}

inline bool is_alnum_ascii(char c) {
    return (c >= '0' && c <= '9') || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z');
}

inline std::string strip_bv(const std::string& input) {
    for (size_t i = 0; i + 12 <= input.size(); ++i) {
        if ((input[i] == 'B' || input[i] == 'b') &&
            (input[i + 1] == 'V' || input[i + 1] == 'v') &&
            input[i + 2] == '1') {
            bool ok = true;
            for (size_t j = 0; j < 9; ++j) {
                if (!is_alnum_ascii(input[i + 3 + j])) {
                    ok = false;
                    break;
                }
            }
            if (ok) return input.substr(i + 3, 9);
        }
    }
    if (input.size() == 9) {
        bool ok = true;
        for (char c : input) {
            if (!is_alnum_ascii(c)) {
                ok = false;
                break;
            }
        }
        if (ok) return input;
    }
    return "";
}

inline bool decode_bv(const std::string& input, int64_t& out_aid) {
    std::string suffix = strip_bv(input);
    if (suffix.size() != 9) return false;
    std::swap(suffix[0], suffix[6]);
    std::swap(suffix[1], suffix[4]);
    uint64_t avid = 0;
    for (char c : suffix) {
        int idx = alphabet_index(c);
        if (idx < 0) return false;
        avid = avid * kBase + static_cast<uint64_t>(idx);
    }
    uint64_t masked = avid % kBit51;
    out_aid = static_cast<int64_t>(masked ^ kXorCode);
    return true;
}

inline std::string encode_bv(int64_t avid) {
    if (avid < 1 || static_cast<uint64_t>(avid) >= kBit51) return "";
    uint64_t tmp = (kBit51 + static_cast<uint64_t>(avid)) ^ kXorCode;
    char buf[10];
    buf[9] = '\0';
    for (int i = 8; i >= 0; --i) {
        buf[i] = kAlphabet[tmp % kBase];
        tmp /= kBase;
    }
    std::swap(buf[0], buf[6]);
    std::swap(buf[1], buf[4]);
    return std::string("BV1") + buf;
}

inline std::string extract_wbi_filename(const std::string& url) {
    size_t slash = url.rfind('/');
    std::string file = (slash == std::string::npos) ? url : url.substr(slash + 1);
    size_t dot = file.rfind('.');
    return (dot == std::string::npos) ? file : file.substr(0, dot);
}

inline std::string wbi_key(const std::string& img_url, const std::string& sub_url) {
    static const int kMixin[32] = {
        46, 47, 18, 2, 53, 8, 23, 32, 15, 50, 10, 31, 58, 3, 45, 35,
        27, 43, 5, 49, 33, 9, 42, 19, 29, 28, 14, 39, 12, 38, 41, 13,
    };
    std::string orig = extract_wbi_filename(img_url) + extract_wbi_filename(sub_url);
    if (orig.size() < 59) return "";
    std::string out;
    out.reserve(32);
    for (int idx : kMixin) {
        out.push_back(orig[static_cast<size_t>(idx)]);
    }
    return out;
}

inline std::string sanitize_wbi_value(const std::string& v) {
    std::string out;
    out.reserve(v.size());
    for (char c : v) {
        if (c != '!' && c != '\'' && c != '(' && c != ')' && c != '*') {
            out.push_back(c);
        }
    }
    return out;
}

inline bool wbi_sign(const std::map<std::string, std::string>& params,
                     const std::string& key, int64_t unix_time,
                     std::string& out_qs, std::string& out_wrid) {
    if (key.empty()) return false;
    std::map<std::string, std::string> merged;
    for (const auto& kv : params) {
        merged[kv.first] = sanitize_wbi_value(kv.second);
    }
    merged["wts"] = std::to_string(unix_time);

    std::string qs;
    bool first = true;
    for (const auto& kv : merged) {
        if (!first) qs.push_back('&');
        first = false;
        qs.append(url_encode(kv.first));
        qs.push_back('=');
        qs.append(url_encode(kv.second));
    }
    out_wrid = md5_hex(qs + key);
    out_qs = qs + "&w_rid=" + out_wrid;
    return true;
}

struct ParsedTarget {
    std::string kind; // "video", "favlist", "shorturl", "invalid"
    std::string bvid;
    int64_t aid = 0;
    int page = 0;
    std::string media_id;
    std::string url;
    std::string raw;
    std::string reason;
};

inline std::string match_digits_after(const std::string& s, const std::string& prefix, bool case_insensitive = false) {
    for (size_t i = 0; i + prefix.size() < s.size(); ++i) {
        bool match = true;
        for (size_t j = 0; j < prefix.size(); ++j) {
            char a = s[i + j];
            char b = prefix[j];
            if (case_insensitive) {
                a = static_cast<char>(std::tolower(static_cast<unsigned char>(a)));
                b = static_cast<char>(std::tolower(static_cast<unsigned char>(b)));
            }
            if (a != b) {
                match = false;
                break;
            }
        }
        if (match && std::isdigit(static_cast<unsigned char>(s[i + prefix.size()]))) {
            size_t p = i + prefix.size();
            size_t start = p;
            while (p < s.size() && std::isdigit(static_cast<unsigned char>(s[p]))) ++p;
            return s.substr(start, p - start);
        }
    }
    return "";
}

inline int extract_page_param(const std::string& raw) {
    for (const char* prefix : { "?p=", "&p=", "#p=" }) {
        std::string digits = match_digits_after(raw, prefix, false);
        if (!digits.empty()) {
            return std::atoi(digits.c_str());
        }
    }
    return 0;
}

inline ParsedTarget parse_token(const std::string& token) {
    ParsedTarget out;
    out.raw = trim_str(token);
    if (out.raw.empty()) return out;

    int page = extract_page_param(out.raw);

    // 1. Favorites list
    std::string fid = match_digits_after(out.raw, "?fid=");
    if (fid.empty()) fid = match_digits_after(out.raw, "&fid=");
    if (fid.empty()) fid = match_digits_after(out.raw, "?media_id=");
    if (fid.empty()) fid = match_digits_after(out.raw, "&media_id=");
    if (fid.empty()) fid = match_digits_after(out.raw, "/medialist/detail/ml", true);
    if (fid.empty() && out.raw.size() > 2 &&
        (out.raw[0] == 'm' || out.raw[0] == 'M') && (out.raw[1] == 'l' || out.raw[1] == 'L')) {
        bool all_digits = true;
        for (size_t i = 2; i < out.raw.size(); ++i) {
            if (!std::isdigit(static_cast<unsigned char>(out.raw[i]))) {
                all_digits = false;
                break;
            }
        }
        if (all_digits) fid = out.raw.substr(2);
    }
    if (!fid.empty()) {
        out.kind = "favlist";
        out.media_id = fid;
        return out;
    }

    // 2. Short URL: b23.tv/xxxx
    size_t b23 = out.raw.find("b23.tv/");
    if (b23 != std::string::npos) {
        size_t start = b23 + 7;
        size_t p = start;
        while (p < out.raw.size() && is_alnum_ascii(out.raw[p])) ++p;
        if (p > start) {
            out.kind = "shorturl";
            out.url = "https://b23.tv/" + out.raw.substr(start, p - start);
            return out;
        }
    }

    // 3. BV ID
    std::string bv_suffix = strip_bv(out.raw);
    if (!bv_suffix.empty() && out.raw.size() >= 12) {
        std::string bv = "BV1" + bv_suffix;
        int64_t aid = 0;
        if (decode_bv(bv, aid)) {
            out.kind = "video";
            out.bvid = bv;
            out.aid = aid;
            out.page = page;
            return out;
        }
        out.kind = "invalid";
        out.reason = "BV 号校验无效";
        return out;
    }

    // 4. av ID
    std::string av_num = match_digits_after(out.raw, "/video/av", true);
    if (av_num.empty() && out.raw.size() > 2 &&
        (out.raw[0] == 'a' || out.raw[0] == 'A') && (out.raw[1] == 'v' || out.raw[1] == 'V')) {
        bool all_digits = true;
        for (size_t i = 2; i < out.raw.size(); ++i) {
            if (!std::isdigit(static_cast<unsigned char>(out.raw[i]))) {
                all_digits = false;
                break;
            }
        }
        if (all_digits) av_num = out.raw.substr(2);
    }
    if (!av_num.empty()) {
        int64_t aid = std::strtoll(av_num.c_str(), nullptr, 10);
        std::string bv = encode_bv(aid);
        if (!bv.empty()) {
            out.kind = "video";
            out.bvid = bv;
            out.aid = aid;
            out.page = page;
            return out;
        }
        out.kind = "invalid";
        out.reason = "AV 号超出有效范围";
        return out;
    }

    out.kind = "invalid";
    out.reason = "未识别的 BV/AV 号、视频链接或收藏夹链接";
    return out;
}

inline std::vector<ParsedTarget> parse_input(const std::string& text) {
    std::vector<ParsedTarget> targets;
    std::set<std::string> seen;
    size_t pos = 0;
    while (pos <= text.size()) {
        size_t nl = text.find_first_of("\r\n", pos);
        std::string line = (nl == std::string::npos) ? text.substr(pos) : text.substr(pos, nl - pos);
        pos = (nl == std::string::npos) ? text.size() + 1 : nl + 1;

        std::string cleaned = trim_str(line);
        if (cleaned.empty()) continue;

        std::string candidate = cleaned;
        size_t http = cleaned.find("http://");
        if (http == std::string::npos) http = cleaned.find("https://");
        if (http != std::string::npos) {
            size_t end = http;
            while (end < cleaned.size() && !std::isspace(static_cast<unsigned char>(cleaned[end]))) ++end;
            candidate = cleaned.substr(http, end - http);
        }
        ParsedTarget parsed = parse_token(candidate);
        if (parsed.kind.empty()) continue;

        std::string dedupe;
        if (parsed.kind == "video") {
            dedupe = "v:" + parsed.bvid + ":" + std::to_string(parsed.page);
        } else if (parsed.kind == "favlist") {
            dedupe = "f:" + parsed.media_id;
        } else if (parsed.kind == "shorturl") {
            dedupe = "s:" + parsed.url;
        } else {
            dedupe = "x:" + cleaned;
        }
        if (seen.insert(dedupe).second) {
            targets.push_back(std::move(parsed));
        }
    }
    return targets;
}

inline std::pair<std::string, std::string> api_bases(const std::string& override_base) {
    if (!override_base.empty()) {
        std::string base = override_base;
        while (!base.empty() && base.back() == '/') base.pop_back();
        return { base, base };
    }
    return { "https://api.bilibili.com", "https://passport.bilibili.com" };
}

inline std::string qr_generate_url(const std::string& override_base) {
    return api_bases(override_base).second + "/x/passport-login/web/qrcode/generate";
}
inline std::string qr_poll_url(const std::string& key, const std::string& override_base) {
    return api_bases(override_base).second + "/x/passport-login/web/qrcode/poll?qrcode_key=" + url_encode(key);
}
inline std::string nav_url(const std::string& override_base) {
    return api_bases(override_base).first + "/x/web-interface/nav";
}
inline std::string view_url(const std::string& bvid_str, int64_t aid, const std::string& override_base) {
    std::string api = api_bases(override_base).first;
    if (!bvid_str.empty()) return api + "/x/web-interface/view?bvid=" + url_encode(bvid_str);
    return api + "/x/web-interface/view?aid=" + std::to_string(aid);
}
inline std::string favlist_url(const std::string& media_id, int pn, int ps, const std::string& override_base) {
    return api_bases(override_base).first + "/x/v3/fav/resource/list?media_id=" + url_encode(media_id) +
           "&pn=" + std::to_string(pn) + "&ps=" + std::to_string(ps) + "&platform=web";
}
inline std::string playurl_wbi(const std::string& bvid_str, int64_t cid,
                               const std::string& key, int64_t unix_time,
                               const std::string& override_base) {
    std::string qs, wrid;
    std::map<std::string, std::string> params = {
        { "bvid", bvid_str },
        { "cid", std::to_string(cid) },
        { "fnval", "4048" },
        { "fnver", "0" },
        { "fourk", "1" },
    };
    if (!wbi_sign(params, key, unix_time, qs, wrid)) return "";
    return api_bases(override_base).first + "/x/player/wbi/playurl?" + qs;
}
inline std::string playurl_fallback(const std::string& bvid_str, int64_t cid, const std::string& override_base) {
    return api_bases(override_base).first + "/x/player/playurl?bvid=" + url_encode(bvid_str) +
           "&cid=" + std::to_string(cid) + "&fnval=4048&fnver=0&fourk=1";
}

struct AccountInfo {
    bool logged_in = false;
    std::string nickname;
    int64_t mid = 0;
    int level = 0;
    bool vip = false;

    Json to_json() const {
        Json o = Json::object();
        o["loggedIn"] = logged_in;
        o["nickname"] = nickname;
        o["mid"] = mid;
        o["level"] = level;
        o["vip"] = vip;
        return o;
    }
};

struct NavResult {
    AccountInfo account;
    std::string wbi_key_str;
};

inline bool parse_nav(const Json& body, NavResult& out) {
    if (!body.is_object() || !body.get("data").is_object()) return false;
    const Json& d = body.get("data");
    if (d.get("wbi_img").is_object()) {
        std::string img = d.get("wbi_img").str("img_url");
        std::string sub = d.get("wbi_img").str("sub_url");
        if (!img.empty() && !sub.empty()) {
            out.wbi_key_str = wbi_key(img, sub);
        }
    }
    bool is_login = d.boolean("isLogin");
    out.account.logged_in = is_login;
    if (is_login) {
        out.account.nickname = d.str("uname");
        out.account.mid = d.i64("mid");
        if (d.get("level_info").is_object()) {
            out.account.level = static_cast<int>(d.get("level_info").i64("current_level"));
        }
        out.account.vip = (d.i64("vipStatus") == 1);
    }
    return true;
}

struct PreviewTrack {
    std::string dedupe_key;
    std::string bvid;
    int64_t aid = 0;
    int64_t cid = 0;
    int page = 1;
    int total_pages = 1;
    std::string title;
    std::string artist = "Bilibili";
    int64_t duration_ms = 0;
    bool selected = true;
};

inline bool parse_view(const Json& body, int requested_page, std::vector<PreviewTrack>& items, std::string& err) {
    if (!body.is_object()) {
        err = "视频信息响应无效";
        return false;
    }
    if (body.i64("code", -1) != 0 || !body.get("data").is_object()) {
        err = body.str("message", "Bilibili 返回错误码 " + std::to_string(body.i64("code", -1)));
        return false;
    }
    const Json& d = body.get("data");
    std::string bv = d.str("bvid");
    int64_t aid = d.i64("aid");
    if (bv.empty() && aid > 0) bv = encode_bv(aid);
    std::string main_title = d.str("title", bv);
    std::string artist = "Bilibili";
    if (d.get("owner").is_object() && !d.get("owner").str("name").empty()) {
        artist = d.get("owner").str("name");
    }
    const Json& pages = d.get("pages");
    if (!pages.is_array() || pages.as_array().empty()) {
        PreviewTrack t;
        t.dedupe_key = bv + ":p1";
        t.bvid = bv;
        t.aid = aid;
        t.cid = d.i64("cid");
        t.page = 1;
        t.total_pages = 1;
        t.title = main_title;
        t.artist = artist;
        t.duration_ms = std::max<int64_t>(0, static_cast<int64_t>(d.num("duration") * 1000.0));
        t.selected = true;
        items.push_back(std::move(t));
        return true;
    }
    int total = static_cast<int>(pages.as_array().size());
    for (int idx = 0; idx < total; ++idx) {
        const Json& pg = pages.as_array()[static_cast<size_t>(idx)];
        if (!pg.is_object()) continue;
        int pnum = static_cast<int>(pg.i64("page", idx + 1));
        std::string part_name = pg.str("part");
        std::string title = main_title;
        if (total > 1) {
            if (!part_name.empty() && part_name != main_title) {
                title = main_title + " · P" + std::to_string(pnum) + " " + part_name;
            } else {
                title = main_title + " · P" + std::to_string(pnum);
            }
        }
        PreviewTrack t;
        t.dedupe_key = bv + ":p" + std::to_string(pnum);
        t.bvid = bv;
        t.aid = aid;
        t.cid = pg.i64("cid");
        t.page = pnum;
        t.total_pages = total;
        t.title = title;
        t.artist = artist;
        t.duration_ms = std::max<int64_t>(0, static_cast<int64_t>(pg.num("duration") * 1000.0));
        t.selected = (requested_page <= 0) || (pnum == requested_page);
        items.push_back(std::move(t));
    }
    return true;
}

struct FavlistPage {
    std::string title;
    std::vector<PreviewTrack> items;
    std::vector<std::string> skipped;
    bool has_more = false;
};

inline bool parse_favlist(const Json& body, FavlistPage& out, std::string& err) {
    if (!body.is_object()) {
        err = "收藏夹响应无效";
        return false;
    }
    if (body.i64("code", -1) != 0 || !body.get("data").is_object()) {
        err = body.str("message", "收藏夹返回错误码 " + std::to_string(body.i64("code", -1)));
        return false;
    }
    const Json& d = body.get("data");
    out.title = d.get("info").is_object() ? d.get("info").str("title", "Bilibili 收藏夹") : "Bilibili 收藏夹";
    out.has_more = d.boolean("has_more");
    if (d.get("medias").is_array()) {
        for (const Json& m : d.get("medias").as_array()) {
            if (!m.is_object()) continue;
            std::string bv = m.str("bvid");
            if (bv.empty()) bv = m.str("bv_id");
            int64_t aid = m.i64("id");
            if (bv.empty() && aid > 0) bv = encode_bv(aid);
            int64_t attr = m.i64("attr");
            std::string title = m.str("title", bv);
            if (bv.empty() || title == "已失效视频" || (attr != 0 && attr != 2)) {
                out.skipped.push_back(title + "（已失效，已跳过）");
                continue;
            }
            std::string artist = "Bilibili";
            if (m.get("upper").is_object() && !m.get("upper").str("name").empty()) {
                artist = m.get("upper").str("name");
            }
            int64_t cid = 0;
            if (m.get("ugc").is_object()) {
                cid = m.get("ugc").i64("first_cid");
            }
            PreviewTrack t;
            t.dedupe_key = bv + ":p1";
            t.bvid = bv;
            t.aid = aid;
            t.cid = cid;
            t.page = 1;
            t.total_pages = static_cast<int>(m.i64("page", 1));
            t.title = title;
            t.artist = artist;
            t.duration_ms = std::max<int64_t>(0, static_cast<int64_t>(m.num("duration") * 1000.0));
            t.selected = true;
            out.items.push_back(std::move(t));
        }
    }
    return true;
}

struct AudioCandidate {
    std::string url;
    std::string label;
};

inline std::string audio_quality_label(int64_t id) {
    switch (id) {
    case 30251: return "Hi-Res FLAC";
    case 30250: return "Dolby";
    case 30280: return "192K AAC";
    case 30232: return "132K AAC";
    case 30216: return "64K AAC";
    default: return id > 0 ? ("DASH " + std::to_string(id)) : "DASH Audio";
    }
}

inline void append_stream_urls(std::vector<AudioCandidate>& out, std::set<std::string>& seen,
                               const Json& stream, const std::string& label) {
    if (!stream.is_object()) return;
    auto push = [&](const std::string& u) {
        if (!u.empty() && seen.insert(u).second) {
            out.push_back({ u, label });
        }
    };
    std::string base = stream.str("baseUrl");
    if (base.empty()) base = stream.str("base_url");
    push(base);
    const Json& backups = stream.has("backupUrl") ? stream.get("backupUrl") : stream.get("backup_url");
    if (backups.is_array()) {
        for (const Json& b : backups.as_array()) {
            if (b.is_string()) push(b.as_str());
        }
    }
}

inline bool select_audio_candidates(const Json& body, const std::string& preferred_quality,
                                    std::vector<AudioCandidate>& candidates,
                                    int64_t& duration_ms, std::string& err) {
    candidates.clear();
    duration_ms = 0;
    if (!body.is_object()) {
        err = "播放地址响应无效";
        return false;
    }
    if (body.i64("code", -1) != 0 || !body.get("data").is_object()) {
        err = body.str("message", "获取音频流失败，错误码 " + std::to_string(body.i64("code", -1)));
        return false;
    }
    const Json& d = body.get("data");
    const Json& dash = d.get("dash");
    duration_ms = d.i64("timelength");
    if (duration_ms <= 0 && dash.is_object() && dash.has("duration")) {
        duration_ms = static_cast<int64_t>(dash.num("duration") * 1000.0);
    }
    std::set<std::string> seen;
    if (dash.is_object()) {
        std::map<int64_t, const Json*> by_id;
        std::vector<const Json*> others;
        if (dash.get("audio").is_array()) {
            for (const Json& a : dash.get("audio").as_array()) {
                if (!a.is_object()) continue;
                int64_t id = a.i64("id");
                if (id > 0 && by_id.find(id) == by_id.end()) {
                    by_id[id] = &a;
                } else {
                    others.push_back(&a);
                }
            }
        }
        if (dash.get("flac").is_object() && dash.get("flac").get("audio").is_object()) {
            by_id[30251] = &dash.get("flac").get("audio");
        }
        std::vector<int64_t> order;
        if (preferred_quality == "flac") order = { 30251, 30280, 30232, 30216 };
        else if (preferred_quality == "132k") order = { 30232, 30280, 30216 };
        else if (preferred_quality == "64k") order = { 30216, 30232, 30280 };
        else order = { 30280, 30232, 30216, 30251 };

        for (int64_t id : order) {
            auto it = by_id.find(id);
            if (it != by_id.end()) {
                append_stream_urls(candidates, seen, *it->second, audio_quality_label(id));
            }
        }
        std::sort(others.begin(), others.end(), [](const Json* a, const Json* b) {
            return a->i64("bandwidth") > b->i64("bandwidth");
        });
        for (const Json* a : others) {
            append_stream_urls(candidates, seen, *a, audio_quality_label(a->i64("id")));
        }
    }
    if (candidates.empty() && d.get("durl").is_array()) {
        for (const Json& item : d.get("durl").as_array()) {
            if (!item.is_object()) continue;
            std::string u = item.str("url");
            if (!u.empty() && seen.insert(u).second) {
                candidates.push_back({ u, "Stream" });
            }
            if (item.get("backup_url").is_array()) {
                for (const Json& bu : item.get("backup_url").as_array()) {
                    if (bu.is_string() && !bu.as_str().empty() && seen.insert(bu.as_str()).second) {
                        candidates.push_back({ bu.as_str(), "Stream" });
                    }
                }
            }
        }
    }
    if (candidates.empty()) {
        err = "该视频未返回可播放的音频流（可能需要登录或无可用音频轨）";
        return false;
    }
    return true;
}

} // namespace bili
} // namespace music
