"use strict";

// photocull's whole UI is one dependency-free script: a launcher view (pick a
// mode and folder), a scanning spinner, and a review view (tick the copies to
// remove and send them to the recycle bin). It ships inside the Go binary.

const views = {
  launcher: document.getElementById("launcher"),
  scanning: document.getElementById("scanning"),
  review: document.getElementById("review"),
  "merge-review": document.getElementById("merge-review"),
  "merge-done": document.getElementById("merge-done"),
};

const groupsEl = document.getElementById("groups");
const statsEl = document.getElementById("stats");
const rootEl = document.getElementById("root");
const deleteBtn = document.getElementById("delete-selected");
const selectionCount = document.getElementById("selection-count");
const picked = new Set();

function show(name) {
  for (const [key, el] of Object.entries(views)) {
    el.classList.toggle("hidden", key !== name);
  }
}

function humanBytes(n) {
  if (n < 1024) return `${n} B`;
  const units = ["KB", "MB", "GB", "TB"];
  let v = n / 1024, i = 0;
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++; }
  return `${v.toFixed(1)} ${units[i]}`;
}

// ---- Launcher ----

const folderInput = document.getElementById("folder");
const baseFolderInput = document.getElementById("base-folder");
const sourceFolderInput = document.getElementById("source-folder");
const singleFolder = document.getElementById("single-folder");
const doubleFolder = document.getElementById("double-folder");
const startBtn = document.getElementById("start");
const launchError = document.getElementById("launch-error");
const thresholdRow = document.getElementById("threshold-row");
const thresholdInput = document.getElementById("threshold");
const thresholdVal = document.getElementById("threshold-val");

function selectedMode() {
  return document.querySelector('input[name="mode"]:checked').value;
}

// syncMode reshapes the launcher for the chosen mode: one folder for the two
// duplicate-finding modes, two folders (library + source) for "add to library",
// and a threshold slider whenever similarity matching is in play.
function syncMode() {
  const mode = selectedMode();
  const merge = mode === "merge";
  thresholdRow.classList.toggle("hidden", mode === "exact");
  singleFolder.classList.toggle("hidden", merge);
  doubleFolder.classList.toggle("hidden", !merge);
  startBtn.textContent = merge ? "Find new photos" : "Scan for duplicates";
}

document.querySelectorAll('input[name="mode"]').forEach((r) =>
  r.addEventListener("change", syncMode)
);
thresholdInput.addEventListener("input", () => {
  thresholdVal.textContent = thresholdInput.value;
});

// One handler drives every Browse button; it fills the input named by its
// data-target with the folder the user picks.
document.querySelectorAll(".browse-btn").forEach((btn) => {
  btn.addEventListener("click", async () => {
    try {
      const res = await fetch("/api/browse");
      if (res.status === 501) {
        document.querySelectorAll(".browse-btn").forEach((b) => {
          b.disabled = true;
          b.title = "Type or paste the folder path instead";
        });
        return;
      }
      const data = await res.json();
      if (data.path) document.getElementById(btn.dataset.target).value = data.path;
    } catch {
      btn.disabled = true;
    }
  });
});

const scanPhase = document.getElementById("scan-phase");
const scanningPath = document.getElementById("scanning-path");
const progressBar = document.getElementById("progress-bar");
const scanStats = document.getElementById("scan-stats");
const cancelScanBtn = document.getElementById("cancel-scan");

let pollTimer = null;
let cancelledByUser = false;

function showLaunchError(msg) {
  launchError.textContent = msg;
  launchError.classList.remove("hidden");
  show("launcher");
}

async function startScan() {
  launchError.classList.add("hidden");
  if (selectedMode() === "merge") {
    return startMerge();
  }

  const path = folderInput.value.trim();
  if (!path) {
    showLaunchError("Please choose a folder first.");
    return;
  }

  cancelledByUser = false;
  scanningPath.textContent = path;
  scanPhase.textContent = "Starting…";
  scanStats.textContent = "";
  progressBar.style.width = "0%";
  progressBar.parentElement.classList.add("indeterminate");
  show("scanning");

  let res;
  try {
    res = await fetch("/api/scan", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        path,
        similar: selectedMode() === "similar",
        threshold: Number(thresholdInput.value),
      }),
    });
  } catch (err) {
    showLaunchError("Could not start scan: " + err);
    return;
  }

  if (!res.ok) {
    const body = await res.json().catch(() => ({}));
    showLaunchError(body.error || "Could not start scan.");
    return;
  }

  pollStatus();
}

function formatElapsed(sec) {
  const m = Math.floor(sec / 60);
  const s = sec % 60;
  return m > 0 ? `${m}m ${s}s` : `${s}s`;
}

