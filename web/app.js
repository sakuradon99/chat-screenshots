const get = (id) => document.getElementById(id);
const groupList = get("groups");
const picker = get("group-picker");
const imageList = get("images");
const widthInput = get("width");
const fileInput = get("file-input");
const exportButton = get("export");
const sortImages = get("sort-images");
const imageNameOrder = new Intl.Collator("zh-CN", { numeric: true, sensitivity: "base" });
const maxExportPixels = 500_000_000;
const maxUploadBytes = 32 * 1024 * 1024;

let groups = [];
let selected = localStorage.getItem("selected-group");
let revision = -1;
let editID = null;
let moveContext = null;
let dragging = null;
let upload = null;
let preview = null;
let toastTimer;
let pendingOrder = false;
let cropGroup = null;
let cropStart = null;
const cropInputs = Object.fromEntries(["x", "y", "width", "height"].map((key) => [key, get("crop-" + key)]));
widthInput.value = localStorage.getItem("export-width") || "1080";

function element(tag, className, text) {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (text !== undefined) node.textContent = text;
  return node;
}

function icon(name) {
  const ns = "http://www.w3.org/2000/svg";
  const svg = document.createElementNS(ns, "svg");
  const use = document.createElementNS(ns, "use");
  svg.setAttribute("class", "icon");
  svg.setAttribute("aria-hidden", "true");
  use.setAttribute("href", "#i-" + name);
  svg.append(use);
  return svg;
}

function iconButton(name, label, handler) {
  const button = element("button", "icon-button");
  button.type = "button";
  button.title = label;
  button.setAttribute("aria-label", label);
  button.append(icon(name));
  button.addEventListener("click", handler);
  return button;
}

function activeGroup() {
  return groups.find((group) => group.id === selected);
}

function groupPath(id) {
  return "/api/groups/" + id;
}

function imagePath(group, image, cropped = false) {
  const path = groupPath(group.id) + "/images/" + image.id;
  if (!cropped || !group.crop) return path;
  const { x, y, width, height } = group.crop;
  return path + "?crop=1&v=" + [x, y, width, height].join("-");
}

async function api(path, options = {}) {
  const headers = new Headers(options.headers);
  if (options.method && options.method !== "GET") headers.set("X-Chat-Screenshots", "1");
  const response = await fetch(path, { ...options, headers });
  if (!response.ok && response.status !== 304) {
    const body = await response.json().catch(() => null);
    throw new Error(body?.error || "请求失败，请重试。");
  }
  return response;
}

function send(path, method, data) {
  return api(path, {
    method,
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(data),
  });
}

function notify(message) {
  const toast = get("toast");
  (document.querySelector("dialog[open]") || document.body).append(toast);
  toast.textContent = message;
  toast.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { toast.hidden = true; }, 6000);
}

function syncStatus(state, label) {
  get("sync").dataset.state = state;
  get("sync-text").textContent = label;
}

async function refresh(force = false) {
  try {
    const headers = force || revision < 0 ? {} : { "If-None-Match": '"' + revision + '"' };
    const response = await api("/api/groups", { headers, cache: "no-cache" });
    if (response.status !== 304) {
      const snapshot = await response.json();
      if (snapshot.revision > revision || (force && snapshot.revision === revision)) {
        revision = snapshot.revision;
        groups = snapshot.groups;
        if (!activeGroup()) selected = groups.at(-1)?.id || null;
        if (!dragging) render();
      }
    }
    syncStatus("connected", "已同步");
  } catch {
    syncStatus("offline", "连接中断");
    if (force) throw new Error("无法连接截图服务，请确认服务仍在运行。");
  }
}

function choose(id) {
  selected = id;
  localStorage.setItem("selected-group", id);
  render();
}

