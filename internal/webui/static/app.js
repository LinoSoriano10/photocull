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

function fileCard(file, group, index) {
  const node = document.getElementById("file-template").content.cloneNode(true);
  const fig = node.querySelector("figure");
  const thumbWrap = node.querySelector(".thumb-wrap");
  const checkbox = node.querySelector(".pick");
  const img = node.querySelector("img");
  const ord = node.querySelector(".ord");
  const badge = node.querySelector(".badge");
  const name = node.querySelector(".name");
  const meta = node.querySelector(".meta");

  const isKeep = index === group.keepIndex;
  const groupType = group.type;

  ord.textContent = `#${index + 1}`;
  img.src = `/api/thumb?path=${encodeURIComponent(file.path)}`;
  img.alt = file.relPath;
  name.textContent = file.relPath;
  name.title = file.path;
  const dims = file.decoded ? `${file.width}×${file.height}` : "unreadable";
  meta.textContent = `${humanBytes(file.size)} · ${dims} · ${file.modTime}`;
  thumbWrap.title = isKeep
    ? "Click to compare against the other copies"
    : "Click to compare against the copy being kept";

  // Clicking the photo opens the comparison view against the copy being kept;
  // it never toggles the delete checkbox, so looking closely can't accidentally
  // select a file.
  const open = () => openViewer(group.files, index, group.keepIndex);
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
      : "These photos only look alike. Click one to compare it against the copy being kept, then tick the ones to remove. None are pre-selected.";

    group.files.forEach((file, i) => {
      filesEl.appendChild(fileCard(file, group, i));
    });
    groupsEl.appendChild(node);
  });
  updateToolbar();
}

// ---- Comparison viewer ----
//
// Two photos a perceptual hash called "similar" are, by definition, hard to
// tell apart. Looking at one and then the other does not work: the eye cannot
// carry that much detail across a glance. So the pair is offered four ways, and
// each earns its place by catching something the others miss — side by side for
// framing and colour, wipe for crops and watermarks, blink for the subtle
// change you cannot name, and the difference map for where to look at all.

const viewer = document.getElementById("viewer");
const viewerModes = document.getElementById("viewer-modes");
const viewerVerdict = document.getElementById("viewer-verdict");
const viewerNote = document.getElementById("viewer-note");
const viewerFoot = document.getElementById("viewer-foot");
const viewerPos = document.getElementById("viewer-pos");
const stackClip = document.getElementById("stack-clip");
const stackFrame = document.getElementById("stack-frame");
const wipeHandle = document.getElementById("wipe-handle");

const panes = {
  side: document.getElementById("stage-side"),
  stack: document.getElementById("stage-stack"),
  heat: document.getElementById("stage-heat"),
  single: document.getElementById("stage-single"),
};

// How long each photo stays up in blink mode. Fast enough that the eye reads a
// change as motion, slow enough to see what the photo actually is.
const BLINK_MS = 600;
const MAX_ZOOM = 8;

let vs = null; // viewer state; null when the viewer is closed

function previewURL(file) {
  return `/api/preview?path=${encodeURIComponent(file.path)}`;
}

function fileCaption(file) {
  const dims = file.decoded ? `${file.width}×${file.height}` : "unreadable";
  return `${file.relPath} — ${humanBytes(file.size)} · ${dims} · ${file.modTime}`;
}

function modeButton(view) {
  return viewerModes.querySelector(`[data-view="${view}"]`);
}

// openSingle shows one photo on its own — the add-to-library gallery has
// nothing to compare against.
function openSingle(file) {
  vs = { view: "single", others: [], at: 0, zoom: identityZoom() };
  document.getElementById("single-img").src = previewURL(file);
  document.getElementById("single-cap").textContent = fileCaption(file);
  viewerModes.classList.add("hidden");
  viewerFoot.classList.add("hidden");
  viewerVerdict.textContent = "";
  viewerNote.classList.add("hidden");
  setPane("single");
  applyZoom();
  viewer.classList.remove("hidden");
}

// openViewer compares one file in a group against the copy being kept, which is
// the only comparison that matters: every decision in the review view is
// "should this go, given that one stays?".
function openViewer(files, index, keepIndex) {
  const others = files.map((_, i) => i).filter((i) => i !== keepIndex);
  if (!others.length || keepIndex < 0 || keepIndex >= files.length) {
    openSingle(files[index]);
    return;
  }

  // Clicking the keeper itself has no pair, so start at the first duplicate.
  const wanted = index === keepIndex ? others[0] : index;
  vs = {
    files,
    keepIndex,
    others,
    at: Math.max(0, others.indexOf(wanted)),
    view: "side",
    wipe: 0.5,
    zoom: identityZoom(),
    blink: null,
  };

  viewerModes.classList.remove("hidden");
  viewerFoot.classList.toggle("hidden", others.length < 2);
  setView("side");
  loadPair();
  viewer.classList.remove("hidden");
}