function renderProgress(st) {
  if (st.phase === "grouping") {
    scanPhase.textContent = "Comparing photos…";
  } else if (selectedMode() === "similar") {
    scanPhase.textContent = "Scanning & fingerprinting…";
  } else {
    scanPhase.textContent = "Scanning…";
  }

  const bar = progressBar.parentElement;
  if (st.walkDone && st.discovered > 0) {
    // The total is known: show a real percentage.
    bar.classList.remove("indeterminate");
    const pct = Math.min(100, Math.round((st.processed / st.discovered) * 100));
    progressBar.style.width = pct + "%";
  } else {
    // Still discovering files — total unknown, so animate rather than lie.
    bar.classList.add("indeterminate");
  }

  const parts = [];
  parts.push(`${st.discovered.toLocaleString()} images found`);
  parts.push(`${st.processed.toLocaleString()} scanned`);
  if (st.bytes > 0) parts.push(humanBytes(st.bytes));
  parts.push(formatElapsed(st.elapsedSec));
  scanStats.textContent = parts.join(" · ");
}

async function pollStatus() {
  let st;
  try {
    const res = await fetch("/api/scan/status");
    st = await res.json();
  } catch {
    pollTimer = setTimeout(pollStatus, 700);
    return;
  }

  renderProgress(st);

  if (!st.done) {
    pollTimer = setTimeout(pollStatus, 500);
    return;
  }

  // Finished.
  if (st.error) {
    if (cancelledByUser) {
      show("launcher");
    } else {
      showLaunchError("Scan failed: " + st.error);
    }
    return;
  }

  const report = await (await fetch("/api/report")).json();
  render(report);
  show("review");
}

cancelScanBtn.addEventListener("click", async () => {
  cancelledByUser = true;
  scanPhase.textContent = "Cancelling…";
  try {
    await fetch("/api/scan/cancel", { method: "POST" });
  } catch {
    /* the poll will still notice it stopped */
  }
});

startBtn.addEventListener("click", startScan);
folderInput.addEventListener("keydown", (e) => {
  if (e.key === "Enter") startScan();
});
document.getElementById("back").addEventListener("click", () => {
  syncMode();
  show("launcher");
});

// ---- Review ----

function renderStats(s) {
  statsEl.innerHTML = "";
  const items = [
    [`${s.filesScanned}`, "scanned"],
    [`${s.groups}`, "groups"],
    [`${s.exactGroups}`, "exact"],
    [`${s.similarGroups}`, "similar"],
    [`${s.duplicateFiles}`, "duplicates"],
    [humanBytes(s.reclaimableBytes), "reclaimable"],
  ];
  for (const [value, label] of items) {
    const span = document.createElement("span");
    span.innerHTML = `<b>${value}</b> ${label}`;
    statsEl.appendChild(span);
  }
}

function updateToolbar() {
  deleteBtn.disabled = picked.size === 0;
  selectionCount.textContent = picked.size === 0 ? "" : `${picked.size} selected`;
}

function fileCard(file, isKeep, groupType, ordinal) {
  const node = document.getElementById("file-template").content.cloneNode(true);
  const fig = node.querySelector("figure");
  const thumbWrap = node.querySelector(".thumb-wrap");
  const checkbox = node.querySelector(".pick");
  const img = node.querySelector("img");
  const ord = node.querySelector(".ord");
  const badge = node.querySelector(".badge");
  const name = node.querySelector(".name");
  const meta = node.querySelector(".meta");

  ord.textContent = `#${ordinal}`;
  img.src = `/api/thumb?path=${encodeURIComponent(file.path)}`;
  img.alt = file.relPath;
  name.textContent = file.relPath;
  name.title = file.path;
  const dims = file.decoded ? `${file.width}×${file.height}` : "unreadable";
  meta.textContent = `${humanBytes(file.size)} · ${dims} · ${file.modTime}`;

  // Clicking the photo opens the full-size comparison view; it never toggles
  // the delete checkbox, so looking closely can't accidentally select a file.
  const open = () => openLightbox(file);
  thumbWrap.addEventListener("click", open);
  thumbWrap.addEventListener("keydown", (e) => {
    if (e.key === "Enter" || e.key === " ") { e.preventDefault(); open(); }
  });

  if (isKeep) {
    badge.textContent = "KEEP · suggested";
    badge.classList.add("keep");
  } else {
    badge.textContent = "duplicate";
  }

  // Exact duplicates are byte-identical, so pre-ticking the extra copies is
  // safe and saves clicks. "Similar" photos only look alike, so nothing is
  // pre-ticked: the user decides what goes, having compared them.
  const preselect = groupType === "exact" && !isKeep;
  checkbox.checked = preselect;
  if (preselect) {
    picked.add(file.path);
    fig.classList.add("picked");
  }

  checkbox.addEventListener("change", () => {
    if (checkbox.checked) {
      picked.add(file.path);
      fig.classList.add("picked");
    } else {
      picked.delete(file.path);
      fig.classList.remove("picked");
    }
    updateToolbar();
  });

  return node;
}

