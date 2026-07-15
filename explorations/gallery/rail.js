// Comment rail injected into every demo by server.mjs. Press "c" or click 💬.
(() => {
  const demo = window.__demo;
  const css = `
  #snrail{position:fixed;top:0;right:0;height:100%;width:320px;box-sizing:border-box;
    background:rgba(250,250,248,.97);border-left:1px solid rgba(128,128,128,.3);
    transform:translateX(100%);transition:transform .2s ease-out;z-index:9999;
    font:13px/1.45 -apple-system,sans-serif;color:#222;display:flex;flex-direction:column;padding:14px}
  @media(prefers-color-scheme:dark){#snrail{background:rgba(24,24,24,.97);color:#ddd}}
  #snrail.open{transform:none}
  #snrail h3{margin:0 0 4px;font-size:13px;font-weight:600;opacity:.7}
  #snrail .back{font-size:12px;opacity:.55;text-decoration:none;color:inherit;margin-bottom:10px;display:block}
  #snrail .list{flex:1;overflow-y:auto;margin:0 -4px;padding:0 4px}
  #snrail .cm{margin:8px 0;padding:8px 10px;border-radius:8px;background:rgba(128,128,128,.12)}
  #snrail .cm.reply{margin-left:18px}
  #snrail .cm .who{font-weight:600;font-size:11px;opacity:.6;margin-bottom:2px}
  #snrail .cm .rb{font-size:11px;opacity:.45;cursor:pointer;margin-top:4px;display:inline-block}
  #snrail textarea{width:100%;box-sizing:border-box;min-height:60px;border-radius:8px;
    border:1px solid rgba(128,128,128,.35);background:transparent;color:inherit;
    font:inherit;padding:8px;resize:vertical;outline:none}
  #snrail .hint{font-size:11px;opacity:.45;margin-top:4px}
  #snbtn{position:fixed;bottom:16px;right:16px;z-index:9998;border:none;cursor:pointer;
    width:40px;height:40px;border-radius:50%;background:rgba(128,128,128,.15);font-size:17px;
    transition:transform .15s}
  #snbtn:hover{transform:scale(1.1)}`;
  const style = document.createElement("style");
  style.textContent = css;
  document.head.appendChild(style);

  const rail = document.createElement("div");
  rail.id = "snrail";
  rail.innerHTML = `<a class="back" href="/">← all demos</a><h3>thread · ${demo}</h3>
    <div class="list"></div>
    <textarea placeholder="comment on this demo…"></textarea>
    <div class="hint">Enter to send · Shift-Enter newline · c toggles</div>`;
  document.body.appendChild(rail);
  const btn = document.createElement("button");
  btn.id = "snbtn";
  btn.textContent = "💬";
  document.body.appendChild(btn);

  const list = rail.querySelector(".list");
  const ta = rail.querySelector("textarea");
  let replyTo = null;

  async function load() {
    const cs = await fetch(`/api/comments/${demo}`).then((r) => r.json());
    list.innerHTML = "";
    for (const c of cs) {
      const d = document.createElement("div");
      d.className = "cm" + (c.replyTo ? " reply" : "");
      d.innerHTML = `<div class="who">${c.author} · #${c.id}</div><div></div><span class="rb">reply</span>`;
      d.children[1].textContent = c.text;
      d.querySelector(".rb").onclick = () => {
        replyTo = c.id;
        ta.placeholder = `replying to #${c.id}…`;
        ta.focus();
      };
      list.appendChild(d);
    }
    list.scrollTop = list.scrollHeight;
  }

  async function send() {
    const text = ta.value.trim();
    if (!text) return;
    await fetch(`/api/comments/${demo}`, { method: "POST", body: JSON.stringify({ text, replyTo }) });
    ta.value = "";
    replyTo = null;
    ta.placeholder = "comment on this demo…";
    load();
  }

  const toggle = () => {
    rail.classList.toggle("open");
    if (rail.classList.contains("open")) { load(); ta.focus(); }
  };
  btn.onclick = toggle;
  document.addEventListener("keydown", (e) => {
    if (e.key === "c" && document.activeElement !== ta && !e.metaKey && !e.ctrlKey) {
      const t = document.activeElement.tagName;
      if (t !== "INPUT" && t !== "TEXTAREA") { e.preventDefault(); toggle(); }
    }
    if (e.key === "Escape" && rail.classList.contains("open")) rail.classList.remove("open");
  });
  ta.addEventListener("keydown", (e) => {
    if (e.key === "Enter" && !e.shiftKey) { e.preventDefault(); send(); }
    e.stopPropagation(); // don't leak keys into demo handlers
  });
  load();
})();
