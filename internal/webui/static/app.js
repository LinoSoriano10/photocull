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

// ---- What is being deduplicated ----
//
// Photos and documents differ in wording, in how a card looks, and in how
// strict "similar" should be, but in nothing else: the same scan, the same
// groups, the same delete button. Everything that does differ is gathered here
// rather than scattered as `kind === "docs"` tests through the file.

const KINDS = {
  photos: {
    nouns: "photos",
    tagline: "Find and remove duplicate photos — safely. Nothing is ever deleted permanently.",
    similarHint: "Also resized, re-compressed or re-encoded copies (incl. HEIC). Review by eye.",
    mergeTitle: "Add new photos to a library",
    mergeHint: "Copy into a library only the photos from another folder that it doesn't already have.",
    sourceTitle: "New photos to bring in",
    // The photo threshold reaches as far as 16 because a re-encoded JPEG can
    // sit that far from its original and still be the same picture.
    slider: { min: 2, max: 16, value: 8 },
    reviewHint:
      "Each group below is one set of photos that look alike. Click any photo to " +
      "compare it against the copy being kept — side by side, with a wipe, blinking " +
      "between the two, or with the differences highlighted — then tick the ones to " +
      "remove. Nothing is deleted until you press the button above, and even then it " +
      "only goes to the recycle bin.",
    mergeReviewHint:
      "These photos are not in your library. Click any to see it full size, tick the " +
      "ones to add, and press the button.",
  },
  docs: {
    nouns: "documents",
    tagline: "Find and remove duplicate documents — safely. Nothing is ever deleted permanently.",
    similarHint: "Also drafts, re-saved copies and the same text exported to another format. Review by eye.",
    mergeTitle: "Add new documents to a folder",
    mergeHint: "Copy into a folder only the documents from elsewhere that it doesn't already have.",
    sourceTitle: "New documents to bring in",
    // Documents stop at 12: past that the distance is reaching the band where
    // two unrelated files from one template look alike, and a wrong delete
    // there costs a real document rather than one of several copies of a photo.
    slider: { min: 2, max: 12, value: 6 },
    reviewHint:
      "Each group below is one set of documents that say much the same thing. The " +
      "opening lines of each are shown so you can tell them apart. Click one to see " +
      "exactly which words differ from the copy being kept, then tick the ones to " +
      "remove. Nothing is deleted until you press the button above, and even then it " +
      "only goes to the recycle bin.",
    mergeReviewHint:
      "These documents are not in the destination folder. Tick the ones to add and " +
      "press the button.",
  },
};