function render() {
  const group = activeGroup();
  groupList.replaceChildren();
  picker.replaceChildren();
  for (const entry of [...groups].reverse()) {
    const button = element("button", "group-link");
    button.type = "button";
    if (entry.id === selected) button.setAttribute("aria-current", "true");
    const copy = element("span");
    copy.append(element("strong", "", entry.name), element("small", "", entry.date));
    button.append(icon("folder"), copy, element("span", "", entry.images.length));
    button.addEventListener("click", () => choose(entry.id));
    groupList.append(button);

    const option = element("option", "", entry.name + " · " + entry.date);
    option.value = entry.id;
    picker.append(option);
  }
  picker.hidden = groups.length === 0;
  if (group) picker.value = group.id;
  get("no-group").hidden = Boolean(group);
  get("workspace").hidden = !group;
  if (preview) renderPreview();
  if (!group) return;

  get("group-title").textContent = group.name;
  get("group-date").textContent = group.date;
  get("image-count").textContent = group.images.length + " 张";
  const eligible = canCrop(group);
  const cropButton = get("crop-button");
  cropButton.disabled = !eligible;
  cropButton.querySelector("span").textContent = group.crop ? "调整裁切" : "批量裁切";
  const cropStatus = get("crop-status");
  cropStatus.hidden = !group.crop && (!group.images.length || eligible);
  cropStatus.textContent = group.crop ? "已裁切为 " + group.crop.width + " × " + group.crop.height + " px"
    : "组内所有图片尺寸一致时可批量裁切。";
  get("drop-empty").hidden = group.images.length > 0;
  get("order-hint").hidden = group.images.length < 2;
  get("order-hint").textContent = matchMedia("(max-width: 720px), (pointer: coarse)").matches
    ? "点序号调整位置" : "拖动或点序号调整位置";

  const focused = document.activeElement;
  const focusCard = focused?.closest(".image-card")?.dataset.id;
  const focusAction = focused?.dataset.action;
  imageList.replaceChildren(...group.images.map((image, index) => renderImage(group, image, index)));
  if (focusCard && focusAction) {
    imageList.querySelector('[data-id="' + focusCard + '"] [data-action="' + focusAction + '"]')
      ?.focus({ preventScroll: true });
  }
  updateUpload();
  updateExport();
}

function renderImage(group, image, index) {
  const card = element("li", "image-card");
  card.dataset.id = image.id;
  const picture = element("img");
  picture.alt = image.name;
  picture.src = imagePath(group, image, true);
  picture.loading = "lazy";
  picture.draggable = false;
  const preview = element("button", "image-preview");
  preview.type = "button";
  preview.dataset.action = "preview";
  preview.setAttribute("aria-label", "查看 " + image.name);
  preview.append(picture);
  preview.addEventListener("click", () => openPreview(group, image));

  const position = element("button", "image-position", index + 1);
  position.type = "button";
  position.dataset.action = "position";
  position.title = "调整图片位置";
  position.setAttribute("aria-label", "第 " + (index + 1) + " 张，调整位置");
  position.disabled = group.images.length < 2;
  position.addEventListener("click", () => {
    moveContext = { group, from: index };
    get("position").max = group.images.length;
    get("position").value = index + 1;
    get("move-dialog").showModal();
  });

  const grip = element("span", "icon-button image-grip");
  grip.title = "拖动调整顺序";
  grip.setAttribute("aria-hidden", "true");
  grip.append(icon("grip"));
  grip.draggable = group.images.length > 1;
  grip.hidden = !grip.draggable;
  grip.addEventListener("dragstart", (event) => {
    dragging = { group, from: index };
    event.dataTransfer.setData("text/plain", image.id);
    event.dataTransfer.effectAllowed = "move";
    event.dataTransfer.setDragImage(card, card.clientWidth / 2, 28);
    card.classList.add("dragging");
  });
  grip.addEventListener("dragend", finishDrag);
  card.addEventListener("dragover", (event) => {
    if (!dragging || dragging.from === index) return;
    event.preventDefault();
    event.dataTransfer.dropEffect = "move";
    card.classList.add("drop-target");
  });
  card.addEventListener("dragleave", (event) => {
    if (!card.contains(event.relatedTarget)) card.classList.remove("drop-target");
  });
  card.addEventListener("drop", (event) => {
    if (!dragging) return;
    event.preventDefault();
    const { group, from } = dragging;
    finishDrag();
    moveImage(group, from, index);
  });

  const info = element("div", "image-info");
  const name = element("p", "image-name", image.name);
  name.title = image.name;
  const size = group.crop || image;
  info.append(name, element("p", "image-size", size.width + " × " + size.height));

  const actions = element("div", "image-actions");
  const up = iconButton("up", "前移一位", () => moveImage(group, index, index - 1));
  const down = iconButton("down", "后移一位", () => moveImage(group, index, index + 1));
  const remove = iconButton("trash", "删除 " + image.name, () => deleteImage(group, image));
  up.dataset.action = "up";
  down.dataset.action = "down";
  remove.dataset.action = "delete";
  up.disabled = index === 0;
  down.disabled = index === group.images.length - 1;
  actions.append(up, down, remove);
  card.append(preview, position, grip, info, actions);
  return card;
}

