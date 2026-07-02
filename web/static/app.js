"use strict";

// ---- DOM references -------------------------------------------------------
const descriptionInput = document.getElementById("description");
const expiresSelect = document.getElementById("expires");
const dropzone = document.getElementById("dropzone");
const dropHint = document.getElementById("drop-hint");
const textInput = document.getElementById("text-input");
const browseBtn = document.getElementById("browse-btn");
const uploadTextBtn = document.getElementById("upload-text-btn");
const fileInput = document.getElementById("file-input");
const uploadsSection = document.getElementById("uploads-section");
const uploadsList = document.getElementById("uploads-list");
const filesList = document.getElementById("files-list");
const filesEmpty = document.getElementById("files-empty");
const sentinel = document.getElementById("sentinel");
const encryptToggle = document.getElementById("encrypt-toggle");
const encryptNote = document.getElementById("encrypt-note");

// ---- Helpers --------------------------------------------------------------
// sanitizeSuffix mirrors what the server does to the URL suffix: lowercase and
// whitespace collapsed to dashes. The server does the final full sanitize.
function sanitizeSuffix(s) {
  return s.toLowerCase().replace(/\s+/g, "-");
}

// Live-sanitize the suffix field as the user types, preserving the caret.
descriptionInput.addEventListener("input", () => {
  const caret = sanitizeSuffix(descriptionInput.value.slice(0, descriptionInput.selectionStart)).length;
  descriptionInput.value = sanitizeSuffix(descriptionInput.value);
  descriptionInput.setSelectionRange(caret, caret);
});

function humanSize(bytes) {
  if (bytes < 1024) return bytes + " B";
  const units = ["KB", "MB", "GB", "TB"];
  let val = bytes / 1024;
  let i = 0;
  while (val >= 1024 && i < units.length - 1) {
    val /= 1024;
    i++;
  }
  return val.toFixed(val < 10 ? 1 : 0) + " " + units[i];
}

function fileUrl(id) {
  return new URL("/" + id, window.location.origin).href;
}

