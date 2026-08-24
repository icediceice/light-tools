(() => {
  "use strict";

  const byId = (id) => document.getElementById(id);
  const pairView = byId("pair-view");
  const passwordView = byId("password-view");
  const vaultView = byId("vault-view");
  const vaultPanel = byId("vault-panel");
  const settingsView = byId("settings-view");
  const telemetryView = byId("telemetry-view");
  const viewTabs = {
    vault: byId("view-vault"),
    settings: byId("view-settings"),
    telemetry: byId("view-telemetry")
  };
  const logoutButton = byId("logout");
  const message = byId("message");
  let token = sessionStorage.getItem("light-vault-session") || "";
  let configured = false;
  let overview = { secrets: [], groups: [] };
  let pendingImport = null;
  let importSequence = 0;
  // The value control of whichever overlay currently owns the adopted import
  // nodes. There is exactly one import flow, so there is exactly one of these.
  let activeValueInput = null;
  let collapsed = {};
  let filter = "";
  let openOverlay = null;
  const maxImportedFileBytes = 1 << 20;

  function show(view) {
    for (const candidate of [pairView, passwordView, vaultView]) {
      candidate.classList.toggle("hidden", candidate !== view);
    }
    logoutButton.classList.toggle("hidden", !token);
  }

  function showView(name) {
    closeOverlay();
    for (const key of Object.keys(viewTabs)) {
      viewTabs[key].classList.toggle("active", key === name);
    }
    vaultPanel.classList.toggle("hidden", name !== "vault");
    settingsView.classList.toggle("hidden", name !== "settings");
    telemetryView.classList.toggle("hidden", name !== "telemetry");
  }

  for (const key of Object.keys(viewTabs)) {
    viewTabs[key].addEventListener("click", () => {
      showView(key);
      if (key === "settings") loadSettings();
      if (key === "telemetry") loadTelemetry();
    });
  }

  function notice(text, error = false) {
    message.textContent = text;
    message.classList.toggle("error", error);
    message.classList.toggle("hidden", !text);
  }

  function el(tag, className, text) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text != null) node.textContent = text;
    return node;
  }

  function button(label, className) {
    const element = document.createElement("button");
    element.type = "button";
    element.className = className;
    element.textContent = label;
    return element;
  }

  function empty(text) {
    const element = document.createElement("p");
    element.className = "empty";
    element.textContent = text;
    return element;
  }

  function option(value, label) {
    const element = document.createElement("option");
    element.value = value;
    element.textContent = label;
    return element;
  }

  // ---- Import flow ------------------------------------------------------
  //
  // The file input and its status row are declared once in index.html and are
  // MOVED into whichever overlay needs them, then handed back on close. One
  // hardened reader serves both the add and the edit overlay, so the UTF-8
  // check, the size cap and the sequence guard cannot drift apart.
  //
  // These handles are captured ONCE, deliberately. An overlay calls adoptImport
  // while it is still DETACHED, which takes these nodes out of the document
  // tree, so a getElementById lookup after that point returns null.
  const importHome = byId("secret-import-home");
  const importField = byId("secret-file-field");
  const importRow = byId("secret-import");
  const importName = byId("secret-import-name");
  const importSize = byId("secret-import-size");
  const importFile = byId("secret-file");

  function renderPendingImport() {
    const active = pendingImport !== null;
    importRow.classList.toggle("hidden", !active);
    if (activeValueInput) {
      activeValueInput.disabled = active;
    }
    importName.textContent = active ? pendingImport.name : "";
    importSize.textContent = active
      ? pendingImport.size.toLocaleString() + " bytes ready to save"
      : "";
  }

  function discardImport(reason = "") {
    importSequence += 1;
    pendingImport = null;
    importFile.value = "";
    renderPendingImport();
    if (reason) {
      notice(reason, true);
    }
  }

  function adoptImport(node, valueInput, labelText) {
    activeValueInput = valueInput;
    importField.firstElementChild.textContent = labelText;
    node.append(importField, importRow);
    renderPendingImport();
  }

  function releaseImport() {
    importHome.append(importField, importRow);
    activeValueInput = null;
  }

  byId("secret-import-discard").addEventListener("click", () => {
    discardImport();
    notice("Imported file discarded.");
  });

  byId("secret-file").addEventListener("change", async (event) => {
    const input = event.currentTarget;
    const file = input.files && input.files[0];
    input.value = "";
    const sequence = ++importSequence;
    pendingImport = null;
    renderPendingImport();
    if (!file) return;

    const valueInput = activeValueInput;
    if (valueInput && valueInput.value !== "") {
      notice("Clear the typed value before importing a file.", true);
      return;
    }
    if (file.size === 0) {
      notice("That file is empty.", true);
      return;
    }
    if (file.size > maxImportedFileBytes) {
      notice("That file is larger than 1 MiB.", true);
      return;
    }

    notice("Reading " + file.name + " locally…");
    let text;
    try {
      const bytes = await file.arrayBuffer();
      text = new TextDecoder("utf-8", { fatal: true }).decode(bytes);
    } catch {
      if (sequence !== importSequence) return;
      notice("That file is not UTF-8 text. Export a PEM or OpenSSH copy first.", true);
      return;
    }
    if (sequence !== importSequence) return;
    if (valueInput && valueInput.value !== "") {
      notice("The typed value was kept; clear it before importing a file.", true);
      return;
    }

    pendingImport = { text, name: file.name, size: file.size };
    renderPendingImport();
    notice("File loaded locally. Save to store it in the vault.");
  });

  async function api(route, options = {}) {
    const headers = new Headers(options.headers || {});
    if (token) {
      headers.set("Authorization", "Bearer " + token);
    }
    if (options.body) {
      headers.set("Content-Type", "application/json");
    }
    const response = await fetch(route, { ...options, headers });
    const text = await response.text();
    let body = {};
    if (text && response.headers.get("Content-Type")?.includes("application/json")) {
      body = JSON.parse(text);
    }
    if (!response.ok) {
      throw new Error(text.trim() || "Request failed");
    }
    return body;
  }

  async function boot() {
    notice("");
    if (!token) {
      show(pairView);
      byId("pair-code").focus();
      return;
    }
    try {
      const status = await api("/api/status");
      configured = Boolean(status.configured);
      if (status.authenticated) {
        show(vaultView);
        showView("vault");
        await loadVault();
      } else {
        showPassword();
      }
    } catch {
      token = "";
      sessionStorage.removeItem("light-vault-session");
      show(pairView);
      notice("Pair this browser again with the code in your terminal.", true);
    }
  }

  function showPassword() {
    byId("password-title").textContent = configured ? "Unlock vault" : "Create a vault password";
    byId("password-help").textContent = configured
      ? "Enter the password you created for this local vault UI."
      : "Choose a password of at least 8 characters. It protects access to this browser UI.";
    byId("password-submit").textContent = configured ? "Unlock" : "Create password";
    byId("password").setAttribute("autocomplete", configured ? "current-password" : "new-password");
    show(passwordView);
    byId("password").focus();
  }

  byId("pair-form").addEventListener("submit", async (event) => {
    event.preventDefault();
    notice("");
    try {
      const result = await api("/api/pair", {
        method: "POST",
        body: JSON.stringify({ code: byId("pair-code").value })
      });
      token = result.token;
      sessionStorage.setItem("light-vault-session", token);
      byId("pair-code").value = "";
      await boot();
    } catch {
      notice("That pairing code is invalid or expired. Restart “light-tools vault ui” for a new code.", true);
    }
  });

  byId("password-form").addEventListener("submit", async (event) => {
    event.preventDefault();
    notice("");
    const input = byId("password");
    const password = input.value;
    input.value = "";
    try {
      await api(configured ? "/api/login" : "/api/setup", {
        method: "POST",
        body: JSON.stringify({ password })
      });
      show(vaultView);
      showView("vault");
      await loadVault();
      loadSettings();
      loadTelemetry();
    } catch (error) {
      const busy = String(error.message).includes("busy");
      notice(busy ? "Password verification is busy. Try once more." : "Password setup or login failed.", true);
    }
  });

  logoutButton.addEventListener("click", async () => {
    try {
      await api("/api/logout", { method: "POST", body: "{}" });
    } catch {
      // The local session may already have expired.
    }
    token = "";
    sessionStorage.removeItem("light-vault-session");
    closeOverlay();
    discardImport();
    showView("vault");
    show(pairView);
    notice("Vault locked. Restart the terminal command to pair again.");
  });

  byId("refresh").addEventListener("click", loadVault);
  byId("settings-refresh").addEventListener("click", () => loadSettings());
  byId("telemetry-refresh").addEventListener("click", () => loadTelemetry());

  // ---- Settings view -------------------------------------------------

  // The toggles reflect ONLY the UI-owned markers. Launch withholding is not
  // observable from this process; tools/list stays authoritative.
  async function loadSettings() {
    try {
      const state = await api("/api/settings");
      renderSettings(state.tools || [], state);
    } catch {
      notice("Settings could not be loaded.", true);
    }
  }

  // The confinement row sits above the tool toggles because it governs all of
  // them at once. Default posture is unconfined: light-tools is meant to
  // replace the agent's native edit tool, which has no boundary either.
  function renderConfinement(container, state) {
    const authoritative = Boolean(state.config_roots_authoritative);
    const confined = Boolean(state.confine);
    const row = el("div", "row toggle-row");
    const main = el("div", "row-main");
    main.append(
      el("div", "row-title", "Confine file tools to the working directory"),
      el("div", "row-meta", authoritative
        ? "config.toml sets allowed_roots, which outranks this switch — edit the file to change the boundary"
        : confined
          ? "At the next MCP start: light_file paths, local SCP endpoints and caller-supplied light_ops paths are held inside the working directory. light_bash only has its cwd held there — the command itself still reaches any path."
          : "Unconfined: every path is reachable except light-tools' own secrets, snapshots, spills and telemetry")
    );

    const toggle = el("label", "switch");
    const input = document.createElement("input");
    input.type = "checkbox";
    input.checked = authoritative || confined;
    input.disabled = authoritative;
    input.setAttribute("aria-label", "Confine file tools to the working directory");
    const slider = el("span", "slider");
    slider.setAttribute("aria-hidden", "true");
    toggle.append(input, slider);
    input.addEventListener("change", async () => {
      const wanted = input.checked;
      notice("");
      try {
        await api("/api/settings/confine", {
          method: "POST",
          body: JSON.stringify({ confine: wanted })
        });
        notice(wanted
          ? "File paths will be confined to the working directory at the next MCP start. light_bash keeps full filesystem access; only its cwd is confined."
          : "Confinement removed; file tools reach any path at the next MCP start.");
        await loadSettings();
      } catch {
        input.checked = !wanted;
        notice("The setting could not be saved.", true);
      }
    });
    row.append(main, toggle);
    container.append(row);
  }

  function renderSettings(tools, state) {
    const container = byId("settings-tools");
    container.replaceChildren();
    renderConfinement(container, state || {});
    if (!tools.length) {
      container.append(empty("No tools are known to this UI."));
      return;
    }
    for (const tool of tools) {
      const row = el("div", "row toggle-row");
      const main = el("div", "row-main");
      main.append(
        el("div", "row-title", tool.name),
        el("div", "row-meta", tool.disabled
          ? "Withheld by this UI at the next MCP start"
          : "Registered, as far as this UI can see")
      );

      const toggle = el("label", "switch");
      const input = document.createElement("input");
      input.type = "checkbox";
      input.checked = Boolean(tool.disabled);
      input.setAttribute("aria-label", "Withhold " + tool.name);
      const slider = el("span", "slider");
      slider.setAttribute("aria-hidden", "true");
      toggle.append(input, slider);
      input.addEventListener("change", async () => {
        const withheld = input.checked;
        notice("");
        try {
          await api("/api/settings/tools", {
            method: "POST",
            body: JSON.stringify({ tool: tool.name, disabled: withheld })
          });
          notice(withheld
            ? tool.name + " will be withheld at the next MCP start."
            : tool.name + " marker removed; it registers at the next MCP start unless launch arguments withhold it.");
          await loadSettings();
        } catch {
          input.checked = !withheld;
          notice("The setting could not be saved.", true);
        }
      });
      row.append(main, toggle);
      container.append(row);
    }
  }

  // ---- Telemetry view --------------------------------------------------

  async function loadTelemetry() {
    try {
      renderTelemetry(await api("/api/telemetry"));
    } catch {
      notice("Telemetry could not be loaded.", true);
    }
  }

  function renderTelemetry(totals) {
    const cards = byId("telemetry-cards");
    cards.replaceChildren();
    cards.append(
      card("Terse output", (Number(totals.terse_tokens_saved) || 0).toLocaleString() + " tokens", "saved by terse result formatting"),
      card("Read dedup", formatBytes(Number(totals.dedup_bytes_saved) || 0), "not re-sent for repeated reads"),
      card("Writing", formatBytes(Number(totals.write_bytes_saved) || 0), "vs. sending a full rewrite")
    );

    const warnings = byId("telemetry-warnings");
    warnings.replaceChildren();
    const list = totals.warnings || [];
    if (!list.length) {
      warnings.append(empty("All retained snapshots are healthy."));
    } else {
      for (const warning of list) {
        const row = el("div", "row");
        row.append(el("div", "row-meta", warning));
        warnings.append(row);
      }
    }

    const calls = byId("telemetry-calls");
    calls.replaceChildren();
    const entries = Object.entries(totals.calls || {}).sort((left, right) => right[1] - left[1]);
    if (!entries.length) {
      calls.append(empty("No tool calls recorded yet."));
      return;
    }
    for (const [name, count] of entries) {
      const row = el("div", "row");
      row.append(
        el("div", "row-title", name),
        el("div", "row-meta", Number(count).toLocaleString() + " calls")
      );
      calls.append(row);
    }
  }

  function card(title, value, label) {
    const element = el("div", "card");
    element.append(
      el("div", "card-title", title),
      el("div", "card-value", value),
      el("div", "card-label", label)
    );
    return element;
  }

  function formatBytes(count) {
    if (count < 1024) return count.toLocaleString() + " B";
    const units = ["KiB", "MiB", "GiB", "TiB"];
    let value = count;
    for (const unit of units) {
      value /= 1024;
      if (value < 1024 || unit === units[units.length - 1]) {
        return value.toLocaleString(undefined, { maximumFractionDigits: 1 }) + " " + unit;
      }
    }
  }

  // ---- Overlay ---------------------------------------------------------

  function closeOverlay() {
    if (!openOverlay) return;
    const current = openOverlay;
    openOverlay = null;
    discardImport();
    // Hand the adopted nodes back BEFORE the overlay is destroyed, or they are
    // removed along with it and every later overlay finds them missing.
    releaseImport();
    if (current.scrim) current.scrim.remove();
    current.node.remove();
    const host = current.anchor && current.anchor.closest(".slot, .add-slot");
    if (host) host.classList.remove("open");
    if (current.anchor && current.anchor.isConnected) current.anchor.focus();
  }

  // Anchored and collision-flipped: the bottom row of the grid never has room
  // below it, so read the anchor rect rather than assuming the panel fits.
  function place(node, anchor) {
    const rect = anchor.getBoundingClientRect();
    const width = node.offsetWidth;
    const height = node.offsetHeight;
    const pad = 8;
    const left = Math.min(Math.max(pad, rect.left), window.innerWidth - width - pad);
    let top = rect.bottom + 6;
    if (top + height > window.innerHeight - pad) {
      top = rect.top - height - 6;
      if (top < pad) top = Math.max(pad, window.innerHeight - height - pad);
    }
    node.style.left = left + "px";
    node.style.top = top + "px";
  }

  function overlay(anchor, title, kind, build) {
    closeOverlay();
    const scrim = el("div", "ov-scrim");
    const node = el("div", "overlay");
    node.setAttribute("role", "dialog");
    node.setAttribute("aria-modal", "true");
    node.setAttribute("aria-label", title);

    const head = el("div", "ov-head");
    head.append(el("span", "ov-title", title), el("span", "ov-kind", kind));
    node.append(head);
    build(node);

    document.body.append(scrim, node);
    place(node, anchor);
    const host = anchor.closest(".slot, .add-slot");
    if (host) host.classList.add("open");
    openOverlay = { node, scrim, anchor };

    scrim.addEventListener("mousedown", closeOverlay);
    const focusable = node.querySelector("input, textarea, select, button");
    if (focusable) focusable.focus();
    return node;
  }

  function field(labelText, control) {
    const label = el("label", "field");
    label.append(el("span", null, labelText), control);
    return label;
  }

  function groupSelect(current) {
    const select = document.createElement("select");
    select.append(option("", "No group"));
    for (const name of overview.groups) {
      select.append(option(name, name));
    }
    select.value = current || "";
    return select;
  }

  function actionRow(confirmLabel, onConfirm) {
    const actions = el("div", "ov-actions");
    const cancel = button("Cancel", "btn ghost");
    cancel.addEventListener("click", closeOverlay);
    const confirm = button(confirmLabel, "btn");
    confirm.addEventListener("click", onConfirm);
    actions.append(cancel, confirm);
    return actions;
  }

  // Two-step: the first press arms, the second commits. A destructive action
  // reachable in one click from a card face is too easy to hit by accident.
  function dangerRow(why, label, armedLabel, onConfirm) {
    const danger = el("div", "ov-danger");
    danger.append(el("span", "why", why));
    const remove = button(label, "btn danger");
    remove.addEventListener("click", () => {
      if (remove.dataset.armed !== "1") {
        remove.dataset.armed = "1";
        remove.textContent = armedLabel;
        return;
      }
      onConfirm();
    });
    danger.append(remove);
    return danger;
  }

  // ---- Vault view ------------------------------------------------------

  async function loadVault() {
    notice("");
    try {
      overview = await api("/api/vault");
      overview.secrets ||= [];
      overview.groups ||= [];
      render();
    } catch {
      notice("Vault metadata could not be loaded.", true);
    }
  }

  function editSecret(anchor, item) {
    overlay(anchor, item.name, "key", (node) => {
      const valueInput = document.createElement("textarea");
      valueInput.rows = 3;
      valueInput.autocomplete = "off";
      valueInput.placeholder = "paste a new value to replace it";
      node.append(field("Replace value", valueInput));
      node.append(el("div", "ov-hint", "Sent once and never returned. Leave this blank to keep the stored value."));

      adoptImport(node, valueInput, "Or replace it from a file");

      const group = groupSelect(item.group);
      node.append(field("Group", group));

      node.append(actionRow("Save", async () => {
        const replacement = pendingImport ? pendingImport.text : valueInput.value;
        const moved = group.value !== (item.group || "");
        if (!replacement && !moved) {
          notice("Nothing to save.");
          closeOverlay();
          return;
        }
        try {
          if (replacement) {
            await api("/api/secret/set", {
              method: "POST",
              body: JSON.stringify({ name: item.name, value: replacement, group: group.value })
            });
          } else {
            await api("/api/secret/group", {
              method: "POST",
              body: JSON.stringify({ name: item.name, group: group.value })
            });
          }
        } catch (error) {
          const expired = String(error.message).includes("unauthorized");
          notice(expired
            ? "Your session expired. Copy the value or reselect the file after restarting the vault UI."
            : "Secret could not be saved. Check its name, group, and size.", true);
          return;
        }
        closeOverlay();
        notice(replacement
          ? item.name + " updated. Its value will not be shown again."
          : item.name + " moved to " + (group.value || "no group") + ".");
        await loadVault();
      }));

      node.append(dangerRow(
        "Deleting is permanent. Anything reading this name breaks.",
        "Delete", "Confirm delete",
        async () => {
          try {
            await api("/api/secret/remove", {
              method: "POST",
              body: JSON.stringify({ name: item.name })
            });
          } catch {
            notice("Secret could not be deleted.", true);
            return;
          }
          closeOverlay();
          notice(item.name + " deleted.");
          await loadVault();
        }
      ));
    });
  }

  function addSecret(anchor, presetGroup) {
    overlay(anchor, "New secret", "key", (node) => {
      const nameInput = document.createElement("input");
      nameInput.type = "text";
      nameInput.pattern = "[-_.A-Za-z0-9]+";
      nameInput.autocomplete = "off";
      nameInput.placeholder = "SERVICE_API_TOKEN";
      node.append(field("Name", nameInput));

      const valueInput = document.createElement("textarea");
      valueInput.rows = 3;
      valueInput.autocomplete = "off";
      valueInput.placeholder = "value";
      node.append(field("Value", valueInput));

      adoptImport(node, valueInput, "Or import from a file");

      const group = groupSelect(presetGroup);
      node.append(field("Group", group));

      node.append(actionRow("Create", async () => {
        const name = nameInput.value.trim();
        if (!name) {
          nameInput.focus();
          notice("A secret needs a name.", true);
          return;
        }
        if (!/^[-_.A-Za-z0-9]+$/.test(name)) {
          nameInput.focus();
          notice("Names allow letters, digits, dot, dash and underscore.", true);
          return;
        }
        const payload = {
          name,
          value: pendingImport ? pendingImport.text : valueInput.value,
          group: group.value
        };
        if (!payload.value) {
          notice("A secret needs a value, typed or imported.", true);
          return;
        }
        try {
          await api("/api/secret/set", { method: "POST", body: JSON.stringify(payload) });
        } catch (error) {
          const expired = String(error.message).includes("unauthorized");
          notice(expired
            ? "Your session expired. Copy the value or reselect the file after restarting the vault UI."
            : "Secret could not be saved. Check its name, group, and size.", true);
          return;
        }
        closeOverlay();
        notice(name + " saved. Its value will not be shown again.");
        await loadVault();
      }));
    });
  }

  function newGroup(anchor) {
    overlay(anchor, "New group", "group", (node) => {
      const input = document.createElement("input");
      input.type = "text";
      input.maxLength = 64;
      input.placeholder = "production";
      node.append(field("Group name", input));
      node.append(actionRow("Add", async () => {
        const name = input.value.trim();
        if (!name) {
          input.focus();
          return;
        }
        try {
          await api("/api/group/add", { method: "POST", body: JSON.stringify({ name }) });
        } catch {
          notice("Group could not be added.", true);
          return;
        }
        closeOverlay();
        await loadVault();
      }));
    });
  }

  function editGroup(anchor, name) {
    overlay(anchor, name, "group", (node) => {
      const input = document.createElement("input");
      input.type = "text";
      input.maxLength = 64;
      input.value = name;
      node.append(field("Group name", input));

      node.append(actionRow("Rename", async () => {
        const next = input.value.trim();
        if (!next || next === name) {
          closeOverlay();
          return;
        }
        try {
          await api("/api/group/rename", { method: "POST", body: JSON.stringify({ from: name, to: next }) });
        } catch {
          notice("Group could not be renamed. The destination may already exist.", true);
          return;
        }
        closeOverlay();
        await loadVault();
      }));

      node.append(dangerRow(
        "Deleting a group keeps its secrets — they move to No group.",
        "Delete group", "Confirm delete",
        async () => {
          try {
            await api("/api/group/remove", { method: "POST", body: JSON.stringify({ name }) });
          } catch {
            notice("Group could not be deleted.", true);
            return;
          }
          closeOverlay();
          notice("Group " + name + " deleted; its secrets were kept.");
          await loadVault();
        }
      ));
    });
  }

  function slotCard(item) {
    const slot = el("div", "slot");
    const face = el("button", "slot-face");
    face.type = "button";

    const socket = el("span", "zone-socket");
    socket.append(el("span", "gem", "🔑"));
    face.append(socket);

    // Names ellipsize at the card width, so keep the full one reachable.
    const identity = el("span", "zone-identity", item.name);
    identity.title = item.name;
    face.append(identity);

    const updated = item.updated_at ? new Date(item.updated_at).toLocaleDateString() : "legacy entry";
    face.append(el("span", "zone-readout mono", "updated " + updated));
    face.append(el("span", "zone-kind", "key"));

    face.addEventListener("click", () => editSecret(face, item));
    slot.append(face);
    return slot;
  }

  function addCard(presetGroup, label) {
    const add = el("button", "add-slot");
    add.type = "button";
    const socket = el("span", "zone-socket");
    socket.append(el("span", "gem-add", "+"));
    add.append(socket, el("span", "zone-identity", label));
    add.addEventListener("click", () => addSecret(add, presetGroup));
    return add;
  }

  function shelf(name, label, members) {
    const wrapper = el("div", "shelf");
    const head = el("div", "shead");
    const open = !collapsed[name];

    const toggle = el("button", "stoggle");
    toggle.type = "button";
    toggle.setAttribute("aria-expanded", String(open));
    toggle.append(
      el("span", "chev", open ? "▾" : "▸"),
      el("span", "sname", label),
      el("span", "scount", String(members.length))
    );
    toggle.addEventListener("click", () => {
      collapsed[name] = open;
      render();
    });
    head.append(toggle, el("span", "spacer"));

    if (name) {
      const rename = button("Rename", "shelf-act");
      rename.addEventListener("click", () => editGroup(rename, name));
      head.append(rename);
    }
    wrapper.append(head);

    if (open) {
      const body = el("div", "sbody");
      const grid = el("div", "card-grid");
      for (const item of members) {
        grid.append(slotCard(item));
      }
      grid.append(addCard(name, name ? "Add to " + name : "Add secret"));
      body.append(grid);
      wrapper.append(body);
    }
    return wrapper;
  }

  function render() {
    closeOverlay();
    const shelves = byId("shelves");
    shelves.replaceChildren();

    const visible = overview.secrets.filter(
      (item) => !filter || item.name.toLowerCase().includes(filter)
    );
    byId("vault-count").textContent = visible.length === overview.secrets.length
      ? overview.secrets.length + (overview.secrets.length === 1 ? " secret" : " secrets")
      : visible.length + " of " + overview.secrets.length;

    for (const name of overview.groups) {
      shelves.append(shelf(name, name, visible.filter((item) => item.group === name)));
    }
    shelves.append(shelf("", "No group", visible.filter((item) => !item.group)));

    const shelfBar = el("div", "shead");
    const addGroup = button("New group", "shelf-act");
    addGroup.addEventListener("click", () => newGroup(addGroup));
    shelfBar.append(el("span", "spacer"), addGroup);
    shelves.append(shelfBar);

    if (!visible.length && filter) {
      shelves.append(empty("Nothing matches “" + filter + "”."));
    }
  }

  byId("vault-filter").addEventListener("input", (event) => {
    filter = event.currentTarget.value.trim().toLowerCase();
    render();
  });

  document.addEventListener("keydown", (event) => {
    if (event.key === "Escape") closeOverlay();
  });

  window.addEventListener("resize", () => {
    if (openOverlay) place(openOverlay.node, openOverlay.anchor);
  });

  boot();
})();