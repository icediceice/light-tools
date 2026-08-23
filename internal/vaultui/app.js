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
  const maxImportedFileBytes = 1 << 20;

  function show(view) {
    for (const candidate of [pairView, passwordView, vaultView]) {
      candidate.classList.toggle("hidden", candidate !== view);
    }
    logoutButton.classList.toggle("hidden", !token);
  }

  function showView(name) {
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

  function renderPendingImport() {
    const container = byId("secret-import");
    const valueInput = byId("secret-value");
    const active = pendingImport !== null;
    container.classList.toggle("hidden", !active);
    valueInput.disabled = active;
    byId("secret-import-name").textContent = active ? pendingImport.name : "";
    byId("secret-import-size").textContent = active
      ? pendingImport.size.toLocaleString() + " bytes ready to save"
      : "";
  }

  function discardImport(reason = "") {
    importSequence += 1;
    pendingImport = null;
    renderPendingImport();
    if (reason) {
      notice(reason, true);
    }
  }

  function clearSecretDraft() {
    importSequence += 1;
    pendingImport = null;
    byId("secret-file").value = "";
    byId("secret-value").value = "";
    byId("secret-name").value = "";
    renderPendingImport();
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

    const valueInput = byId("secret-value");
    if (valueInput.value !== "") {
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
    if (valueInput.value !== "") {
      notice("The typed value was kept; clear it before importing a file.", true);
      return;
    }

    pendingImport = { text, name: file.name, size: file.size };
    renderPendingImport();
    notice("File loaded locally. Press Save value to store it in the vault.");
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
    clearSecretDraft();
    show(pairView);
    notice("Vault locked. Restart the terminal command to pair again.");
  });

  byId("refresh").addEventListener("click", loadVault);

  async function loadVault() {
    notice("");
    try {
      overview = await api("/api/vault");
      overview.secrets ||= [];
      overview.groups ||= [];
      renderGroupSelect();
      renderGroups();
      renderSecrets();
    } catch {
      notice("Vault metadata could not be loaded.", true);
    }
  }

  function renderGroupSelect() {
    const select = byId("secret-group");
    select.replaceChildren();
    select.append(option("", "No group"));
    for (const group of overview.groups) {
      select.append(option(group, group));
    }
  }

  function option(value, label) {
    const element = document.createElement("option");
    element.value = value;
    element.textContent = label;
    return element;
  }

  byId("secret-form").addEventListener("submit", async (event) => {
    event.preventDefault();
    notice("");
    const valueInput = byId("secret-value");
    const payload = {
      name: byId("secret-name").value,
      value: pendingImport ? pendingImport.text : valueInput.value,
      group: byId("secret-group").value
    };
    try {
      await api("/api/secret/set", { method: "POST", body: JSON.stringify(payload) });
    } catch (error) {
      const expired = String(error.message).includes("unauthorized");
      notice(expired
        ? "Your session expired. The unsaved value is still here; copy it or reselect the file after restarting the vault UI."
        : "Secret could not be saved. The value was kept; check its name, group, and size.", true);
      return;
    }
    clearSecretDraft();
    notice("Secret saved. Its value will not be shown again.");
    await loadVault();
  });

  byId("group-form").addEventListener("submit", async (event) => {
    event.preventDefault();
    const input = byId("group-name");
    try {
      await api("/api/group/add", { method: "POST", body: JSON.stringify({ name: input.value }) });
      input.value = "";
      await loadVault();
    } catch {
      notice("Group could not be added.", true);
    }
  });

  function renderGroups() {
    const container = byId("groups");
    container.replaceChildren();
    if (!overview.groups.length) {
      container.append(empty("No groups yet."));
      return;
    }
    for (const group of overview.groups) {
      const row = document.createElement("div");
      row.className = "row";
      const name = document.createElement("div");
      name.className = "row-title";
      name.textContent = group;
      const actions = document.createElement("div");
      actions.className = "actions";
      const rename = button("Rename", "secondary");
      rename.addEventListener("click", async () => {
        const next = window.prompt("Rename group", group);
        if (!next || next === group) return;
        try {
          await api("/api/group/rename", { method: "POST", body: JSON.stringify({ from: group, to: next }) });
          await loadVault();
        } catch {
          notice("Group could not be renamed. The destination may already exist.", true);
        }
      });
      const remove = button("Delete", "danger");
      remove.addEventListener("click", async () => {
        if (!window.confirm("Delete this group? Secrets will be kept and unassigned.")) return;
        try {
          await api("/api/group/remove", { method: "POST", body: JSON.stringify({ name: group }) });
          await loadVault();
        } catch {
          notice("Group could not be deleted.", true);
        }
      });
      actions.append(rename, remove);
      row.append(name, actions);
      container.append(row);
    }
  }

  function renderSecrets() {
    const container = byId("secrets");
    container.replaceChildren();
    if (!overview.secrets.length) {
      container.append(empty("No secrets saved."));
      return;
    }
    for (const item of overview.secrets) {
      const row = document.createElement("div");
      row.className = "row";
      const main = document.createElement("div");
      main.className = "row-main";
      const title = document.createElement("div");
      title.className = "row-title";
      title.textContent = item.name;
      const meta = document.createElement("div");
      meta.className = "row-meta";
      const updated = item.updated_at ? new Date(item.updated_at).toLocaleString() : "legacy entry";
      meta.textContent = (item.group || "No group") + " · Updated " + updated;
      main.append(title, meta);

      const actions = document.createElement("div");
      actions.className = "actions";
      const group = document.createElement("select");
      group.setAttribute("aria-label", "Group for " + item.name);
      group.append(option("", "No group"));
      for (const name of overview.groups) {
        group.append(option(name, name));
      }
      group.value = item.group || "";
      const assign = button("Assign", "secondary");
      assign.addEventListener("click", async () => {
        try {
          await api("/api/secret/group", {
            method: "POST",
            body: JSON.stringify({ name: item.name, group: group.value })
          });
          await loadVault();
        } catch {
          notice("Secret group could not be changed.", true);
        }
      });
      const remove = button("Delete", "danger");
      remove.addEventListener("click", async () => {
        if (!window.confirm("Delete secret “" + item.name + "”? This cannot be undone.")) return;
        try {
          await api("/api/secret/remove", { method: "POST", body: JSON.stringify({ name: item.name }) });
          await loadVault();
        } catch {
          notice("Secret could not be deleted.", true);
        }
      });
      actions.append(group, assign, remove);
      row.append(main, actions);
      container.append(row);
    }
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

  boot();
})();