// ---- Encryption -----------------------------------------------------------
// randomKey returns a fresh URL-safe base64 key (128 bits of entropy). A new
// key is minted for every upload; it is never shown, reused, or persisted as a
// setting — it only lives in the share link's #fragment and the per-file key
// map below.
function randomKey() {
  const bytes = new Uint8Array(16);
  crypto.getRandomValues(bytes);
  let s = "";
  for (const b of bytes) s += String.fromCharCode(b);
  return btoa(s).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

// The Encrypt toggle is a remembered preference (on/off only — never a key).
const ENCRYPT_PREF = "ff-encrypt";
function syncEncrypt() {
  const on = encryptToggle.checked;
  encryptNote.classList.toggle("hidden", !on);
  try {
    localStorage.setItem(ENCRYPT_PREF, on ? "1" : "0");
  } catch (err) {
    /* ignore */
  }
}
function restoreEncryptPref() {
  try {
    encryptToggle.checked = localStorage.getItem(ENCRYPT_PREF) === "1";
  } catch (err) {
    /* ignore */
  }
  encryptNote.classList.toggle("hidden", !encryptToggle.checked);
}
encryptToggle.addEventListener("change", syncEncrypt);

// Keys are stored only in this browser's localStorage, keyed by file id, so the
// Files list can rebuild working share links. They never leave the browser
// except inside a share link's #fragment. Each record keeps a timestamp so old
// keys can be pruned (see pruneKeys) rather than accumulating forever.
const KEY_PREFIX = "ff-key:";
const KEY_TTL_MS = 48 * 60 * 60 * 1000; // forget locally-stored keys after 48h
function storeKey(id, key) {
  try {
    localStorage.setItem(KEY_PREFIX + id, JSON.stringify({ k: key, t: Date.now() }));
  } catch (err) {
    console.error("could not persist key", err);
  }
}
function loadKey(id) {
  try {
    const raw = localStorage.getItem(KEY_PREFIX + id);
    if (!raw) return null;
    const rec = JSON.parse(raw);
    return rec && rec.k ? rec.k : null;
  } catch (err) {
    return null;
  }
}
function forgetKey(id) {
  try {
    localStorage.removeItem(KEY_PREFIX + id);
  } catch (err) {
    /* ignore */
  }
}
// pruneKeys drops locally-stored keys older than KEY_TTL_MS (or malformed ones)
// so the browser doesn't retain them indefinitely. Runs once at startup.
function pruneKeys() {
  const now = Date.now();
  const stale = [];
  try {
    for (let i = 0; i < localStorage.length; i++) {
      const name = localStorage.key(i);
      if (!name || !name.startsWith(KEY_PREFIX)) continue;
      let ok = false;
      try {
        const rec = JSON.parse(localStorage.getItem(name));
        ok = rec && typeof rec.t === "number" && now - rec.t <= KEY_TTL_MS;
      } catch (err) {
        ok = false;
      }
      if (!ok) stale.push(name);
    }
    for (const name of stale) localStorage.removeItem(name);
  } catch (err) {
    /* localStorage unavailable; nothing to prune */
  }
}
// shareUrlFor returns the file's URL, appending the key as the URL fragment
// when this browser holds the key for an encrypted file. The fragment is
// reserved entirely for the key — it is the raw key, with no "key=" prefix.
function shareUrlFor(id) {
  const base = fileUrl(id);
  if (!id.endsWith(".encr")) return base;
  const key = loadKey(id);
  return key ? base + "#" + encodeURIComponent(key) : base;
}

async function copyToClipboard(text, btn) {
  try {
    await navigator.clipboard.writeText(text);
    const original = btn.textContent;
    btn.textContent = "Copied!";
    setTimeout(() => {
      btn.textContent = original;
    }, 1200);
  } catch (err) {
    console.error("copy failed", err);
  }
}

// ---- Upload flow ----------------------------------------------------------
async function uploadBlob(blob, filename) {
  const slug = descriptionInput.value.trim();
  const expireDays = Number(expiresSelect.value);
  // Mint a fresh key per upload (never reused, never shown). Captured here so
  // later toggling doesn't affect an in-flight transfer.
  const encrypt = encryptToggle.checked;
  const key = encrypt ? randomKey() : "";

  let created;
  try {
    const res = await fetch("/upload/api/create", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ filename, slug, expireDays, encrypt }),
    });
    if (!res.ok) throw new Error("create failed: " + res.status);
    created = await res.json();
  } catch (err) {
    console.error(err);
    renderUploadCard(filename, null, "failed");
    return;
  }

  // The key never touches the URL path or query — it rides in the fragment
  // (raw, with no "key=" prefix; the fragment is reserved for the key), which
  // the browser keeps out of requests and thus out of server logs. Save it
  // locally so the Files list can rebuild the link later on this browser.
  const shareUrl = encrypt ? created.url + "#" + encodeURIComponent(key) : created.url;
  if (encrypt) storeKey(created.id, key);

  // Show the URL immediately, before bytes finish uploading.
  const card = renderUploadCard(filename, shareUrl, "uploading");
  const bar = card.querySelector(".upload-bar");
  const status = card.querySelector(".upload-status");

  const xhr = new XMLHttpRequest();
  xhr.open("PUT", "/upload/api/put/" + encodeURIComponent(created.id));
  if (encrypt) {
    // The key and the original filename travel as headers, not query params:
    // the filename is embedded (encrypted) so the ".encr" URL leaks no type.
    xhr.setRequestHeader("X-Encryption-Key", key);
    xhr.setRequestHeader("X-Filename", encodeURIComponent(filename));
  }
  xhr.upload.onprogress = (e) => {
    if (e.lengthComputable) {
      const pct = Math.round((e.loaded / e.total) * 100);
      bar.style.width = pct + "%";
    }
  };
  xhr.onload = () => {
    if (xhr.status === 204) {
      bar.style.width = "100%";
      status.textContent = "done";
      status.className = "upload-status text-xs font-medium text-emerald-600";
      // Auto-copy the share URL to the clipboard (best-effort; the copy button
      // shows "Copied!" feedback and copyToClipboard swallows failures).
      const copyBtn = card.querySelector(".upload-copy");
      if (copyBtn) copyToClipboard(shareUrl, copyBtn);
      // Refresh the Files list from page 1.
      resetFiles();
    } else {
      markFailed(status);
    }
  };
  xhr.onerror = () => markFailed(status);
  xhr.send(blob);
}