async function loadPair() {
  const a = vs.files[vs.keepIndex];
  const b = vs.files[vs.others[vs.at]];
  const urlA = previewURL(a);
  const urlB = previewURL(b);

  document.getElementById("side-a").src = urlA;
  document.getElementById("side-b").src = urlB;
  document.getElementById("stack-a").src = urlA;
  document.getElementById("stack-b").src = urlB;
  document.getElementById("side-a-cap").textContent = `KEEP · ${fileCaption(a)}`;
  document.getElementById("side-b-cap").textContent = fileCaption(b);
  document.getElementById("stack-cap").textContent =
    `Keeping "${a.relPath}" underneath · "${b.relPath}" on top`;
  document.getElementById("heat-img").src =
    `/api/imagediff?a=${encodeURIComponent(a.path)}&b=${encodeURIComponent(b.path)}`;
  document.getElementById("heat-cap").textContent =
    `Red marks where "${b.relPath}" differs from the copy being kept.`;
  viewerPos.textContent = `Copy ${vs.at + 1} of ${vs.others.length}`;

  resetZoom();
  viewerVerdict.textContent = "comparing…";
  viewerNote.classList.add("hidden");

  try {
    const res = await fetch(`/api/compare?a=${encodeURIComponent(a.path)}&b=${encodeURIComponent(b.path)}`);
    if (!res.ok) throw new Error(String(res.status));
    renderVerdict(await res.json());
  } catch {
    // The photos are still there to look at; only the summary is missing.
    viewerVerdict.textContent = "";
  }
}

function renderVerdict(cmp) {
  const heatBtn = modeButton("heat");

  if (cmp.note) {
    viewerVerdict.textContent = "";
    viewerNote.textContent = cmp.note;
    viewerNote.classList.remove("hidden");
    // Without a pixel comparison there is no map to draw.
    heatBtn.disabled = true;
    if (vs.view === "heat") setView("side");
    return;
  }

  heatBtn.disabled = false;
  if (!cmp.image) {
    viewerVerdict.textContent = "";
    return;
  }

  const pct = cmp.image.ratio * 100;
  let text;
  if (pct === 0) text = "Every pixel matches.";
  else if (pct < 0.05) text = "Differ in under 0.05% of pixels.";
  else text = `Differ in ${pct < 1 ? pct.toFixed(2) : pct.toFixed(1)}% of pixels.`;

  if (cmp.image.box && cmp.image.width && cmp.image.height) {
    text += ` ${whereText(cmp.image.box, cmp.image.width, cmp.image.height)}`;
  }
  viewerVerdict.textContent = text;
}

// whereText turns the bounding box into words. "0.4% of pixels" is a puzzle;
// "0.4%, bottom right" is a decision — it tells you where to point the zoom.
function whereText(box, w, h) {
  if ((box.w * box.h) / (w * h) > 0.6) return "Spread across the whole frame.";

  const cx = (box.x + box.w / 2) / w;
  const cy = (box.y + box.h / 2) / h;
  const vert = cy < 0.34 ? "top" : cy > 0.66 ? "bottom" : "middle";
  const horiz = cx < 0.34 ? "left" : cx > 0.66 ? "right" : "centre";
  if (vert === "middle" && horiz === "centre") return "Concentrated in the centre.";
  if (vert === "middle") return `Concentrated on the ${horiz}.`;
  if (horiz === "centre") return `Concentrated at the ${vert}.`;
  return `Concentrated ${vert} ${horiz}.`;
}

function setPane(view) {
  const wanted = view === "wipe" || view === "blink" ? "stack" : view;
  for (const [name, el] of Object.entries(panes)) {
    el.classList.toggle("hidden", name !== wanted);
  }
  for (const btn of viewerModes.querySelectorAll(".vmode")) {
    btn.classList.toggle("active", btn.dataset.view === view);
  }
}

function setView(view) {
  if (!vs) return;
  stopBlink();
  vs.view = view;
  setPane(view);

  wipeHandle.classList.toggle("hidden", view !== "wipe");
  if (view === "wipe") applyWipe();
  else stackClip.style.clipPath = "none";
  if (view === "blink") startBlink();

  applyZoom();
}

function applyWipe() {
  stackClip.style.clipPath = `inset(0 ${((1 - vs.wipe) * 100).toFixed(2)}% 0 0)`;
  wipeHandle.style.left = `${(vs.wipe * 100).toFixed(2)}%`;
}

function startBlink() {
  let showing = true;
  vs.blink = setInterval(() => {
    showing = !showing;
    stackClip.style.opacity = showing ? "1" : "0";
  }, BLINK_MS);
}

function stopBlink() {
  if (vs && vs.blink) {
    clearInterval(vs.blink);
    vs.blink = null;
  }
  stackClip.style.opacity = "1";
}

