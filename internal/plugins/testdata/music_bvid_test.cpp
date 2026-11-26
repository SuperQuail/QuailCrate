#include "bvid.hpp"
#include "json.hpp"

#include <iostream>
#include <map>
#include <string>
#include <vector>

static int fail(const std::string& msg) {
    std::cerr << "FAIL: " << msg << "\n";
    return 1;
}

int main() {
    using namespace music;

    // 1. RFC 1321 MD5 test vectors
    struct Md5Vec { const char* in; const char* want; };
    const Md5Vec md5_vecs[] = {
        {"", "d41d8cd98f00b204e9800998ecf8427e"},
        {"a", "0cc175b9c0f1b6a831c399e269772661"},
        {"abc", "900150983cd24fb0d6963f7d28e17f72"},
        {"message digest", "f96b697d7cb7938d525a2f31aaf161d0"},
        {"abcdefghijklmnopqrstuvwxyz", "c3fcd3d76192e4007dfb496cca67e13b"},
        {"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789", "d174ab98d277d9f5a5611c2c9f419d9f"},
    };
    for (const auto& v : md5_vecs) {
        std::string got = md5_hex(v.in);
        if (got != v.want) {
            return fail(std::string("md5(") + v.in + ") = " + got + ", want " + v.want);
        }
    }

    // 2. BV <-> AID conversion vectors
    int64_t aid0 = 0;
    if (!bili::decode_bv("BV17x411w7KC", aid0) || aid0 != 170001) {
        return fail("BV17x411w7KC decode = " + std::to_string(aid0) + ", want 170001");
    }
    if (bili::encode_bv(aid0) != "BV17x411w7KC") {
        return fail("encode_bv(170001) = " + bili::encode_bv(aid0));
    }
    int64_t aid1 = 0;
    if (!bili::decode_bv("BV1Q541167Qg", aid1) || bili::encode_bv(aid1) != "BV1Q541167Qg") {
        return fail("BV1Q541167Qg roundtrip failed");
    }
    const int64_t aids[] = {1, 2, 170001, 88888888, 1134567890123LL};
    for (int64_t a : aids) {
        std::string bv = bili::encode_bv(a);
        int64_t back = 0;
        if (bv.size() != 12 || bv.rfind("BV1", 0) != 0 || !bili::decode_bv(bv, back) || back != a) {
            return fail("BV roundtrip failed for aid=" + std::to_string(a) + " bv=" + bv);
        }
    }

    // 3. WBI key mixin + parameter sorting + sanitization + MD5 signature
    std::string img_url = "https://i0.hdslb.com/bfs/wbi/4932caff0ff746eab6f01bf08b70ac45.png";
    std::string sub_url = "https://i0.hdslb.com/bfs/wbi/7cd084941338484aae1ad9425b84077c.png";
    std::string key = bili::wbi_key(img_url, sub_url);
    const int mixin[32] = {46, 47, 18, 2, 53, 8, 23, 32, 15, 50, 10, 31, 58, 3, 45, 35, 27, 43, 5, 49, 33, 9, 42, 19, 29, 28, 14, 39, 12, 38, 41, 13};
    std::string orig = "4932caff0ff746eab6f01bf08b70ac457cd084941338484aae1ad9425b84077c";
    std::string want_key;
    for (int idx : mixin) want_key.push_back(orig[static_cast<size_t>(idx)]);
    if (key != want_key) {
        return fail("wbi_key = " + key + ", want " + want_key);
    }
    std::map<std::string, std::string> params = {
        {"bvid", "BV17x411w7KC"},
        {"cid", "279786"},
        {"fnval", "4048"},
        {"note", "a!b'c(d)e*f"},
    };
    std::string qs, wrid;
    if (!bili::wbi_sign(params, key, 1702204169, qs, wrid)) {
        return fail("wbi_sign returned false");
    }
    std::string want_base = "bvid=BV17x411w7KC&cid=279786&fnval=4048&note=abcdef&wts=1702204169";
    std::string want_wrid = md5_hex(want_base + want_key);
    if (wrid != want_wrid || qs != want_base + "&w_rid=" + want_wrid) {
        return fail("wbi_sign mismatch: got " + qs);
    }

    // 4. Multi-line input parsing & deduplication
    std::string input =
        "BV17x411w7KC\n"
        "https://www.bilibili.com/video/BV17x411w7KC?p=3\n"
        "av170001\n"
        "https://space.bilibili.com/12345/favlist?fid=987654\n"
        "https://b23.tv/AbCd12\n"
        "not-a-valid-bilibili-link";
    auto targets = bili::parse_input(input);
    if (targets.size() != 5) {
        return fail("expected 5 deduplicated targets, got " + std::to_string(targets.size()));
    }
    if (targets[0].kind != "video" || targets[0].bvid != "BV17x411w7KC" ||
        targets[1].kind != "video" || targets[1].page != 3 ||
        targets[2].kind != "favlist" || targets[2].media_id != "987654" ||
        targets[3].kind != "shorturl" || targets[3].url != "https://b23.tv/AbCd12" ||
        targets[4].kind != "invalid") {
        return fail("unexpected parsed target fields");
    }

    // 5. DASH audio candidate preference & backup URL ordering
    Json dash_resp;
    const char* dash_json = R"({
      "code": 0,
      "data": {
        "timelength": 185000,
        "dash": {
          "duration": 185,
          "audio": [
            {"id": 30216, "baseUrl": "https://cdn/64k-a", "backupUrl": ["https://cdn/64k-b"]},
            {"id": 30280, "baseUrl": "https://cdn/192k-a", "backupUrl": ["https://cdn/192k-b"]}
          ],
          "flac": {
            "audio": {"id": 30251, "baseUrl": "https://cdn/flac-a"}
          }
        }
      }
    })";
    if (!Json::parse(dash_json, dash_resp)) {
        return fail("failed to parse dash json");
    }
    std::vector<bili::AudioCandidate> cands;
    int64_t dur = 0;
    std::string err;
    if (!bili::select_audio_candidates(dash_resp, "192k", cands, dur, err) || dur != 185000 || cands.size() < 4) {
        return fail("dash 192k selection failed: " + err);
    }
    if (cands[0].url != "https://cdn/192k-a" || cands[1].url != "https://cdn/192k-b") {
        return fail("expected 192k primary+backup first");
    }
    cands.clear();
    if (!bili::select_audio_candidates(dash_resp, "flac", cands, dur, err) || cands.empty() || cands[0].url != "https://cdn/flac-a") {
        return fail("expected flac candidate first");
    }

    std::cout << "OK\n";
    return 0;
}
