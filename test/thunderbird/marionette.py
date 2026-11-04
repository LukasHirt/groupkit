# SPDX-FileCopyrightText: 2026 groupkit contributors
#
# SPDX-License-Identifier: Apache-2.0

"""A minimal Marionette client that runs privileged JavaScript inside Thunderbird.

It speaks the length-prefixed JSON protocol of the remote-control server and needs
only the standard library."""
import json, socket, sys

class Marionette:
    def __init__(self, host="127.0.0.1", port=2828, timeout=120):
        self.s = socket.create_connection((host, port), timeout=timeout)
        self.buf = b""
        self.id = 0
        self.hello = self._read()
        self.cmd("WebDriver:NewSession", {"capabilities": {}})
        self.cmd("Marionette:SetContext", {"value": "chrome"})

    def _read(self):
        while b":" not in self.buf:
            self.buf += self.s.recv(65536)
        n, rest = self.buf.split(b":", 1)
        n = int(n)
        while len(rest) < n:
            rest += self.s.recv(65536)
        msg, self.buf = rest[:n], rest[n:]
        return json.loads(msg.decode("utf-8"))

    def cmd(self, name, params=None):
        self.id += 1
        payload = json.dumps([0, self.id, name, params or {}]).encode("utf-8")
        self.s.sendall(str(len(payload)).encode() + b":" + payload)
        while True:
            r = self._read()
            if isinstance(r, list) and r[0] == 1 and r[1] == self.id:
                if r[2]:
                    raise RuntimeError(json.dumps(r[2])[:800])
                return r[3]

    def run(self, js, timeout_ms=90000):
        """Run an async function body in chrome scope; `return` a JSON-able value."""
        script = ("const done = arguments[arguments.length - 1];"
                  "(async () => {" + js + "})().then(v => done({ok: v}),"
                  " e => done({error: String(e), stack: String(e && e.stack)}));")
        self.cmd("WebDriver:SetTimeouts", {"script": timeout_ms})
        res = self.cmd("WebDriver:ExecuteAsyncScript", {"script": script, "args": []})
        v = res.get("value", res) if isinstance(res, dict) else res
        if isinstance(v, dict) and "error" in v:
            raise RuntimeError(v["error"] + "\n" + v.get("stack", ""))
        return v.get("ok") if isinstance(v, dict) else v

if __name__ == "__main__":
    m = Marionette()
    out = m.run(open(sys.argv[1]).read())
    print(json.dumps(out, indent=1, ensure_ascii=False))