function markFailed(status) {
  status.textContent = "failed";
  status.className = "upload-status text-xs font-medium text-red-600";
}

function renderUploadCard(filename, url, state) {
  uploadsSection.classList.remove("hidden");
  const card = document.createElement("div");
  card.className = "rounded-lg border border-slate-200 bg-white p-4 shadow-sm";

  const statusClass =
    state === "failed"
      ? "upload-status text-xs font-medium text-red-600"
      : "upload-status text-xs font-medium text-slate-500";

  card.innerHTML = `
    <div class="mb-2 flex items-center justify-between gap-3">
      <span class="truncate text-sm font-medium text-slate-800">${escapeHtml(filename)}</span>
      <span class="${statusClass}">${state}</span>
    </div>`;

  if (url) {
    const row = document.createElement("div");
    row.className = "mb-2 flex items-center gap-2";
    const input = document.createElement("input");
    input.type = "text";
    input.readOnly = true;
    input.value = url;
    input.className =
      "flex-1 rounded-md border border-slate-200 bg-slate-50 px-2 py-1 text-xs text-slate-600 focus:outline-none";
    const copyBtn = document.createElement("button");
    copyBtn.textContent = "Copy";
    copyBtn.className =
      "upload-copy shrink-0 rounded-md bg-indigo-600 px-3 py-1 text-xs font-medium text-white hover:bg-indigo-700";
    copyBtn.addEventListener("click", () => copyToClipboard(url, copyBtn));
    row.appendChild(input);
    row.appendChild(copyBtn);
    card.appendChild(row);

    const track = document.createElement("div");
    track.className = "h-1.5 w-full overflow-hidden rounded-full bg-slate-200";
    track.innerHTML = `<div class="upload-bar h-full bg-indigo-600 transition-all duration-200" style="width: 0%"></div>`;
    card.appendChild(track);
  }

  uploadsList.prepend(card);
  return card;
}

function escapeHtml(s) {
  const div = document.createElement("div");
  div.textContent = s;
  return div.innerHTML;
}

// ---- Files list (paginated) ----------------------------------------------
let nextCursor = "";
let loading = false;
let hasMore = true;

function resetFiles() {
  nextCursor = "";
  loading = false;
  hasMore = true;
  filesList.innerHTML = "";
  filesEmpty.classList.add("hidden");
  loadNextPage();
}

async function loadNextPage() {
  if (loading || !hasMore) return;
  loading = true;
  try {
    const params = new URLSearchParams({ limit: "50" });
    if (nextCursor) params.set("cursor", nextCursor);
    const res = await fetch("/upload/api/list?" + params.toString());
    if (!res.ok) throw new Error("list failed: " + res.status);
    const data = await res.json();
    const entries = data.entries || [];
    entries.forEach(renderFileRow);
    nextCursor = data.nextCursor || "";
    hasMore = !!nextCursor;
    if (!filesList.children.length) {
      filesEmpty.classList.remove("hidden");
    }
  } catch (err) {
    console.error(err);
    hasMore = false;
  } finally {
    loading = false;
  }
}

