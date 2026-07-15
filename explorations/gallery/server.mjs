// Tiny demo-gallery server with per-demo comment threads.
// Run: node server.mjs [port]   (default 4700)
// Demos live in demos/*.html; comments persist to comments/<demo>.json
// so Claude can read feedback directly from the repo.
import { createServer } from "node:http";
import { readFile, readdir, writeFile, mkdir } from "node:fs/promises";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";

const ROOT = dirname(fileURLToPath(import.meta.url));
const PORT = Number(process.argv[2] || 4700);

const RAIL = await readFile(join(ROOT, "rail.js"), "utf8");

async function comments(demo) {
  try {
    return JSON.parse(await readFile(join(ROOT, "comments", demo + ".json"), "utf8"));
  } catch {
    return [];
  }
}

const server = createServer(async (req, res) => {
  const url = new URL(req.url, "http://x");
  const send = (code, body, type = "application/json") => {
    res.writeHead(code, { "content-type": type, "access-control-allow-origin": "*" });
    res.end(body);
  };
  try {
    if (url.pathname === "/") {
      const files = (await readdir(join(ROOT, "demos"))).filter((f) => f.endsWith(".html")).sort();
      const rows = [];
      for (const f of files) {
        const name = f.replace(/\.html$/, "");
        const html = await readFile(join(ROOT, "demos", f), "utf8");
        const title = (html.match(/<title>(.*?)<\/title>/) || [])[1] || name;
        const blurb = (html.match(/<meta name="blurb" content="(.*?)"/) || [])[1] || "";
        const n = (await comments(name)).length;
        rows.push(
          `<a class="card" href="/d/${name}"><h2>${title}</h2><p>${blurb}</p><span class="n">${n ? n + " comment" + (n > 1 ? "s" : "") : "no comments yet"}</span></a>`
        );
      }
      return send(
        200,
        `<!doctype html><meta charset="utf-8"><title>explorations</title><style>
        :root{color-scheme:light dark}body{font:16px/1.5 -apple-system,sans-serif;max-width:720px;margin:6vh auto;padding:0 24px;color:#1a1a1a;background:#fcfcfa}
        @media(prefers-color-scheme:dark){body{color:#ddd;background:#141414}}
        h1{font-weight:600;letter-spacing:-.02em}p.sub{opacity:.6;margin-top:-8px}
        .card{display:block;padding:16px 20px;margin:14px 0;border:1px solid rgba(128,128,128,.25);border-radius:10px;text-decoration:none;color:inherit;transition:transform .15s ease-out,border-color .15s}
        .card:hover{transform:translateY(-1px);border-color:rgba(128,128,128,.6)}
        .card h2{margin:0;font-size:17px;font-weight:600}.card p{margin:4px 0 6px;opacity:.7;font-size:14px}
        .n{font-size:12px;opacity:.5}</style>
        <h1>explorations</h1><p class="sub">tiny demos of the notes+agents direction — open one, press <b>c</b> or click 💬 to comment in place</p>${rows.join("")}`,
        "text/html"
      );
    }
    if (url.pathname.startsWith("/d/")) {
      const name = url.pathname.slice(3).replace(/[^a-z0-9-]/g, "");
      let html = await readFile(join(ROOT, "demos", name + ".html"), "utf8");
      html += `<script>window.__demo=${JSON.stringify(name)}</script><script>${RAIL}</script>`;
      return send(200, html, "text/html");
    }
    if (url.pathname.startsWith("/api/comments/")) {
      const name = url.pathname.split("/").pop().replace(/[^a-z0-9-]/g, "");
      if (req.method === "GET") return send(200, JSON.stringify(await comments(name)));
      if (req.method === "POST") {
        let body = "";
        for await (const c of req) body += c;
        const { text, replyTo = null, author = "nitsan" } = JSON.parse(body);
        const list = await comments(name);
        list.push({ id: list.length + 1, author, text, replyTo, at: new Date().toISOString() });
        await mkdir(join(ROOT, "comments"), { recursive: true });
        await writeFile(join(ROOT, "comments", name + ".json"), JSON.stringify(list, null, 2));
        return send(200, JSON.stringify({ ok: true }));
      }
    }
    send(404, "not found", "text/plain");
  } catch (e) {
    send(500, String(e), "text/plain");
  }
});
server.listen(PORT, () => console.log(`gallery http://localhost:${PORT}`));