function render(report) {
  rootEl.textContent = report.root;
  renderStats(report.stats);
  groupsEl.innerHTML = "";
  picked.clear();

  if (!report.groups.length) {
    const p = document.createElement("p");
    p.className = "empty";
    p.textContent = "No duplicates found. 🎉";
    groupsEl.appendChild(p);
    updateToolbar();
    return;
  }

  report.groups.forEach((group, gi) => {
    const node = document.getElementById("group-template").content.cloneNode(true);
    const head = node.querySelector(".group-head");
    const hint = node.querySelector(".group-hint");
    const filesEl = node.querySelector(".files");

    const tag = document.createElement("span");
    tag.className = `tag ${group.type}`;
    tag.textContent = group.type;
    head.appendChild(tag);
    const title = document.createElement("span");
    title.textContent = `Group ${gi + 1} — ${group.files.length} photos`;
    head.appendChild(title);

    hint.textContent = group.type === "exact"
      ? "These files are byte-for-byte identical. The extra copies are pre-ticked; untick any you want to keep."
      : "These photos only look alike. Compare them (click to enlarge) and tick the ones to remove. None are pre-selected.";

    group.files.forEach((file, i) => {
      filesEl.appendChild(fileCard(file, i === group.keepIndex, group.type, i + 1));
    });
    groupsEl.appendChild(node);
  });
  updateToolbar();
}

// ---- Lightbox ----

const lightbox = document.getElementById("lightbox");
const lightboxImg = document.getElementById("lightbox-img");
const lightboxCaption = document.getElementById("lightbox-caption");

function openLightbox(file) {
  const dims = file.decoded ? `${file.width}×${file.height}` : "unreadable";
  lightboxImg.src = `/api/preview?path=${encodeURIComponent(file.path)}`;
  lightboxCaption.textContent = `${file.relPath} — ${humanBytes(file.size)} · ${dims} · ${file.modTime}`;
  lightbox.classList.remove("hidden");
}

function closeLightbox() {
  lightbox.classList.add("hidden");
  lightboxImg.src = "";
}

lightbox.addEventListener("click", closeLightbox);
document.addEventListener("keydown", (e) => {
  if (e.key === "Escape" && !lightbox.classList.contains("hidden")) closeLightbox();
});

deleteBtn.addEventListener("click", async () => {
  if (!picked.size) return;
  const paths = [...picked];
  if (!confirm(`Move ${paths.length} file(s) to the recycle bin?`)) return;

  deleteBtn.disabled = true;
  const res = await fetch("/api/delete", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ paths }),
  });
  const result = await res.json();
  if (result.failed && result.failed.length) {
    alert(`Some files could not be moved:\n${result.failed.join("\n")}`);
  }
  render(result.report);
});

// ---- Add to library (merge) ----

const mergePicked = new Set();
const copyBtn = document.getElementById("copy-selected");
const mergeSelCount = document.getElementById("merge-selection-count");

async function startMerge() {
  const base = baseFolderInput.value.trim();
  const source = sourceFolderInput.value.trim();
  if (!base || !source) {
    showLaunchError("Choose both your library folder and the new-photos folder.");
    return;
  }
  if (base === source) {
    showLaunchError("The library and the new-photos folder must be different.");
    return;
  }

  cancelledByUser = false;
  scanningPath.textContent = source;
  scanPhase.textContent = "Comparing with your library…";
  scanStats.textContent = "";
  progressBar.parentElement.classList.add("indeterminate");
  show("scanning");

  let res;
  try {
    res = await fetch("/api/merge", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ base, source, similar: true, threshold: Number(thresholdInput.value) }),
    });
  } catch (err) {
    showLaunchError("Could not start: " + err);
    return;
  }
  if (!res.ok) {
    const body = await res.json().catch(() => ({}));
    showLaunchError(body.error || "Could not start.");
    return;
  }
  pollMergeStatus();
}

