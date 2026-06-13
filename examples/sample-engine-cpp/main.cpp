// =====================================================================
// SAMPLE DEVELOPER SUBMISSION · C++ matching engine
// ---------------------------------------------------------------------
// A real price-time-priority CLOB that speaks the QuanTime contract, so
// the platform can benchmark engines written in any language behind a
// Dockerfile + POST /submit.
//
//   POST /submit  {id,clientId,side,type,price,qty,targetId,ts}
//                 -> {acks:[{id,status,filled,remaining}], fills:[...]}
//   GET  /healthz -> 200  (the gateway sandbox polls this before deploy)
//
// Prices are integer ticks (price*100), matching the bot fleet + telemetry.
// A mutex serialises the book (cpp-httplib is multi-threaded).
// =====================================================================
#include "httplib.h"
#include <string>
#include <vector>
#include <set>
#include <utility>
#include <algorithm>
#include <mutex>
#include <cctype>

using namespace std;
using namespace httplib;

// ── minimal extractors for the fixed QuanTime order schema ────────────
static long long getInt(const string& b, const string& key) {
    size_t p = b.find("\"" + key + "\"");
    if (p == string::npos) return 0;
    p = b.find(':', p);
    if (p == string::npos) return 0;
    p++;
    while (p < b.size() && (b[p] == ' ' || b[p] == '"')) p++;
    bool neg = false;
    if (p < b.size() && b[p] == '-') { neg = true; p++; }
    long long v = 0; bool any = false;
    while (p < b.size() && isdigit((unsigned char)b[p])) { v = v * 10 + (b[p] - '0'); p++; any = true; }
    if (!any) return 0;
    return neg ? -v : v;
}
static string getStr(const string& b, const string& key) {
    size_t p = b.find("\"" + key + "\"");
    if (p == string::npos) return "";
    p = b.find(':', p); if (p == string::npos) return "";
    p = b.find('"', p); if (p == string::npos) return "";
    p++;
    size_t e = b.find('"', p); if (e == string::npos) return "";
    return b.substr(p, e - p);
}

struct Resting { long long id, px, qty, seq; };

struct Book {
    vector<Resting> bids;            // best (highest px) first; FIFO by seq within px
    vector<Resting> asks;            // best (lowest px) first
    set<pair<long long, long long>> seen; // (clientId, id) dedup
    long long seq = 0;

    long long apply(const string& body, string& status, long long& remOut) {
        string type = getStr(body, "type");
        long long id = getInt(body, "id");
        long long cid = getInt(body, "clientId");
        string side = getStr(body, "side");
        long long px = getInt(body, "price");
        long long qty = getInt(body, "qty");
        long long target = getInt(body, "targetId");
        remOut = 0;

        if (type == "cancel") {
            for (size_t i = 0; i < bids.size(); ++i) if (bids[i].id == target) { bids.erase(bids.begin() + i); status = "cancelled"; return 0; }
            for (size_t i = 0; i < asks.size(); ++i) if (asks[i].id == target) { asks.erase(asks.begin() + i); status = "cancelled"; return 0; }
            status = "cancel-miss"; return 0;
        }
        if (side != "buy" && side != "sell") { status = "rejected"; return 0; }
        if (qty <= 0) { status = "rejected"; return 0; }
        if (type != "market" && px <= 0) { status = "rejected"; return 0; }
        pair<long long, long long> key(cid, id);
        if (id != 0 && seen.count(key)) { status = "rejected"; return 0; }
        if (id != 0) seen.insert(key);

        long long filled = 0, rem = qty;
        bool buy = (side == "buy");
        vector<Resting>& opp = buy ? asks : bids;
        while (!opp.empty() && rem > 0) {
            Resting& best = opp.front();
            if (type == "limit") {
                if (buy && best.px > px) break;
                if (!buy && best.px < px) break;
            }
            long long take = min(rem, best.qty);
            filled += take; rem -= take; best.qty -= take;
            if (best.qty == 0) opp.erase(opp.begin());
        }
        if (rem > 0 && type == "limit") {
            seq++;
            Resting r{ id, px, rem, seq };
            if (buy) {
                bids.push_back(r);
                sort(bids.begin(), bids.end(), [](const Resting& a, const Resting& b) { return a.px != b.px ? a.px > b.px : a.seq < b.seq; });
            } else {
                asks.push_back(r);
                sort(asks.begin(), asks.end(), [](const Resting& a, const Resting& b) { return a.px != b.px ? a.px < b.px : a.seq < b.seq; });
            }
        }
        status = filled == qty ? "filled" : (filled > 0 ? "partial" : (type == "limit" ? "resting" : "unfilled"));
        remOut = rem;
        return filled;
    }
};

Book book;
std::mutex mtx;

int main() {
    Server svr;

    svr.Post("/submit", [](const Request& req, Response& res) {
        string status; long long rem = 0, filled;
        long long id = getInt(req.body, "id");
        {
            std::lock_guard<std::mutex> lock(mtx);
            filled = book.apply(req.body, status, rem);
        }
        string out = "{\"acks\":[{\"id\":" + to_string(id) + ",\"status\":\"" + status +
                     "\",\"filled\":" + to_string(filled) + ",\"remaining\":" + to_string(rem) + "}],\"fills\":[]}";
        res.set_content(out, "application/json");
    });

    auto health = [](const Request&, Response& res) { res.set_content(R"({"status":"ok"})", "application/json"); };
    svr.Get("/healthz", health);
    svr.Get("/health", health);

    svr.listen("0.0.0.0", 9002);
}