function renderFileRow(entry) {
  const encrypted = entry.id.endsWith(".encr");
  // An encrypted file can only be opened from this browser if it holds the key.
  const locked = encrypted && !loadKey(entry.id);
  const row = document.createElement("div");
  row.className = "flex items-center gap-3 px-4 py-3";

  // Label is the full id incl. extension (e.g. 9ef-p9m2rr-my-notes.txt), with a
  // lock glyph for encrypted files. When the key isn't available in this
  // browser the row is not a link — it's dimmed and tagged "[key unavailable]".
  let link;
  if (locked) {
    link = document.createElement("span");
    link.className = "flex-1 truncate font-mono text-sm text-slate-400";
    link.title = "Encrypted — key not available in this browser, so it can't be opened here";
    const id = document.createElement("span");
    id.textContent = "🔒 " + entry.id;
    const tag = document.createElement("span");
    tag.textContent = " [key unavailable]";
    tag.className = "italic";
    link.append(id, tag);
  } else {
    link = document.createElement("a");
    link.href = shareUrlFor(entry.id);
    link.target = "_blank";
    link.rel = "noopener";
    link.textContent = (encrypted ? "🔒 " : "") + entry.id;
    link.className = "flex-1 truncate font-mono text-sm font-medium text-indigo-600 hover:underline";
  }

  const size = document.createElement("span");
  size.textContent = humanSize(entry.size || 0);
  size.className = "shrink-0 w-16 text-right text-xs text-slate-400";

  const date = document.createElement("span");
  date.textContent = new Date(entry.uploadedAt).toLocaleDateString();
  date.className = "shrink-0 w-24 text-right text-xs text-slate-400";

  // No usable link to copy when the key isn't available.
  let copyBtn = null;
  if (!locked) {
    copyBtn = document.createElement("button");
    copyBtn.textContent = "Copy link";
    copyBtn.className =
      "shrink-0 rounded-md border border-slate-200 px-2 py-1 text-xs font-medium text-slate-600 hover:bg-slate-50";
    copyBtn.addEventListener("click", () => copyToClipboard(shareUrlFor(entry.id), copyBtn));
  }

  const delBtn = document.createElement("button");
  delBtn.textContent = "Delete";
  delBtn.className =
    "shrink-0 rounded-md px-2 py-1 text-xs font-medium text-red-600 hover:bg-red-50";
  delBtn.addEventListener("click", async () => {
    if (!confirm("Delete " + (entry.slug || entry.id) + "?")) return;
    try {
      const res = await fetch("/upload/api/file/" + encodeURIComponent(entry.id), {
        method: "DELETE",
      });
      if (res.status === 204) {
        forgetKey(entry.id);
        row.remove();
      } else console.error("delete failed: " + res.status);
    } catch (err) {
      console.error(err);
    }
  });

  row.appendChild(link);
  row.appendChild(size);
  row.appendChild(date);
  if (copyBtn) row.appendChild(copyBtn);
  row.appendChild(delBtn);
  filesList.appendChild(row);
}

// ---- Input wiring ---------------------------------------------------------
browseBtn.addEventListener("click", () => fileInput.click());

fileInput.addEventListener("change", () => {
  for (const file of fileInput.files) uploadBlob(file, file.name);
  fileInput.value = "";
});

// Upload the textarea contents as a text file, named with the paste ext.
function uploadText() {
  const text = textInput.value;
  if (!text.trim()) return;
  // Always sent as paste.txt; a trailing extension in the URL suffix relabels
  // the served type for text content (see internal/store/id.go splitSuffixExt).
  uploadBlob(new Blob([text], { type: "text/plain" }), "paste.txt");
  textInput.value = "";
  syncUploadTextBtn();
  syncCompose();
}

function syncUploadTextBtn() {
  uploadTextBtn.disabled = !textInput.value.trim();
}

// syncCompose collapses the "drop or paste" hint and grows the textarea to fill
// the card once the user starts composing (textarea focused or non-empty), so
// the two don't sit awkwardly stacked in the same bubble. Driven with inline
// styles (with transition-* utilities on the elements) to animate smoothly.
function syncCompose() {
  const composing = document.activeElement === textInput || textInput.value.length > 0;
  if (composing) {
    // Zero the padding too: with box-sizing:border-box, max-height:0 clips the
    // content but padding can't shrink below itself, so pt-10/pb-2 would leave
    // ~48px of empty space at the top of the field.
    dropHint.style.paddingTop = "0px";
    dropHint.style.paddingBottom = "0px";
    dropHint.style.maxHeight = "0px";
    dropHint.style.opacity = "0";
    textInput.style.minHeight = "16rem";
    textInput.classList.remove("text-center");
  } else {
    dropHint.style.paddingTop = "";
    dropHint.style.paddingBottom = "";
    dropHint.style.maxHeight = dropHint.scrollHeight + "px";
    dropHint.style.opacity = "1";
    textInput.style.minHeight = "";
    textInput.classList.add("text-center");
  }
}

textInput.addEventListener("input", () => {
  syncUploadTextBtn();
  syncCompose();
});
textInput.addEventListener("focus", syncCompose);
textInput.addEventListener("blur", syncCompose);
uploadTextBtn.addEventListener("click", uploadText);

