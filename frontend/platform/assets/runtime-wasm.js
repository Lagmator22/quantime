/* =====================================================================
   QuanTime · WASM RUNTIME WORKER
   ---------------------------------------------------------------------
   Instantiates a developer's .wasm module. The wasm must export:

     submit_bytes(ptr: i32, len: i32) -> i32   // returns response ptr
     submit_bytes_len() -> i32                 // length of last response
     alloc(n: i32) -> i32                      // wasm-side allocator
     free(ptr: i32) -> void                    // matching free
     memory                                    // WebAssembly.Memory export

   Wire format: orders + responses serialized as compact JSON (UTF-8).
   For peak throughput a binary FlatBuffers / Cap'n Proto format would
   be better; JSON is the documented Layer-1 wire format because it's
   readable and language-agnostic.

   This is what a C++/Rust/Go submission would compile to: emcc / wasm-
   bindgen / TinyGo all produce modules that satisfy this contract
   with a thin adapter.

   If no .wasm was uploaded, we boot in passthrough mode (reference
   engine) - same as the JS runtime fallback.
===================================================================== */
importScripts('engine.js');

let instance = null;
let memory = null;
let alloc = null;
let free = null;
let submit_bytes = null;
let submit_bytes_len = null;
let fallbackEngine = null;

function log(level, msg) { self.postMessage({ type: 'log', level, msg }); }

self.addEventListener('message', async (e) => {
  const m = e.data;

  if (m.type === 'init') {
    try {
      if (!m.wasm) {
        log('info', 'no wasm bytes uploaded - using reference engine');
        fallbackEngine = new MatchingEngine();
        self.postMessage({ type: 'ready' });
        return;
      }

      const wasmBytes = m.wasm;
      log('info', `instantiating wasm module (${wasmBytes.byteLength} bytes)`);

      // Provide minimal imports - most quant code doesn't need stdlib
      const imports = {
        env: {
          // Generic ABI shims
          abort: () => { throw new Error('wasm abort'); },
          // Allow wasm code to log strings
          log_str: (ptr, len) => {
            const view = new Uint8Array(memory.buffer, ptr, len);
            log('info', '[wasm] ' + new TextDecoder().decode(view));
          },
          // Monotonic time in ns
          now_ns: () => BigInt(Math.floor(performance.now() * 1e6)),
        },
        wasi_snapshot_preview1: {
          // Stubs in case the module was compiled with WASI in mind
          proc_exit: (code) => { throw new Error('wasi exit ' + code); },
          fd_write: () => 0,
          fd_close: () => 0,
          fd_seek: () => 0,
          fd_read: () => 0,
          environ_sizes_get: () => 0,
          environ_get: () => 0,
        },
      };

      const result = await WebAssembly.instantiate(wasmBytes, imports);
      instance = result.instance;
      const ex = instance.exports;

      memory = ex.memory;
      alloc = ex.alloc;
      free = ex.free;
      submit_bytes = ex.submit_bytes;
      submit_bytes_len = ex.submit_bytes_len;

      if (!memory) throw new Error('wasm module must export "memory"');
      if (!submit_bytes) {
        log('warn', 'no submit_bytes export - using reference engine');
        fallbackEngine = new MatchingEngine();
      } else {
        log('ok', 'wasm module ready · submit_bytes export found');
      }
      self.postMessage({ type: 'ready' });
    } catch (err) {
      log('err', 'wasm load failed: ' + err.message);
      self.postMessage({ type: 'load-error', message: err.message });
    }
    return;
  }

  if (m.type === 'submit') {
    const t0 = performance.now();
    let result;
    try {
      if (submit_bytes && memory && alloc) {
        // Serialize order as JSON
        const json = JSON.stringify(m.order);
        const bytes = new TextEncoder().encode(json);
        const ptr = alloc(bytes.length);
        if (!ptr) throw new Error('wasm alloc failed');
        new Uint8Array(memory.buffer, ptr, bytes.length).set(bytes);
        const respPtr = submit_bytes(ptr, bytes.length);
        const respLen = submit_bytes_len ? submit_bytes_len() : 0;
        if (free) free(ptr);

        if (respPtr && respLen > 0) {
          const respBytes = new Uint8Array(memory.buffer, respPtr, respLen);
          const respJson = new TextDecoder().decode(respBytes);
          const parsed = JSON.parse(respJson);
          result = {
            acks: Array.isArray(parsed.acks) ? parsed.acks : [],
            fills: Array.isArray(parsed.fills) ? parsed.fills : [],
          };
        } else {
          result = { acks: [{ id: m.order.id, status: 'accepted' }], fills: [] };
        }
      } else if (fallbackEngine) {
        const r = fallbackEngine.submit(m.order);
        result = { acks: r.acks, fills: r.fills };
      } else {
        result = { acks: [{ id: m.order.id, status: 'error', message: 'no runtime' }], fills: [], error: 'no runtime' };
      }
    } catch (e) {
      result = {
        acks: [{ id: m.order.id, status: 'error', message: e.message }],
        fills: [],
        error: e.message,
      };
    }
    result.latencyNs = (performance.now() - t0) * 1e6;
    self.postMessage({ type: 'response', reqId: m.reqId, result });
    return;
  }

  if (m.type === 'snapshot') {
    let snap = null;
    if (fallbackEngine) snap = fallbackEngine.snapshot();
    self.postMessage({ type: 'response', reqId: m.reqId, result: snap });
    return;
  }
});
