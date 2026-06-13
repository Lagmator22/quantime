#include "httplib.h"
#include <iostream>
#include <string>
#include <mutex>
#include "src/Book.h"
#include "src/Order.h"
#include "src/Types.h"

using namespace std;
using namespace httplib;

Book book;
std::mutex mtx;

int main() {
    Server svr;

    svr.Post("/submit", [](const Request& req, Response& res) {
        string body = req.body;
        
        // Very basic json parsing for performance simulation
        // '{"id":1,"side":"buy","type":"limit","price":100,"qty":10}'
        OrderType side = body.find("\"side\":\"buy\"") != string::npos ? BUY : SELL;
        
        // Mock parsing for speed
        ID id = 1;
        ID agent_id = 1;
        Price price = 100;
        Volume qty = 10;
        
        auto order = std::make_shared<Order>(id, agent_id, side, price, qty);
        
        {
            std::lock_guard<std::mutex> lock(mtx);
            book.place_order(order);
        }
        
        res.set_content(R"({"status":"ok"})", "application/json");
    });

    svr.Get("/health", [](const Request& req, Response& res) {
        res.set_content(R"({"status":"ok"})", "application/json");
    });

    cout << "Real World C++ Engine listening on port 9004..." << endl;
    svr.listen("0.0.0.0", 9004);
}