async function pollMergeStatus() {
  let st;
  try {
    st = await (await fetch("/api/merge/status")).json();
  } catch {
    pollTimer = setTimeout(pollMergeStatus, 700);
    return;
  }

  // The total is only known after both folders are walked, so show a moving
  // bar with live counts rather than a misleading percentage.
  progressBar.parentElement.classList.add("indeterminate");
  scanPhase.textContent = "Comparing with your library…";
  const parts = [`${st.processed.toLocaleString()} photos checked`];
  if (st.bytes > 0) parts.push(humanBytes(st.bytes));
  parts.push(formatElapsed(st.elapsedSec));
  scanStats.textContent = parts.join(" · ");

  if (!st.done) {
    pollTimer = setTimeout(pollMergeStatus, 500);
    return;
  }
  if (st.error) {
    if (cancelledByUser) show("launcher");
    else showLaunchError("Comparison failed: " + st.error);
    return;
  }

  const result = await (await fetch("/api/merge/result")).json();
  renderMerge(result);
  show("merge-review");
}

function updateMergeToolbar() {
  copyBtn.disabled = mergePicked.size === 0;
  mergeSelCount.textContent = mergePicked.size === 0 ? "" : `${mergePicked.size} selected`;
}

function mergeCard(file) {
  const node = document.getElementById("file-template").content.cloneNode(true);
  const fig = node.querySelector("figure");
  const thumbWrap = node.querySelector(".thumb-wrap");
  const checkbox = node.querySelector(".pick");
  const img = node.querySelector("img");

  node.querySelector(".ord").style.display = "none";
  node.querySelector(".badge").style.display = "none";
  node.querySelector(".del-text").textContent = "Copy to library";

  img.src = `/api/thumb?path=${encodeURIComponent(file.path)}`;
  img.alt = file.relPath;
  node.querySelector(".name").textContent = file.relPath;
  node.querySelector(".name").title = file.path;
  const dims = file.decoded ? `${file.width}×${file.height}` : "unreadable";
  node.querySelector(".meta").textContent = `${humanBytes(file.size)} · ${dims} · ${file.modTime}`;

  const open = () => openLightbox(file);
  thumbWrap.addEventListener("click", open);
  thumbWrap.addEventListener("keydown", (e) => {
    if (e.key === "Enter" || e.key === " ") { e.preventDefault(); open(); }
  });

  checkbox.checked = true;
  mergePicked.add(file.path);
  fig.classList.add("tocopy");
  checkbox.addEventListener("change", () => {
    if (checkbox.checked) { mergePicked.add(file.path); fig.classList.add("tocopy"); }
    else { mergePicked.delete(file.path); fig.classList.remove("tocopy"); }
    updateMergeToolbar();
  });

  return node;
}

function renderMerge(result) {
  mergePicked.clear();
  document.getElementById("import-subdir").textContent = result.importSubdir || "photocull_added";

  const summary = document.getElementById("merge-summary");
  summary.innerHTML = "";
  const items = [
    [`${result.new.length}`, "new to add"],
    [`${result.duplicates}`, "already in library"],
    [`${result.sourceImages}`, "in source"],
    [`${result.baseImages}`, "in library"],
  ];
  for (const [value, label] of items) {
    const span = document.createElement("span");
    span.innerHTML = `<b>${value}</b> ${label}`;
    summary.appendChild(span);
  }

  const gallery = document.getElementById("merge-gallery");
  gallery.innerHTML = "";
  if (!result.new.length) {
    const p = document.createElement("p");
    p.className = "empty";
    p.textContent = "Nothing new — your library already has all of these. 🎉";
    gallery.appendChild(p);
    updateMergeToolbar();
    return;
  }
  for (const file of result.new) gallery.appendChild(mergeCard(file));
  updateMergeToolbar();
}

copyBtn.addEventListener("click", async () => {
  if (!mergePicked.size) return;
  const paths = [...mergePicked];
  copyBtn.disabled = true;

  const res = await fetch("/api/merge/copy", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ paths }),
  });
  const result = await res.json();
  if (result.error) {
    alert("Could not copy: " + result.error);
    copyBtn.disabled = false;
    return;
  }

  document.getElementById("merge-done-title").textContent =
    `Added ${result.copied} photo${result.copied === 1 ? "" : "s"} to your library`;
  document.getElementById("merge-done-msg").textContent =
    (result.failed && result.failed.length ? `${result.failed.length} could not be copied. ` : "") +
    `Copied into: ${result.dest}`;
  show("merge-done");
});

document.getElementById("merge-back").addEventListener("click", () => { syncMode(); show("launcher"); });
document.getElementById("merge-done-ok").addEventListener("click", () => { syncMode(); show("launcher"); });

// ---- Boot ----
// If the server was started with a folder already (photocull serve <dir>), jump
// straight to the review. Otherwise show the launcher.
(async function boot() {
  syncMode();
  try {
    const res = await fetch("/api/report");
    const report = await res.json();
    if (report.loaded) {
      render(report);
      show("review");
      return;
    }
  } catch {
    /* fall through to launcher */
  }
  show("launcher");
})();
