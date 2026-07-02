"use strict";

// ---- DOM references -------------------------------------------------------
const descriptionInput = document.getElementById("description");
const pasteExtInput = document.getElementById("paste-ext");
const expiresSelect = document.getElementById("expires");
const dropzone = document.getElementById("dropzone");
const fileInput = document.getElementById("file-input");
const uploadsSection = document.getElementById("uploads-section");
const uploadsList = document.getElementById("uploads-list");
const filesList = document.getElementById("files-list");
const filesEmpty = document.getElementById("files-empty");
const sentinel = document.getElementById("sentinel");

// ---- Helpers --------------------------------------------------------------
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

  let created;
  try {
    const res = await fetch("/upload/api/create", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ filename, slug, expireDays }),
    });
    if (!res.ok) throw new Error("create failed: " + res.status);
    created = await res.json();
  } catch (err) {
    console.error(err);
    renderUploadCard(filename, null, "failed");
    return;
  }

  // Show the URL immediately, before bytes finish uploading.
  const card = renderUploadCard(filename, created.url, "uploading");
  const bar = card.querySelector(".upload-bar");
  const status = card.querySelector(".upload-status");

  const xhr = new XMLHttpRequest();
  xhr.open("PUT", "/upload/api/put/" + encodeURIComponent(created.id));
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
      "shrink-0 rounded-md bg-indigo-600 px-3 py-1 text-xs font-medium text-white hover:bg-indigo-700";
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
  const url = fileUrl(entry.id);
  const row = document.createElement("div");
  row.className = "flex items-center gap-3 px-4 py-3";

  const link = document.createElement("a");
  link.href = url;
  link.target = "_blank";
  link.rel = "noopener";
  link.textContent = entry.slug || entry.id;
  link.className = "flex-1 truncate text-sm font-medium text-indigo-600 hover:underline";

  const badge = document.createElement("span");
  badge.textContent = entry.ext || "?";
  badge.className =
    "shrink-0 rounded bg-slate-100 px-1.5 py-0.5 text-xs font-medium uppercase text-slate-500";

  const size = document.createElement("span");
  size.textContent = humanSize(entry.size || 0);
  size.className = "shrink-0 w-16 text-right text-xs text-slate-400";

  const date = document.createElement("span");
  date.textContent = new Date(entry.uploadedAt).toLocaleDateString();
  date.className = "shrink-0 w-24 text-right text-xs text-slate-400";

  const copyBtn = document.createElement("button");
  copyBtn.textContent = "Copy link";
  copyBtn.className =
    "shrink-0 rounded-md border border-slate-200 px-2 py-1 text-xs font-medium text-slate-600 hover:bg-slate-50";
  copyBtn.addEventListener("click", () => copyToClipboard(url, copyBtn));

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
      if (res.status === 204) row.remove();
      else console.error("delete failed: " + res.status);
    } catch (err) {
      console.error(err);
    }
  });

  row.appendChild(link);
  row.appendChild(badge);
  row.appendChild(size);
  row.appendChild(date);
  row.appendChild(copyBtn);
  row.appendChild(delBtn);
  filesList.appendChild(row);
}

// ---- Input wiring ---------------------------------------------------------
dropzone.addEventListener("click", () => fileInput.click());

fileInput.addEventListener("change", () => {
  for (const file of fileInput.files) uploadBlob(file, file.name);
  fileInput.value = "";
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
    const ext = (pasteExtInput.value.trim() || "txt");
    const blob = new Blob([text], { type: "text/plain" });
    uploadBlob(blob, "paste." + ext);
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

// Initial load.
loadNextPage();