// ---- Zoom and pan, shared by every pane ----
//
// One transform drives every visible image. That is the whole point: two photos
// panned independently are two photos you cannot compare.

function identityZoom() {
  return { scale: 1, x: 0, y: 0 };
}

function applyZoom() {
  if (!vs) return;
  const { scale, x, y } = vs.zoom;
  const transform = `translate(${x}px, ${y}px) scale(${scale})`;
  for (const img of viewer.querySelectorAll(".zoomable")) {
    img.style.transform = transform;
  }
}

function resetZoom() {
  if (!vs) return;
  vs.zoom = identityZoom();
  applyZoom();
}

viewer.addEventListener("wheel", (e) => {
  if (!vs || !e.target.closest(".frame")) return;
  e.preventDefault();

  const rect = e.target.closest(".frame").getBoundingClientRect();
  // Zoom about the cursor, so whatever detail is under it stays under it.
  const cx = e.clientX - rect.left - rect.width / 2;
  const cy = e.clientY - rect.top - rect.height / 2;

  const before = vs.zoom.scale;
  const after = Math.min(MAX_ZOOM, Math.max(1, before * (e.deltaY < 0 ? 1.15 : 1 / 1.15)));
  const k = after / before;

  vs.zoom.scale = after;
  vs.zoom.x = (vs.zoom.x - cx) * k + cx;
  vs.zoom.y = (vs.zoom.y - cy) * k + cy;
  if (after === 1) { vs.zoom.x = 0; vs.zoom.y = 0; }
  applyZoom();
}, { passive: false });

// Dragging pans, except in wipe mode where it moves the divider instead —
// a comparison slider you have to grab by a 6px handle is a bad slider.
let drag = null;

viewer.addEventListener("pointerdown", (e) => {
  if (!vs) return;
  const frame = e.target.closest(".frame");
  if (!frame) return;

  if (vs.view === "wipe") {
    e.preventDefault();
    drag = { kind: "wipe" };
    vs.wipe = wipeFromEvent(e);
    applyWipe();
  } else if (vs.zoom.scale > 1) {
    e.preventDefault();
    drag = { kind: "pan", x: e.clientX, y: e.clientY };
  }
  if (drag) frame.setPointerCapture(e.pointerId);
});

viewer.addEventListener("pointermove", (e) => {
  if (!drag || !vs) return;
  if (drag.kind === "wipe") {
    vs.wipe = wipeFromEvent(e);
    applyWipe();
    return;
  }
  vs.zoom.x += e.clientX - drag.x;
  vs.zoom.y += e.clientY - drag.y;
  drag.x = e.clientX;
  drag.y = e.clientY;
  applyZoom();
});

viewer.addEventListener("pointerup", () => { drag = null; });
viewer.addEventListener("pointercancel", () => { drag = null; });

function wipeFromEvent(e) {
  const rect = stackFrame.getBoundingClientRect();
  return Math.min(1, Math.max(0, (e.clientX - rect.left) / rect.width));
}

// ---- Viewer navigation ----

function stepCopy(delta) {
  if (!vs || vs.others.length < 2) return;
  vs.at = (vs.at + delta + vs.others.length) % vs.others.length;
  loadPair();
}

function closeViewer() {
  stopBlink();
  viewer.classList.add("hidden");
  for (const img of viewer.querySelectorAll("img")) img.src = "";
  vs = null;
  drag = null;
}

viewerModes.addEventListener("click", (e) => {
  const btn = e.target.closest(".vmode");
  if (btn && !btn.disabled) setView(btn.dataset.view);
});
document.getElementById("viewer-close").addEventListener("click", closeViewer);
document.getElementById("viewer-prev").addEventListener("click", () => stepCopy(-1));
document.getElementById("viewer-next").addEventListener("click", () => stepCopy(1));

// Clicking the backdrop closes, but clicking the photos must not: the viewer is
// something you work inside now, not a picture you dismiss.
viewer.addEventListener("click", (e) => {
  if (e.target === viewer) closeViewer();
});

document.addEventListener("keydown", (e) => {
  if (!vs) return;
  if (e.key === "Escape") { closeViewer(); return; }
  if (vs.view === "single") return;

  switch (e.key) {
    case "ArrowLeft": stepCopy(-1); break;
    case "ArrowRight": stepCopy(1); break;
    case "1": setView("side"); break;
    case "2": setView("wipe"); break;
    case "3": setView("blink"); break;
    case "4": if (!modeButton("heat").disabled) setView("heat"); break;
    case "0": resetZoom(); break;
    default: return;
  }
  e.preventDefault();
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

  // Nothing to compare against here: these photos are the ones the library does
  // not have, so there is no counterpart to put beside them.
  const open = () => openSingle(file);
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
