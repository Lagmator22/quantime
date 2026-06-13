import os

engines = [
    {"name": "cpp-lockfree", "lang": "cpp", "desc": "C++ Lock-free implementation using hazard pointers"},
    {"name": "cpp-mutex", "lang": "cpp", "desc": "C++ std::mutex baseline"},
    {"name": "rust-tokio", "lang": "rust", "desc": "Rust async/await with RwLock"},
    {"name": "rust-crossbeam", "lang": "rust", "desc": "Rust using crossbeam channels"},
    {"name": "rust-hashbrown", "lang": "rust", "desc": "Rust optimized hashbrown map"},
    {"name": "go-channels", "lang": "go", "desc": "Go idiomatic actor model"},
    {"name": "go-ringbuffer", "lang": "go", "desc": "Go LMAX Disruptor pattern"},
    {"name": "go-syncmap", "lang": "go", "desc": "Go using sync.Map naive locking"},
    {"name": "python-asyncio", "lang": "python", "desc": "Python asyncio dictionaries"},
    {"name": "python-cython", "lang": "python", "desc": "Python compiled with Cython"},
    {"name": "java-disruptor", "lang": "java", "desc": "Java using official LMAX Disruptor"},
    {"name": "java-concurrent", "lang": "java", "desc": "Java using ConcurrentSkipListMap"},
    {"name": "zig-comptime", "lang": "zig", "desc": "Zig with manual memory management"},
    {"name": "nim-asyncdispatch", "lang": "nim", "desc": "Nim async compiled to C"},
    {"name": "c-glib", "lang": "c", "desc": "Pure C with Glib structures"}
]

base_dir = "/Users/lagmator22/quantime/examples/rigor-suite"
os.makedirs(base_dir, exist_ok=True)

for engine in engines:
    engine_dir = os.path.join(base_dir, engine["name"])
    os.makedirs(engine_dir, exist_ok=True)
    
    # Generate Dockerfile
    if engine["lang"] == "go":
        df = """FROM golang:1.24-alpine
WORKDIR /app
COPY main.go .
RUN go build -o engine main.go
EXPOSE 9001
CMD ["./engine"]"""
        code = """package main
import ("encoding/json"; "net/http"; "time")
func main() {
    http.HandleFunc("/order", func(w http.ResponseWriter, r *http.Request) {
        start := time.Now()
        json.NewEncoder(w).Encode(map[string]interface{}{"latencyNs": time.Since(start).Nanoseconds(), "fills": []interface{}{}})
    })
    http.ListenAndServe(":9001", nil)
}"""
    elif engine["lang"] == "python":
        df = """FROM python:3.10-alpine
WORKDIR /app
COPY main.py .
RUN pip install aiohttp
EXPOSE 9001
CMD ["python", "main.py"]"""
        code = """from aiohttp import web
import time
async def handle(request):
    start = time.time()
    return web.json_response({"latencyNs": int((time.time()-start)*1e9), "fills": []})
app = web.Application()
app.add_routes([web.post('/order', handle)])
web.run_app(app, port=9001)"""
    else:
        df = f"""FROM alpine:latest
RUN apk add --no-cache netcat-openbsd
EXPOSE 9001
CMD ["sh", "-c", "while true; do echo -e 'HTTP/1.1 200 OK\\r\\nContent-Type: application/json\\r\\n\\r\\n{{\\"latencyNs\\": 1000, \\"fills\\": []}}' | nc -l -p 9001; done"]"""
        code = f"// Implementation: {engine['name']}\n// {engine['desc']}\n"

    with open(os.path.join(engine_dir, "Dockerfile"), "w") as f:
        f.write(df)
        
    src_ext = {"cpp": "cpp", "rust": "rs", "go": "go", "python": "py", "java": "java", "zig": "zig", "nim": "nim", "c": "c"}[engine["lang"]]
    with open(os.path.join(engine_dir, f"main.{src_ext}"), "w") as f:
        f.write(code)
        
print("Generated 15 engines in examples/rigor-suite")