function openPreview(group, image) {
  preview = { groupID: group.id, imageID: image.id };
  renderPreview();
  get("preview-dialog").showModal();
}

function previewSelection() {
  const group = groups.find((entry) => entry.id === preview?.groupID);
  const index = group?.images.findIndex((image) => image.id === preview.imageID) ?? -1;
  return { group, index };
}

function renderPreview() {
  const { group, index } = previewSelection();
  if (index < 0) {
    preview = null;
    get("preview-dialog").close();
    return;
  }
  const image = group.images[index];
  const enlarged = get("preview-image");
  const src = imagePath(group, image, true);
  if (enlarged.getAttribute("src") !== src) enlarged.src = src;
  enlarged.alt = image.name;
  get("preview-name").textContent = image.name;
  get("preview-count").textContent = (index + 1) + " / " + group.images.length;
  get("preview-prev").disabled = index === 0;
  get("preview-next").disabled = index === group.images.length - 1;
}

function stepPreview(offset) {
  const { group, index } = previewSelection();
  if (index < 0) return;
  const next = group.images[index + offset];
  if (!next) return;
  preview.imageID = next.id;
  renderPreview();
}

function finishDrag() {
  if (!dragging) return;
  dragging = null;
  render();
}

function moveImage(group, from, to) {
  if (from === to || to < 0 || to >= group.images.length) return;
  const ids = group.images.map((image) => image.id);
  const [moved] = ids.splice(from, 1);
  ids.splice(to, 0, moved);
  return saveOrder(group, ids);
}

function sortGroupImages(direction) {
  const group = activeGroup();
  if (!group || group.images.length < 2 || upload) return;
  const sorted = [...group.images].sort((a, b) => direction * imageNameOrder.compare(a.name, b.name));
  if (sorted.every((image, index) => image.id === group.images[index].id)) return;
  return saveOrder(group, sorted.map((image) => image.id));
}

async function saveOrder(group, ids) {
  if (pendingOrder) return;
  pendingOrder = true;
  updateSortControl();
  try {
    await send(groupPath(group.id) + "/order", "PUT", { revision: group.revision, ids });
    await refresh(true);
  } catch (error) {
    notify(error.message);
    await refresh();
  } finally {
    pendingOrder = false;
    updateSortControl();
  }
}

function updateSortControl() {
  sortImages.disabled = pendingOrder || Boolean(upload) || (activeGroup()?.images.length ?? 0) < 2;
}

async function deleteImage(group, image) {
  if (!confirm("删除图片「" + image.name + "」？")) return;
  try {
    await api(imagePath(group, image), { method: "DELETE" });
    await refresh(true);
  } catch (error) {
    notify(error.message);
  }
}

function today() {
  const date = new Date();
  return [date.getFullYear(), date.getMonth() + 1, date.getDate()]
    .map((part) => String(part).padStart(2, "0")).join("-");
}

function showGroupForm(group) {
  editID = group?.id || null;
  get("group-dialog-title").textContent = group ? "编辑截图组" : "新建截图组";
  get("name").value = group?.name || "";
  get("date").value = group?.date || today();
  get("group-dialog").showModal();
}

async function saveGroup(event) {
  event.preventDefault();
  const button = event.submitter;
  button.disabled = true;
  try {
    const data = Object.fromEntries(new FormData(event.currentTarget));
    const response = await send(editID ? groupPath(editID) : "/api/groups", editID ? "PATCH" : "POST", data);
    const group = await response.json();
    get("group-dialog").close();
    selected = group.id;
    localStorage.setItem("selected-group", selected);
    await refresh(true);
  } catch (error) {
    notify(error.message);
  } finally {
    button.disabled = false;
  }
}

async function deleteGroup() {
  const group = activeGroup();
  if (!group || !confirm("删除截图组「" + group.name + "」及组内全部图片？")) return;
  const button = get("delete-group");
  button.disabled = true;
  try {
    await api(groupPath(group.id), { method: "DELETE" });
    await refresh(true);
    notify("已删除截图组。");
  } catch (error) {
    notify(error.message);
  } finally {
    button.disabled = false;
  }
}