// Cmd/Ctrl+Enter uploads the typed text.
textInput.addEventListener("keydown", (e) => {
  if ((e.metaKey || e.ctrlKey) && e.key === "Enter") {
    e.preventDefault();
    uploadText();
  }
});

// Pasting files/images into the compose box uploads them; plain text pastes
// into the box normally so it can be edited before uploading.
textInput.addEventListener("paste", (e) => {
  const cd = e.clipboardData;
  if (cd && cd.files && cd.files.length) {
    e.preventDefault();
    for (const file of cd.files) uploadBlob(file, file.name);
  }
});

dropzone.addEventListener("dragover", (e) => {
  e.preventDefault();
  dropzone.classList.add("border-indigo-500", "bg-indigo-50");
});
dropzone.addEventListener("dragleave", () => {
  dropzone.classList.remove("border-indigo-500", "bg-indigo-50");
});
dropzone.addEventListener("drop", (e) => {
  e.preventDefault();
  dropzone.classList.remove("border-indigo-500", "bg-indigo-50");
  if (e.dataTransfer && e.dataTransfer.files) {
    for (const file of e.dataTransfer.files) uploadBlob(file, file.name);
  }
});

// Prevent stray drops elsewhere from navigating away.
document.addEventListener("dragover", (e) => e.preventDefault());
document.addEventListener("drop", (e) => e.preventDefault());

// Paste anywhere (unless focused in an input/textarea).
document.addEventListener("paste", (e) => {
  const tag = e.target && e.target.tagName;
  if (tag === "INPUT" || tag === "TEXTAREA") return;
  const cd = e.clipboardData;
  if (!cd) return;

  if (cd.files && cd.files.length) {
    for (const file of cd.files) uploadBlob(file, file.name);
    return;
  }
  const text = cd.getData("text/plain");
  if (text) {
    const blob = new Blob([text], { type: "text/plain" });
    uploadBlob(blob, "paste.txt");
  }
});

// ---- Infinite scroll ------------------------------------------------------
const observer = new IntersectionObserver(
  (entries) => {
    if (entries.some((en) => en.isIntersecting)) loadNextPage();
  },
  { rootMargin: "200px" }
);
observer.observe(sentinel);

// ---- Terminal usage snippets ---------------------------------------------
// Build the sample commands against the live origin so they're copy-paste
// ready, then wire the per-snippet Copy buttons.
function initTerminalSnippets() {
  const api = window.location.origin + "/upload/api";
  const snippets = {
    "cmd-curl":
`FILE=./notes.txt

# 1) create an upload slot (captures the id + share url)
RESP=$(curl -sS -X POST ${api}/create \\
  -H 'Content-Type: application/json' \\
  -d "{\\"filename\\":\\"$(basename "$FILE")\\",\\"expireDays\\":365}")
ID=$(echo "$RESP" | jq -r .id)

# 2) upload the bytes, then print the share link
curl -sS -X PUT --data-binary @"$FILE" ${api}/put/$ID \\
  && echo "$RESP" | jq -r .url`,
    "cmd-stdin":
`ID=$(curl -sS -X POST ${api}/create \\
  -H 'Content-Type: application/json' \\
  -d '{"filename":"paste.txt"}' | jq -r .id)

echo "hello from the terminal" | curl -sS -X PUT --data-binary @- ${api}/put/$ID
echo "${window.location.origin}/$ID"`,
    "cmd-wget":
`FILE=./notes.txt

ID=$(wget -qO- --method=POST \\
  --header='Content-Type: application/json' \\
  --body-data="{\\"filename\\":\\"$(basename "$FILE")\\"}" \\
  ${api}/create | jq -r .id)

wget -qO- --method=PUT --body-file="$FILE" ${api}/put/$ID
echo "${window.location.origin}/$ID"`,
  };

  for (const [id, cmd] of Object.entries(snippets)) {
    const code = document.querySelector("#" + id + " code");
    if (code) code.textContent = cmd;
  }

  document.querySelectorAll(".cmd-copy").forEach((btn) => {
    btn.addEventListener("click", () => {
      const pre = document.getElementById(btn.dataset.target);
      if (pre) copyToClipboard(pre.textContent, btn);
    });
  });
}

// Initial load.
pruneKeys();
restoreEncryptPref();
initTerminalSnippets();
syncCompose();
loadNextPage();