function kindInfo(kind) {
  return KINDS[kind] || KINDS.photos;
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

function selectedKind() {
  return document.querySelector('input[name="kind"]:checked').value;
}

// syncMode reshapes the launcher for the chosen mode: one folder for the two
// duplicate-finding modes, two folders (library + source) for "add to library",
// and a threshold slider whenever similarity matching is in play.
function syncMode() {
  const mode = selectedMode();
  const merge = mode === "merge";
  const k = kindInfo(selectedKind());
  thresholdRow.classList.toggle("hidden", mode === "exact");
  singleFolder.classList.toggle("hidden", merge);
  doubleFolder.classList.toggle("hidden", !merge);
  startBtn.textContent = merge ? `Find new ${k.nouns}` : "Scan for duplicates";
}

// syncKind rewords the launcher and retunes the slider for the chosen kind.
//
// The slider is reset rather than kept, and that is on purpose: 8 is a sensible
// photo threshold and a reckless document one, so carrying the number across
// would silently apply the wrong policy to whichever kind was chosen second.
function syncKind() {
  const kind = selectedKind();
  const k = kindInfo(kind);

  document.getElementById("tagline").textContent = k.tagline;
  document.getElementById("mode-similar-hint").textContent = k.similarHint;
  document.getElementById("mode-merge-title").textContent = k.mergeTitle;
  document.getElementById("mode-merge-hint").textContent = k.mergeHint;
  document.getElementById("source-folder-title").textContent = k.sourceTitle;

  thresholdInput.min = k.slider.min;
  thresholdInput.max = k.slider.max;
  thresholdInput.value = k.slider.value;
  thresholdVal.textContent = thresholdInput.value;

  // Documents open on "similar": two saves of one file are almost never
  // byte-identical — the ZIP inside a .docx carries timestamps and revision
  // ids — so exact-only would report nothing and look broken.
  if (kind === "docs" && selectedMode() === "exact") {
    document.querySelector('input[name="mode"][value="similar"]').checked = true;
  }
  syncMode();
}

document.querySelectorAll('input[name="mode"]').forEach((r) =>
  r.addEventListener("change", syncMode)
);
document.querySelectorAll('input[name="kind"]').forEach((r) =>
  r.addEventListener("change", syncKind)
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
        kind: selectedKind(),
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
  const k = kindInfo(selectedKind());
  if (st.phase === "grouping") {
    scanPhase.textContent = `Comparing ${k.nouns}…`;
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
  parts.push(`${st.discovered.toLocaleString()} files found`);
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

// reviewKind is what the loaded report is about, and it comes from the report
// itself rather than from the radio button. `photocull serve D:\Docs --kind
// docs` opens straight into this view without the launcher ever being shown,
// so reading the radio would render a folder of documents as photographs.
let reviewKind = "photos";

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

  // The related tier gets its own tiles instead of being folded into the two
  // above. Those figures are what photocull is sure of; these are what a person
  // still has to check, and adding the second to the first would make the
  // headline claim more than the tool actually knows.
  if (s.relatedGroups > 0) {
    items.push([`${s.relatedGroups}`, "to review"]);
    items.push([humanBytes(s.relatedBytes), "if they match"]);
  }

  for (const [value, label] of items) {
    const span = document.createElement("span");
    span.innerHTML = `<b>${value}</b> ${label}`;
    statsEl.appendChild(span);
  }
}

function updateToolbar() {
  deleteBtn.disabled = picked.size === 0;
  deleteBtn.textContent = `Move ticked ${kindInfo(reviewKind).nouns} to recycle bin`;
  selectionCount.textContent = picked.size === 0 ? "" : `${picked.size} selected`;
}

// detailLine is the line under a card: what the file is, in the terms that
// matter for the kind being reviewed.
function detailLine(file, kind) {
  if (kind === "docs") {
    return `${humanBytes(file.size)} · ${file.decoded ? "text" : "no readable text"} · ${file.modTime}`;
  }
  const dims = file.decoded ? `${file.width}×${file.height}` : "unreadable";
  return `${humanBytes(file.size)} · ${dims} · ${file.modTime}`;
}

// fillSnippet drops the opening lines of a document into its card.
//
// It is fetched per card rather than sent with the report because the report is
// one JSON document covering every group, and a few hundred characters times a
// few thousand files would make it enormous to build and slow to parse — for
// text most of which is never scrolled to.
async function fillSnippet(el, file, groupType) {
  try {
    const res = await fetch(`/api/snippet?path=${encodeURIComponent(file.path)}&n=1200`);
    if (!res.ok) throw new Error(String(res.status));
    const text = (await res.text()).trim();
    if (!text) throw new Error("empty");
    el.textContent = text;
    return;
  } catch {
    // Not an error worth an error message: a .zip, a scanned PDF or a legacy
    // .doc has nothing to show, and saying what photocull matched it on instead
    // is the useful thing to put here.
    el.textContent = groupType === "related"
      ? "No readable text inside. These files were matched on their names and sizes alone — open them to check."
      : "No readable text inside. These files were matched on their contents, byte for byte.";
    el.classList.add("none");
  }
}

function fileCard(file, group, index) {
  const node = document.getElementById("file-template").content.cloneNode(true);
  const fig = node.querySelector("figure");
  const thumbWrap = node.querySelector(".thumb-wrap");
  const checkbox = node.querySelector(".pick");
  const img = node.querySelector("img");
  const snippet = node.querySelector(".snippet");
  const ord = node.querySelector(".ord");
  const badge = node.querySelector(".badge");
  const name = node.querySelector(".name");
  const meta = node.querySelector(".meta");

  const isKeep = index === group.keepIndex;
  const groupType = group.type;
  const isDoc = reviewKind === "docs";

  ord.textContent = `#${index + 1}`;
  name.textContent = file.relPath;
  name.title = file.path;
  meta.textContent = detailLine(file, reviewKind);

  // Clicking a card opens the comparison against the copy being kept, which is
  // the only comparison that matters: every decision here is "should this go,
  // given that one stays?". It never toggles the delete checkbox, so looking
  // closely can't accidentally select a file.
  const open = () => {
    // A click that ends a text selection is somebody finishing a drag inside
    // the snippet, not asking for the comparison.
    if (isDoc && !window.getSelection().isCollapsed) return;
    openViewer(group.files, index, group.keepIndex, reviewKind);
  };
  thumbWrap.title = isKeep
    ? "Click to compare against the other copies"
    : "Click to compare against the copy being kept";
  thumbWrap.addEventListener("click", open);
  thumbWrap.addEventListener("keydown", (e) => {
    if (e.key === "Enter" || e.key === " ") { e.preventDefault(); open(); }
  });

  if (isDoc) {
    // A document has nothing to look at, so the card shows what it says.
    fig.classList.add("doc");
    img.classList.add("hidden");
    node.querySelector(".zoom").textContent = "⇄";
    snippet.classList.remove("hidden");
    snippet.textContent = "reading…";
    fillSnippet(snippet, file, groupType);
  } else {
    img.src = `/api/thumb?path=${encodeURIComponent(file.path)}`;
    img.alt = file.relPath;
  }

  if (isKeep) {
    badge.textContent = "KEEP · suggested";
    badge.classList.add("keep");
  } else {
    // A related group is a guess, so its members are not called duplicates.
    // The badge is the one word most likely to be read, and calling a guess a
    // duplicate is how somebody ticks all of them without looking.
    badge.textContent = groupType === "related" ? "possible copy" : "duplicate";
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

// groupHint explains, per tier, what photocull knows and what it is asking of
// the user. The three sentences differ in confidence, and that difference is
// the whole point of having three tiers at all.
function groupHint(type, kind) {
  if (type === "exact") {
    return "These files are byte-for-byte identical. The extra copies are pre-ticked; untick any you want to keep.";
  }
  if (type === "related") {
    return "photocull could not read inside these files, so this is a guess from their names and sizes only — " +
      "they may well be different things. Nothing is pre-ticked and nothing here is counted as reclaimable. " +
      "Open them before deciding.";
  }
  return kind === "docs"
    ? "These documents say much the same thing, but they are not identical — one may be a later draft. " +
      "Read the snippets, then tick the ones to remove. None are pre-selected."
    : "These photos only look alike. Click one to compare it against the copy being kept, then tick the ones " +
      "to remove. None are pre-selected.";
}

function render(report) {
  // Kind first: every helper below asks what is being reviewed.
  reviewKind = report.stats.kind || "photos";
  const k = kindInfo(reviewKind);

  rootEl.textContent = report.root;
  renderStats(report.stats);
  document.getElementById("review-hint").textContent = k.reviewHint;
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
    title.textContent = `Group ${gi + 1} — ${group.files.length} ${k.nouns}`;
    head.appendChild(title);

    hint.textContent = groupHint(group.type, reviewKind);
    filesEl.classList.toggle("docs", reviewKind === "docs");

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
  doc: document.getElementById("stage-doc"),
  opaque: document.getElementById("stage-opaque"),
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
  vs = { view: "single", kind: "photos", others: [], at: 0, zoom: identityZoom() };
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
function openViewer(files, index, keepIndex, kind) {
  const isDoc = kind === "docs";
  const others = files.map((_, i) => i).filter((i) => i !== keepIndex);
  if (!others.length || keepIndex < 0 || keepIndex >= files.length) {
    // A document on its own has nothing to lay beside it, and its text is
    // already on the card, so there is no view worth opening.
    if (isDoc) return;
    openSingle(files[index]);
    return;
  }

  // Clicking the keeper itself has no pair, so start at the first duplicate.
  const wanted = index === keepIndex ? others[0] : index;
  vs = {
    files,
    keepIndex,
    others,
    kind,
    at: Math.max(0, others.indexOf(wanted)),
    view: isDoc ? "doc" : "side",
    wipe: 0.5,
    zoom: identityZoom(),
    blink: null,
  };

  // The four ways of looking at a pair of photographs mean nothing for text,
  // so the bar loses them rather than offering four buttons that all show the
  // same thing.
  viewerModes.classList.toggle("hidden", isDoc);
  viewerFoot.classList.toggle("hidden", others.length < 2);
  if (isDoc) setPane("doc");
  else setView("side");
  loadPair();
  viewer.classList.remove("hidden");
}

// loadSeq orders the responses, not the requests. Stepping quickly through a
// group starts several comparisons, and a document comparison is slow enough
// (two file reads and a diff) that an earlier one can land after a later one
// and render the wrong pair over the right one.
let loadSeq = 0;

function loadPair() {
  const a = vs.files[vs.keepIndex];
  const b = vs.files[vs.others[vs.at]];

  viewerPos.textContent = `Copy ${vs.at + 1} of ${vs.others.length}`;
  viewerVerdict.textContent = "comparing…";
  viewerNote.classList.add("hidden");

  if (vs.kind === "docs") loadDocPair(a, b);
  else loadPhotoPair(a, b);
}

async function loadPhotoPair(a, b) {
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

  resetZoom();

  const mine = ++loadSeq;
  const cmp = await compare(a, b);
  if (mine !== loadSeq || !vs) return;
  if (!cmp) {
    // The photos are still there to look at; only the summary is missing.
    viewerVerdict.textContent = "";
    return;
  }
  renderVerdict(cmp);
}

async function compare(a, b) {
  try {
    const res = await fetch(`/api/compare?a=${encodeURIComponent(a.path)}&b=${encodeURIComponent(b.path)}`);
    if (!res.ok) throw new Error(String(res.status));
    return await res.json();
  } catch {
    return null;
  }
}

// ---- Document comparison ----

const docDiffEl = document.getElementById("doc-diff");

async function loadDocPair(a, b) {
  document.getElementById("doc-a-cap").textContent = `KEEP · ${fileCaption(a)}`;
  document.getElementById("doc-b-cap").textContent = fileCaption(b);
  docDiffEl.textContent = "Reading both documents…";
  setPane("doc");
  vs.view = "doc";

  const mine = ++loadSeq;
  const cmp = await compare(a, b);
  if (mine !== loadSeq || !vs) return;

  if (!cmp) {
    viewerVerdict.textContent = "";
    docDiffEl.textContent = "These two documents could not be compared.";
    return;
  }
  if (cmp.kind === "opaque") {
    renderOpaque(cmp, a, b);
    return;
  }
  setPane("doc");
  vs.view = "doc";
  renderDocDiff(cmp.doc);
}

function renderDocDiff(d) {
  docDiffEl.textContent = "";
  const total = d.sameWords + d.changedWords;

  if (!d.hunks.length) {
    const p = document.createElement("p");
    p.className = "doc-identical";
    p.textContent = total === 0
      ? "Neither file yielded any text to compare."
      : `The text is identical — all ${total.toLocaleString()} words match.`;
    docDiffEl.appendChild(p);
    viewerVerdict.textContent = total === 0 ? "" : "No differences in the text.";
    return;
  }

  for (const h of d.hunks) {
    if (h.skippedWords) docDiffEl.appendChild(skipMarker(h.skippedWords, h.skipped));
    docDiffEl.appendChild(hunkParagraph(h));
  }
  if (d.trailingWords) docDiffEl.appendChild(skipMarker(d.trailingWords, d.trailing));

  const pct = total ? (d.changedWords / total) * 100 : 0;
  const n = d.hunks.length;
  viewerVerdict.textContent =
    `${n} change${n === 1 ? "" : "s"} · ${d.changedWords.toLocaleString()} of ` +
    `${total.toLocaleString()} words differ (${pct < 1 ? pct.toFixed(1) : Math.round(pct)}%)` +
    (d.truncated ? " · only the first part of these documents was compared" : "");
}

// hunkParagraph renders one change with the words around it.
//
// Every piece goes in as text, never as markup. This is the contents of a file
// off the user's disk, and a document containing a <script> tag must render as
// a document containing a <script> tag.
function hunkParagraph(h) {
  const p = document.createElement("p");
  if (h.before) p.append(h.before + " ");
  if (h.del) {
    const el = document.createElement("del");
    el.textContent = h.del;
    p.append(el, " ");
  }
  if (h.ins) {
    const el = document.createElement("ins");
    el.textContent = h.ins;
    p.append(el, " ");
  }
  if (h.after) p.append(h.after);
  return p;
}

// skipMarker stands in for a stretch of text that did not change. It opens,
// because "what did I just skip past?" is a fair question when the answer
// decides whether a file goes in the bin.
function skipMarker(count, text) {
  const btn = document.createElement("button");
  btn.type = "button";
  btn.className = "skip";
  btn.textContent = `··· ${count.toLocaleString()} identical words ···`;
  btn.title = "Click to read them";
  btn.addEventListener("click", () => {
    const p = document.createElement("p");
    p.className = "skip-text";
    p.textContent = text;
    btn.replaceWith(p);
  });
  return btn;
}

// renderOpaque is the panel for the related tier: two files photocull could not
// read inside, matched on their names and sizes alone. There is no diff to
// show, and inventing one would be worse than saying so — seeing the same byte
// count under two different paths usually settles it on its own.
function renderOpaque(cmp, a, b) {
  setPane("opaque");
  vs.view = "opaque";
  document.getElementById("opaque-note").textContent = cmp.note || "";
  fillOpaque(document.getElementById("opaque-a"), a, b, cmp.reasonA, true);
  fillOpaque(document.getElementById("opaque-b"), b, a, cmp.reasonB, false);

  viewerVerdict.textContent = a.size === b.size
    ? "Identical in size, to the byte."
    : `Sizes differ by ${humanBytes(Math.abs(a.size - b.size))}.`;
}

function fillOpaque(dl, file, other, reason, isKeep) {
  dl.textContent = "";
  const rows = [
    ["File", (isKeep ? "KEEP · " : "") + file.relPath, ""],
    ["Full path", file.path, ""],
    ["Size", `${file.size.toLocaleString()} bytes`, file.size === other.size ? "same" : "differs"],
    ["Modified", file.modTime, file.modTime === other.modTime ? "same" : "differs"],
    ["Text", reason || "readable", ""],
  ];
  for (const [label, value, cls] of rows) {
    const dt = document.createElement("dt");
    dt.textContent = label;
    const dd = document.createElement("dd");
    dd.textContent = value;
    if (cls) dd.classList.add(cls);
    dl.append(dt, dd);
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
  // A diff carries the collapsed runs with it, which for two long documents is
  // most of both of them; there is no reason to hold that while it is not on
  // screen.
  docDiffEl.textContent = "";
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

  // Documents have one view, so only the arrows mean anything; 1-4 would switch
  // to panes that hold photographs.
  if (vs.kind === "docs") {
    if (e.key === "ArrowLeft") stepCopy(-1);
    else if (e.key === "ArrowRight") stepCopy(1);
    else return;
    e.preventDefault();
    return;
  }

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
      body: JSON.stringify({
        base,
        source,
        kind: selectedKind(),
        similar: true,
        threshold: Number(thresholdInput.value),
      }),
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
  const parts = [`${st.processed.toLocaleString()} files checked`];
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

// mergeKind is what the last comparison was about, read from its result for the
// same reason reviewKind is.
let mergeKind = "photos";

function updateMergeToolbar() {
  copyBtn.disabled = mergePicked.size === 0;
  copyBtn.textContent = `Copy ticked ${kindInfo(mergeKind).nouns} into library`;
  mergeSelCount.textContent = mergePicked.size === 0 ? "" : `${mergePicked.size} selected`;
}

function mergeCard(file) {
  const node = document.getElementById("file-template").content.cloneNode(true);
  const fig = node.querySelector("figure");
  const thumbWrap = node.querySelector(".thumb-wrap");
  const checkbox = node.querySelector(".pick");
  const img = node.querySelector("img");
  const snippet = node.querySelector(".snippet");

  node.querySelector(".ord").style.display = "none";
  node.querySelector(".badge").style.display = "none";
  node.querySelector(".del-text").textContent = "Copy to library";

  node.querySelector(".name").textContent = file.relPath;
  node.querySelector(".name").title = file.path;
  node.querySelector(".meta").textContent = detailLine(file, mergeKind);

  if (mergeKind === "docs") {
    fig.classList.add("doc");
    img.classList.add("hidden");
    node.querySelector(".zoom").classList.add("hidden");
    snippet.classList.remove("hidden");
    thumbWrap.removeAttribute("role");
    thumbWrap.removeAttribute("tabindex");
    thumbWrap.title = "";
    snippet.textContent = "reading…";
    // No group here, so nothing was matched: the fallback wording that fits is
    // the content one.
    fillSnippet(snippet, file, "exact");
  } else {
    img.src = `/api/thumb?path=${encodeURIComponent(file.path)}`;
    img.alt = file.relPath;

    // Nothing to compare against here: these photos are the ones the library
    // does not have, so there is no counterpart to put beside them.
    const open = () => openSingle(file);
    thumbWrap.addEventListener("click", open);
    thumbWrap.addEventListener("keydown", (e) => {
      if (e.key === "Enter" || e.key === " ") { e.preventDefault(); open(); }
    });
  }

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
  mergeKind = result.kind || "photos";
  const k = kindInfo(mergeKind);

  document.getElementById("import-subdir").textContent = result.importSubdir || "photocull_added";
  document.getElementById("merge-hint").textContent = k.mergeReviewHint;

  const summary = document.getElementById("merge-summary");
  summary.innerHTML = "";
  const items = [
    [`${result.new.length}`, "new to add"],
    [`${result.duplicates}`, "already in library"],
    [`${result.sourceFiles}`, "in source"],
    [`${result.baseFiles}`, "in library"],
  ];
  for (const [value, label] of items) {
    const span = document.createElement("span");
    span.innerHTML = `<b>${value}</b> ${label}`;
    summary.appendChild(span);
  }

  const gallery = document.getElementById("merge-gallery");
  gallery.innerHTML = "";
  gallery.classList.toggle("docs", mergeKind === "docs");
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
  syncKind();
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