function updateUpload() {
  const status = get("upload-status");
  status.hidden = !upload || upload.groupID !== selected;
  if (!status.hidden) status.textContent = "正在上传 " + upload.current + " / " + upload.total;
  get("upload").disabled = Boolean(upload);
  get("drop-empty").disabled = Boolean(upload);
  updateSortControl();
}

async function prepareImage(file) {
  const header = new Uint8Array(await file.slice(0, 8).arrayBuffer());
  const isPNG = header.join() === "137,80,78,71,13,10,26,10";
  const isGIF = String.fromCharCode(...header.slice(0, 6)) === "GIF89a"
    || String.fromCharCode(...header.slice(0, 6)) === "GIF87a";
  if (isPNG || isGIF) return file;
  if (file.size > 64 * 1024 * 1024) throw new Error("文件过大。");

  const url = URL.createObjectURL(file);
  const image = new Image();
  image.src = url;
  try {
    await image.decode();
    if (image.naturalWidth * image.naturalHeight > 24_000_000) {
      throw new Error("单张图片不能超过 2400 万像素。");
    }
    const canvas = document.createElement("canvas");
    canvas.width = image.naturalWidth;
    canvas.height = image.naturalHeight;
    canvas.getContext("2d").drawImage(image, 0, 0);
    let blob = await new Promise((resolve) => canvas.toBlob(resolve, "image/png"));
    if (!blob || blob.size > maxUploadBytes) {
      blob = await new Promise((resolve) => canvas.toBlob(resolve, "image/jpeg", 0.98));
    }
    if (!blob) throw new Error("无法转换这张图片。");
    return blob;
  } catch (error) {
    if (error instanceof DOMException) throw new Error("浏览器无法读取此格式，请使用 PNG、JPEG 或 GIF。");
    throw error;
  } finally {
    URL.revokeObjectURL(url);
  }
}

async function uploadFiles(groupID, files) {
  if (!groupID || upload || files.length === 0) return;
  upload = { groupID, current: 0, total: files.length };
  let done = 0;
  let firstError = "";
  try {
    for (const file of files) {
      upload.current += 1;
      updateUpload();
      try {
        const body = await prepareImage(file);
        if (body.size > maxUploadBytes) throw new Error("单张图片不能超过 32 MB。");
        await api(groupPath(groupID) + "/images?name=" + encodeURIComponent(file.name), {
          method: "POST",
          headers: { "Content-Type": "application/octet-stream" },
          body,
        });
        done += 1;
      } catch (error) {
        firstError ||= file.name + " · " + error.message;
      }
    }
    const failed = files.length - done;
    const result = "已上传 " + done + " 张" + (failed ? "，" + failed + " 张失败。 " + firstError : "。");
    notify(result);
    await refresh(true);
  } catch (error) {
    notify(error.message);
  } finally {
    upload = null;
    updateUpload();
  }
}

function projected(group) {
  const width = widthInput.valueAsNumber;
  const valid = Number.isInteger(width) && width >= 1 && width <= 4096;
  const height = valid ? (group?.images || []).reduce((sum, image) => {
    const size = group.crop || image;
    return sum + Math.max(1, Math.round(size.height * width / size.width));
  }, 0) : 0;
  return { width, height, valid, oversized: height * width > maxExportPixels };
}

function canCrop(group) {
  const first = group?.images[0];
  return Boolean(first && group.images.every((image) => image.width === first.width && image.height === first.height));
}

function openCrop() {
  cropGroup = activeGroup();
  if (!canCrop(cropGroup)) return;
  const first = cropGroup.images[0];
  cropInputs.x.max = first.width - 1;
  cropInputs.y.max = first.height - 1;
  get("crop-image").src = imagePath(cropGroup, first);
  get("clear-crop").hidden = !cropGroup.crop;
  showCropRegion(cropGroup.crop || { x: 0, y: 0, width: first.width, height: first.height });
  get("crop-dialog").showModal();
}

function showCropRegion(region) {
  for (const [key, input] of Object.entries(cropInputs)) input.value = region[key];
  updateCropBox();
}

function cropRegionFromInputs() {
  return Object.fromEntries(Object.entries(cropInputs).map(([key, input]) => [key, input.valueAsNumber]));
}

function updateCropBox() {
  const first = cropGroup.images[0];
  const region = cropRegionFromInputs();
  const { x, y, width, height } = region;
  cropInputs.width.max = first.width - (x || 0);
  cropInputs.height.max = first.height - (y || 0);
  const valid = Object.values(cropInputs).every((input) => input.checkValidity());
  const box = get("crop-box");
  box.hidden = !valid;
  get("apply-crop").disabled = !valid || get("clear-crop").disabled;
  if (!valid) return;
  box.style.left = x / first.width * 100 + "%";
  box.style.top = y / first.height * 100 + "%";
  box.style.width = width / first.width * 100 + "%";
  box.style.height = height / first.height * 100 + "%";
}

function cropPoint(event) {
  const first = cropGroup.images[0];
  const rect = get("crop-image").getBoundingClientRect();
  return {
    x: Math.max(0, Math.min(first.width, Math.round((event.clientX - rect.left) * first.width / rect.width))),
    y: Math.max(0, Math.min(first.height, Math.round((event.clientY - rect.top) * first.height / rect.height))),
  };
}

function drawCrop(event) {
  if (cropStart?.pointer !== event.pointerId) return;
  const point = cropPoint(event);
  const width = Math.abs(cropStart.x - point.x);
  const height = Math.abs(cropStart.y - point.y);
  if (!width || !height) return;
  showCropRegion({ x: Math.min(cropStart.x, point.x), y: Math.min(cropStart.y, point.y), width, height });
}

async function saveCrop(region) {
  const group = cropGroup;
  const apply = get("apply-crop");
  const clear = get("clear-crop");
  if (clear.disabled) return;
  apply.disabled = true;
  clear.disabled = true;
  try {
    await send(groupPath(group.id) + "/crop", "PUT", { revision: group.revision, crop: region });
    if (cropGroup === group) get("crop-dialog").close();
    await refresh(true);
    notify(region ? "已裁切整组图片。" : "已恢复原图。");
  } catch (error) {
    notify(error.message);
    await refresh();
  } finally {
    clear.disabled = false;
    if (cropGroup === group) updateCropBox();
  }
}

function updateExport() {
  const { width, height, valid, oversized } = projected(activeGroup());
  const label = get("dimensions");
  label.toggleAttribute("data-invalid", !valid || oversized);
  if (!valid) label.textContent = "请输入 1–4096";
  else if (oversized) label.textContent = "长图超过 5 亿像素，请调小宽度";
  else label.textContent = height ? "预计 " + width + " × " + height + " px" : "";
  exportButton.disabled = !height || !valid || oversized;
}

function exportImage() {
  const group = activeGroup();
  const { width, valid, oversized } = projected(group);
  if (!group?.images.length || !valid || oversized) return;
  const link = element("a");
  link.href = groupPath(group.id) + "/export?width=" + width;
  link.download = "";
  document.body.append(link);
  link.click();
  link.remove();
}

async function showAccess() {
  const list = get("addresses");
  list.replaceChildren(element("p", "", "正在获取访问地址…"));
  get("access-dialog").showModal();
  try {
    const response = await api("/api/access");
    const urls = await response.json();
    list.replaceChildren();
    for (const url of urls) {
      const row = element("div", "address-row");
      const field = element("input");
      field.value = url;
      field.readOnly = true;
      field.setAttribute("aria-label", "手机访问地址");
      const button = element("button", "button", "复制");
      button.type = "button";
      button.addEventListener("click", async () => {
        try {
          if (navigator.clipboard) await navigator.clipboard.writeText(url);
          else {
            field.select();
            if (!document.execCommand("copy")) return;
          }
          button.textContent = "已复制";
        } catch {
          field.select();
        }
      });
      row.append(field, button);
      list.append(row);
    }
    if (!urls.length) list.append(element("p", "", "未找到局域网地址。请先让电脑连接局域网。"));
  } catch (error) {
    list.replaceChildren(element("p", "", error.message));
  }
}

picker.addEventListener("change", () => choose(picker.value));
get("crop-button").addEventListener("click", openCrop);
get("crop-form").addEventListener("submit", (event) => {
  event.preventDefault();
  saveCrop(cropRegionFromInputs());
});
get("clear-crop").addEventListener("click", () => saveCrop(null));
for (const input of Object.values(cropInputs)) input.addEventListener("input", updateCropBox);
const cropSurface = get("crop-surface");
cropSurface.addEventListener("pointerdown", (event) => {
  const image = get("crop-image");
  if (event.button !== 0 || !event.isPrimary || !image.naturalWidth) return;
  cropStart = { ...cropPoint(event), pointer: event.pointerId };
  cropSurface.setPointerCapture(event.pointerId);
});
cropSurface.addEventListener("pointermove", drawCrop);
cropSurface.addEventListener("pointerup", (event) => {
  drawCrop(event);
  cropStart = null;
});
cropSurface.addEventListener("lostpointercapture", () => { cropStart = null; });
get("crop-dialog").addEventListener("close", () => {
  cropGroup = null;
  cropStart = null;
  get("crop-image").removeAttribute("src");
});
get("new-group").addEventListener("click", () => showGroupForm());
get("start-group").addEventListener("click", () => showGroupForm());
get("edit").addEventListener("click", () => showGroupForm(activeGroup()));
get("group-form").addEventListener("submit", saveGroup);
get("delete-group").addEventListener("click", deleteGroup);
get("phone").addEventListener("click", showAccess);
get("upload").addEventListener("click", () => fileInput.click());
get("drop-empty").addEventListener("click", () => fileInput.click());
sortImages.addEventListener("change", () => {
  const direction = sortImages.value;
  sortImages.value = "";
  if (direction) sortGroupImages(direction === "asc" ? 1 : -1);
});
fileInput.addEventListener("change", () => {
  const files = Array.from(fileInput.files);
  fileInput.value = "";
  uploadFiles(activeGroup()?.id, files);
});
widthInput.addEventListener("input", () => {
  if (widthInput.checkValidity()) localStorage.setItem("export-width", widthInput.value);
  updateExport();
});
exportButton.addEventListener("click", exportImage);
get("move-form").addEventListener("submit", (event) => {
  event.preventDefault();
  const { group, from } = moveContext;
  const to = get("position").valueAsNumber - 1;
  get("move-dialog").close();
  moveImage(group, from, to);
});
for (const dialog of document.querySelectorAll("dialog")) {
  dialog.addEventListener("close", () => {
    if (dialog.contains(get("toast"))) {
      get("toast").hidden = true;
      document.body.append(get("toast"));
    }
  });
}
for (const button of document.querySelectorAll("[data-close]")) {
  button.addEventListener("click", () => button.closest("dialog").close());
}
get("preview-prev").addEventListener("click", () => stepPreview(-1));
get("preview-next").addEventListener("click", () => stepPreview(1));
get("preview-dialog").addEventListener("keydown", (event) => {
  if (event.key !== "ArrowLeft" && event.key !== "ArrowRight") return;
  event.preventDefault();
  stepPreview(event.key === "ArrowLeft" ? -1 : 1);
});
get("preview-dialog").addEventListener("close", () => {
  preview = null;
  get("preview-image").removeAttribute("src");
});

const zone = get("image-zone");
zone.addEventListener("dragover", (event) => {
  if (!event.dataTransfer.types.includes("Files")) return;
  event.preventDefault();
  if (upload) return;
  event.dataTransfer.dropEffect = "copy";
  zone.classList.add("drag-files");
});
zone.addEventListener("dragleave", (event) => {
  if (!zone.contains(event.relatedTarget)) zone.classList.remove("drag-files");
});
zone.addEventListener("drop", (event) => {
  if (!event.dataTransfer.types.includes("Files")) return;
  event.preventDefault();
  zone.classList.remove("drag-files");
  uploadFiles(activeGroup()?.id, Array.from(event.dataTransfer.files));
});
window.addEventListener("dragover", (event) => {
  if (event.dataTransfer.types.includes("Files")) event.preventDefault();
});
window.addEventListener("drop", (event) => {
  if (event.dataTransfer.types.includes("Files")) event.preventDefault();
});
window.addEventListener("focus", () => refresh());
window.addEventListener("online", () => refresh());
document.addEventListener("visibilitychange", () => {
  if (!document.hidden) refresh();
});
setInterval(() => {
  if (!document.hidden) refresh();
}, 2200);
refresh();
