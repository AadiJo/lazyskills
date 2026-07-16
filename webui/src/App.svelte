<script lang="ts">
  import { onMount, tick } from "svelte";
  import { api, APIError } from "./lib/api";
  import type { DiscoveredSkill, Preview, PreviewRequest, RegistrySkill, ScanPayload, Scope, Skill, SkillRef, SourceGroup, UpdatePlan } from "./lib/types";

  type DetailTab = "metadata" | "content" | "health" | "visibility";
  type ScopeFilter = "all" | Scope;
  type ActionOption = { id: string; label: string; description: string; dangerous?: boolean };

  let payload: ScanPayload | null = null;
  let loading = true;
  let refreshing = false;
  let error = "";
  let search = "";
  let scope: ScopeFilter = "all";
  let agent = "";
  let selectedKey = "";
  let selectedKeys = new Set<string>();
  let detailTab: DetailTab = "metadata";
  let mobileDetail = false;
  let searchInput: HTMLInputElement;

  let preview: Preview | null = null;
  let previewIdempotencyKey = "";
  let previewBusy = false;
  let previewError = "";
  let actionsOpen = false;
  let helpOpen = false;

  let registryOpen = false;
  let registryQuery = "";
  let registryResults: RegistrySkill[] = [];
  let registryLoading = false;
  let registryError = "";
  let registrySelected = new Set<string>();

  let sourceOpen = false;
  let sourceName = "";
  let sourceSkills: DiscoveredSkill[] = [];
  let sourceLoading = false;
  let sourceError = "";
  let sourceSelected = new Set<string>();

  let contentCache: Record<string, string> = {};
  let contentLoading = "";
  let contentError = "";

  let terminalOpen = false;
  let terminalTitle = "command output";
  let terminalOutput = "";
  let terminalStream = "";
  let receivedStdout = false;
  let receivedStderr = false;
  let terminalStatus = "idle";
  let activeEvents: EventSource | null = null;
  let scanEvents: EventSource | null = null;
  let updatePlan: UpdatePlan | null = null;

  $: allSkills = payload?.result.skills ?? [];
  $: filteredSkills = allSkills.filter((skill) => {
    if (scope !== "all" && skill.scope !== scope) return false;
    if (agent && !skill.visibility?.some((item) => item.agent === agent && item.visible)) return false;
    const query = search.trim().toLowerCase();
    return !query || `${skill.name} ${skill.description} ${sourceFor(skill)}`.toLowerCase().includes(query);
  });
  $: groups = groupSkills(filteredSkills);
  $: selectedSkill = allSkills.find((skill) => keyFor(skill) === selectedKey) ?? null;
  $: selectedRefs = allSkills.filter((skill) => selectedKeys.has(keyFor(skill))).map(refFor);
  $: currentActions = selectedSkill ? actionsFor(selectedSkill) : [];
  $: if (selectedSkill && detailTab === "content") void ensureContent(selectedSkill);

  onMount(() => {
    void loadScan();
    void api.update().then((plan) => updatePlan = plan).catch(() => undefined);
    scanEvents = new EventSource("/api/events");
    const refreshFromEvent = (raw: Event) => {
      const data = JSON.parse((raw as MessageEvent).data);
      if (Number(data.generation) > (payload?.generation ?? 0)) void loadScan();
    };
    scanEvents.addEventListener("ready", refreshFromEvent);
    scanEvents.addEventListener("scan", refreshFromEvent);
    scanEvents.addEventListener("scan-error", (raw) => {
      const data = JSON.parse((raw as MessageEvent).data);
      error = data.error || "Live refresh failed.";
    });
    return () => {
      activeEvents?.close();
      scanEvents?.close();
    };
  });

  async function loadScan() {
    if (payload) refreshing = true;
    else loading = true;
    error = "";
    try {
      const next = await api.scan();
      payload = next;
      const existing = next.result.skills.some((skill) => keyFor(skill) === selectedKey);
      if (!existing) selectedKey = next.result.skills[0] ? keyFor(next.result.skills[0]) : "";
      const valid = new Set(next.result.skills.map(keyFor));
      selectedKeys = new Set([...selectedKeys].filter((key) => valid.has(key)));
    } catch (caught) {
      error = errorMessage(caught);
    } finally {
      loading = false;
      refreshing = false;
    }
  }

  function keyFor(skill: Skill) { return `${skill.scope}:${skill.name}`; }
  function refFor(skill: Skill): SkillRef { return { scope: skill.scope, name: skill.name }; }
  function sourceFor(skill: Skill) {
    const lock = skill.local_lock ?? skill.global_lock;
    return lock?.source || lock?.sourceUrl || "untracked";
  }
  function sourceGroup(label: string) { return payload?.sources?.find((item) => item.label === label); }
  function groupSkills(skills: Skill[]) {
    const map = new Map<string, Skill[]>();
    for (const skill of skills) {
      const source = sourceFor(skill);
      map.set(source, [...(map.get(source) ?? []), skill]);
    }
    return [...map.entries()].sort(([left], [right]) => left.localeCompare(right));
  }
  function errorMessage(caught: unknown) { return caught instanceof Error ? caught.message : String(caught); }
  function statusFor(skill: Skill) {
    if (skill.health_issues?.some((issue) => issue.severity === "error")) return { glyph: "✗", level: "bad", label: "error" };
    if (skill.health_issues?.length) return { glyph: "⚠", level: "warn", label: "warning" };
    if (skill.disabled) return { glyph: "○", level: "warn", label: "disabled" };
    return { glyph: "✓", level: "good", label: "healthy" };
  }
  function actionsFor(skill: Skill): ActionOption[] {
    const output: ActionOption[] = [
      { id: "reinstall_update", label: "Reinstall / update", description: "Refresh this skill from its locked source." }
    ];
    const active = skill.observed_paths?.some((item) => item.status !== "disabled" && item.status !== "broken_symlink") ?? false;
    const disabled = skill.observed_paths?.some((item) => item.status === "disabled") ?? false;
    if (active) output.push({ id: "disable_skill", label: agent ? `Disable for ${agentLabel(agent)}` : "Disable", description: "Move active skill paths to the disabled shelf." });
    if (disabled) output.push({ id: "enable_skill", label: agent ? `Enable for ${agentLabel(agent)}` : "Enable", description: "Restore disabled paths from the shelf." });
    if (skill.health_issues?.some((issue) => issue.type === "lock_without_files")) output.push({ id: "prune_lock", label: "Prune lock", description: "Remove the stale lock entry." });
    if (skill.observed_paths?.some((item) => item.status === "broken_symlink")) output.push({ id: "delete_broken_symlink", label: "Delete broken link", description: "Revalidate and remove dangling symlinks only.", dangerous: true });
    output.push({ id: "remove", label: "Remove", description: "Delete this installed skill.", dangerous: true });
    return output;
  }
  function agentLabel(name: string) { return payload?.result.agents?.find((item) => item.name === name)?.display ?? name; }

  function selectSkill(skill: Skill) {
    selectedKey = keyFor(skill);
    mobileDetail = true;
  }
  function toggleBulk(event: MouseEvent, skill: Skill) {
    event.stopPropagation();
    const next = new Set(selectedKeys);
    const key = keyFor(skill);
    if (next.has(key)) next.delete(key); else next.add(key);
    selectedKeys = next;
  }
  function toggleCurrent() {
    if (!selectedSkill) return;
    const next = new Set(selectedKeys);
    const key = keyFor(selectedSkill);
    if (next.has(key)) next.delete(key); else next.add(key);
    selectedKeys = next;
  }

  async function ensureContent(skill: Skill) {
    const key = keyFor(skill);
    if (contentCache[key] !== undefined || contentLoading === key) return;
    contentLoading = key;
    contentError = "";
    try {
      const result = await api.content(skill.scope, skill.name);
      contentCache = { ...contentCache, [key]: result.html };
    } catch (caught) {
      contentError = errorMessage(caught);
    } finally {
      if (contentLoading === key) contentLoading = "";
    }
  }

  async function requestAction(input: PreviewRequest) {
    if (payload?.read_only) {
      previewError = "This server is running in read-only mode.";
      return;
    }
    previewBusy = true;
    previewError = "";
    actionsOpen = false;
    registryOpen = false;
    sourceOpen = false;
    preview = null;
    previewIdempotencyKey = "";
    try {
      preview = await api.preview(input);
      previewIdempotencyKey = globalThis.crypto?.randomUUID?.() ?? `request-${Date.now()}-${Math.random().toString(16).slice(2)}`;
    } catch (caught) {
      previewError = errorMessage(caught);
    } finally {
      previewBusy = false;
    }
  }
  function requestSkillAction(id: string) {
    if (!selectedSkill) return;
    void requestAction({ action: id, skills: [refFor(selectedSkill)], agent: agent || undefined });
  }
  function requestBulk(id: "bulk_reinstall_update" | "bulk_remove" | "bulk_enable_skill" | "bulk_disable_skill") {
    if (!selectedRefs.length) return;
    void requestAction({ action: id, skills: selectedRefs, agent: agent || undefined });
  }
  function requestSourceToggle(skills: Skill[], id: "bulk_enable_skill" | "bulk_disable_skill") {
    void requestAction({ action: id, skills: skills.map(refFor), agent: agent || undefined });
  }
  function requestInstall(skill: RegistrySkill | DiscoveredSkill, global: boolean) {
    if (!skill.candidate_id) return;
    void requestAction({ action: "install_skill", candidate_ids: [skill.candidate_id], global });
  }
  function requestInstallCandidates(ids: Set<string>, global: boolean) {
    if (!ids.size) return;
    void requestAction({ action: "bulk_install_skills", candidate_ids: [...ids], global });
  }
  function toggleCandidate(current: Set<string>, id: string) {
    const next = new Set(current);
    if (next.has(id)) next.delete(id); else next.add(id);
    return next;
  }

  async function executePreview() {
    if (!preview) return;
    previewBusy = true;
    previewError = "";
    try {
      if (!previewIdempotencyKey) {
        previewIdempotencyKey = globalThis.crypto?.randomUUID?.() ?? `request-${Date.now()}-${Math.random().toString(16).slice(2)}`;
      }
      const job = await api.execute(preview, previewIdempotencyKey);
      terminalOpen = true;
      terminalTitle = preview.title;
      terminalOutput = `$ ${preview.command}\n\n${job.existing ? "reconnected to existing job" : "queued"}\n`;
      terminalStream = "";
      receivedStdout = false;
      receivedStderr = false;
      terminalStatus = "running";
      preview = null;
      previewIdempotencyKey = "";
      streamJob(job.events_url);
    } catch (caught) {
      previewError = errorMessage(caught);
      if (caught instanceof APIError && caught.status === 409) await loadScan();
    } finally {
      previewBusy = false;
    }
  }

  function streamJob(eventsURL: string) {
    activeEvents?.close();
    const stream = new EventSource(eventsURL);
    activeEvents = stream;
    const scrollTerminal = () => {
      void tick().then(() => document.querySelector(".terminal-body")?.scrollTo(0, 1000000));
    };
    const appendMessage = (line: string) => {
      if (terminalOutput && !terminalOutput.endsWith("\n")) terminalOutput += "\n";
      terminalOutput += `${line}\n`;
      terminalStream = "";
      scrollTerminal();
    };
    const appendOutput = (kind: string, data: string) => {
      if (terminalStream !== kind) {
        if (terminalOutput && !terminalOutput.endsWith("\n")) terminalOutput += "\n";
        if (kind !== "stdout") terminalOutput += `[${kind}] `;
        terminalStream = kind;
      }
      terminalOutput += data;
      if (kind === "stdout") receivedStdout = true;
      if (kind === "stderr") receivedStderr = true;
      scrollTerminal();
    };
    stream.addEventListener("started", () => appendMessage("started"));
    stream.addEventListener("progress", (raw) => {
      const data = JSON.parse((raw as MessageEvent).data);
      appendMessage(`[${data.current}/${data.total}] ${data.program}`);
    });
    stream.addEventListener("output", (raw) => {
      const data = JSON.parse((raw as MessageEvent).data);
      appendOutput(data.stream, data.data);
    });
    stream.addEventListener("replay-reset", (raw) => {
      const data = JSON.parse((raw as MessageEvent).data);
      appendMessage(`[notice] ${data.message}`);
    });
    stream.addEventListener("complete", (raw) => {
      const data = JSON.parse((raw as MessageEvent).data);
      const result = data.result ?? {};
      if (!receivedStdout && result.stdout) appendOutput("stdout", result.stdout);
      if (!receivedStderr && result.stderr) appendOutput("stderr", result.stderr);
      if (result.Err || result.err || result.error) appendMessage(`[error] ${result.Err || result.err || result.error}`);
      appendMessage(result.ExitCode === 0 || result.exit_code === 0 ? "completed" : `failed (exit ${result.ExitCode ?? result.exit_code ?? "unknown"})`);
      terminalStatus = result.ExitCode === 0 || result.exit_code === 0 ? "complete" : "failed";
      stream.close();
      activeEvents = null;
      selectedKeys = new Set();
      void loadScan();
    });
    stream.onerror = () => {
      if (terminalStatus === "running") appendMessage("stream disconnected, retrying...");
    };
  }

  async function searchRegistry() {
    registryLoading = true;
    registryError = "";
    registrySelected = new Set();
    try {
      registryResults = (await api.registry(registryQuery.trim())).results;
    } catch (caught) {
      registryError = errorMessage(caught);
    } finally {
      registryLoading = false;
    }
  }
  async function browseSource(group: SourceGroup) {
    sourceName = group.label;
    sourceOpen = true;
    sourceSkills = [];
    sourceSelected = new Set();
    sourceError = "";
    sourceLoading = true;
    try {
      sourceSkills = (await api.source(group.id)).skills;
    } catch (caught) {
      sourceError = errorMessage(caught);
    } finally {
      sourceLoading = false;
    }
  }

  function cycleAgent() {
    const agents = ["", ...(payload?.result.agents?.filter((item) => item.detected).map((item) => item.name) ?? [])];
    agent = agents[(agents.indexOf(agent) + 1) % agents.length];
  }
  function cycleScope() { scope = scope === "all" ? "project" : scope === "project" ? "global" : "all"; }
  function moveSelection(direction: number) {
    if (!filteredSkills.length) return;
    const index = filteredSkills.findIndex((skill) => keyFor(skill) === selectedKey);
    const next = Math.max(0, Math.min(filteredSkills.length - 1, index + direction));
    selectedKey = keyFor(filteredSkills[next]);
    document.querySelector(`[data-skill-key="${CSS.escape(selectedKey)}"]`)?.scrollIntoView({ block: "nearest" });
  }
  function modalOpen() { return Boolean(preview || actionsOpen || helpOpen || registryOpen || sourceOpen); }
  function closeModals() {
    preview = null;
    previewIdempotencyKey = "";
    actionsOpen = false;
    helpOpen = false;
    registryOpen = false;
    sourceOpen = false;
    previewError = "";
  }

  function dialog(node: HTMLElement) {
    const previous = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const backgrounds = [...document.querySelectorAll<HTMLElement>(".app-shell, .terminal-drawer")];
    const inertBefore = backgrounds.map((item) => item.inert);
    backgrounds.forEach((item) => item.inert = true);
    if (!node.hasAttribute("tabindex")) node.tabIndex = -1;
    void tick().then(() => (node.querySelector<HTMLElement>("[data-autofocus], input, button, select, textarea, [tabindex]:not([tabindex='-1'])") ?? node).focus());
    const trap = (event: KeyboardEvent) => {
      if (event.key !== "Tab") return;
      const items = [...node.querySelectorAll<HTMLElement>("button:not(:disabled), input:not(:disabled), select:not(:disabled), textarea:not(:disabled), [tabindex]:not([tabindex='-1'])")].filter((item) => item.offsetParent !== null);
      if (!items.length) return;
      const first = items[0], last = items[items.length - 1];
      if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus(); }
      else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus(); }
    };
    node.addEventListener("keydown", trap);
    return { destroy() {
      node.removeEventListener("keydown", trap);
      backgrounds.forEach((item, index) => item.inert = inertBefore[index]);
      previous?.focus();
    }};
  }
  function handleKey(event: KeyboardEvent) {
    const target = event.target as HTMLElement;
    const editing = target instanceof HTMLInputElement || target instanceof HTMLSelectElement || target instanceof HTMLTextAreaElement;
    if (event.key === "Escape") {
      if (modalOpen()) closeModals();
      else if (mobileDetail) mobileDetail = false;
      else if (terminalOpen) terminalOpen = false;
      return;
    }
    if (editing || modalOpen()) return;
    if (event.key === "/") { event.preventDefault(); searchInput?.focus(); }
    else if (event.key === "a") { event.preventDefault(); cycleAgent(); }
    else if (event.key === "f") { event.preventDefault(); cycleScope(); }
    else if (event.key === "ArrowDown" || event.key === "j") { event.preventDefault(); moveSelection(1); }
    else if (event.key === "ArrowUp" || event.key === "k") { event.preventDefault(); moveSelection(-1); }
    else if (event.key === " ") { event.preventDefault(); toggleCurrent(); }
    else if (event.key === "Enter" && selectedSkill) { event.preventDefault(); mobileDetail = true; }
    else if (event.key === "c" && selectedSkill) { event.preventDefault(); actionsOpen = true; }
    else if (event.key === "u" && selectedSkill) { event.preventDefault(); requestSkillAction("reinstall_update"); }
    else if (event.key === "x" && selectedSkill) { event.preventDefault(); requestSkillAction("remove"); }
    else if (event.key === "n") { event.preventDefault(); registryOpen = true; }
    else if (event.key === "?") { event.preventDefault(); helpOpen = true; }
  }
</script>

<svelte:window on:keydown={handleKey} />

<main class="app-shell">
  <div>
    <header class="topbar">
      <div class="brand"><strong>lazyskills</strong><span>web</span></div>
      <div class="project-path" title={payload?.result.cwd ?? ""}>{payload?.result.cwd ?? "scanning..."}</div>
      <div class="top-spacer"></div>
      <div class="search-wrap">
        <input bind:this={searchInput} bind:value={search} class="input" aria-label="Search skills" placeholder="filter skills" />
        <span class="hint">/</span>
      </div>
      <div class="segmented" aria-label="Scope filter">
        {#each ["all", "project", "global"] as item}
          <button class:active={scope === item} on:click={() => scope = item as ScopeFilter}>{item}</button>
        {/each}
      </div>
      <select bind:value={agent} class="select" aria-label="Agent filter">
        <option value="">all agents</option>
        {#each payload?.result.agents?.filter((item) => item.detected) ?? [] as item}
          <option value={item.name}>{item.display}</option>
        {/each}
      </select>
      <button class="button compact" on:click={() => registryOpen = true}>{payload?.read_only ? "+ browse" : "+ install"}</button>
      <button class="button compact" on:click={loadScan} disabled={refreshing}>{refreshing ? "scanning" : "refresh"}</button>
      <button class="button compact" on:click={() => helpOpen = true}>?</button>
    </header>
    {#if updatePlan?.status === "available"}
      <div class="update-banner">
        <span>Update {updatePlan.latest} is available.</span>
        <code>{updatePlan.command_preview || updatePlan.reason}</code>
        {#if updatePlan.release_url}<a href={updatePlan.release_url} target="_blank" rel="noreferrer">release notes</a>{/if}
      </div>
    {/if}
  </div>

  {#if error}<div class="error-line">{error} <button class="button compact" on:click={loadScan}>retry</button></div>{/if}

  <section class="workspace" aria-busy={loading}>
    <aside class="inventory">
      <div class="pane-header">
        <div class="pane-heading">
          <strong>skills</strong>
          <span>{filteredSkills.length} shown / {allSkills.length} total</span>
        </div>
        {#if selectedKeys.size}
          <div class="detail-actions" style="justify-content:flex-start;margin-top:7px">
            <button class="button compact primary" disabled={payload?.read_only} on:click={() => requestBulk("bulk_reinstall_update")}>update {selectedKeys.size}</button>
            <button class="button compact" disabled={payload?.read_only} on:click={() => requestBulk("bulk_enable_skill")}>enable</button>
            <button class="button compact" disabled={payload?.read_only} on:click={() => requestBulk("bulk_disable_skill")}>disable</button>
            <button class="button compact danger" disabled={payload?.read_only} on:click={() => requestBulk("bulk_remove")}>remove {selectedKeys.size}</button>
            <button class="button compact ghost" on:click={() => selectedKeys = new Set()}>clear</button>
          </div>
        {/if}
      </div>
      <div class="inventory-scroll">
        {#if loading}
          <div class="plain-state">scanning...</div>
        {:else if !filteredSkills.length}
          <div class="plain-state">no skills match</div>
        {:else}
          {#each groups as [source, skills]}
            {@const groupSource = sourceGroup(source)}
            <section class="group">
              <div class="group-header">
                <span class="source" title={source}>{source}</span>
                <span class="count">{skills.length}</span>
                {#if skills.some((skill) => skill.observed_paths?.some((item) => item.status === "disabled"))}
                  <button class="button compact ghost" disabled={payload?.read_only} title="Enable disabled skills from this source" on:click={() => requestSourceToggle(skills, "bulk_enable_skill")}>enable</button>
                {/if}
                {#if skills.some((skill) => skill.observed_paths?.some((item) => item.status !== "disabled" && item.status !== "broken_symlink"))}
                  <button class="button compact ghost" disabled={payload?.read_only} title="Disable active skills from this source" on:click={() => requestSourceToggle(skills, "bulk_disable_skill")}>disable</button>
                {/if}
                {#if groupSource?.discoverable}
                  <button class="button compact ghost" title="Discover more skills from this source" on:click={() => browseSource(groupSource)}>browse</button>
                {/if}
              </div>
              {#each skills as skill}
                {@const status = statusFor(skill)}
                <div class="skill-row" class:selected={selectedKey === keyFor(skill)} data-skill-key={keyFor(skill)}>
                  <input class="checkbox" type="checkbox" checked={selectedKeys.has(keyFor(skill))} aria-label={`Select ${skill.name}`} on:click={(event) => toggleBulk(event, skill)} />
                  <button class="skill-open" aria-label={`Open ${skill.name}`} on:click={() => selectSkill(skill)}>
                    <span class="skill-main">
                      <span class="name">{skill.name}</span>
                      <span class="description">{skill.description || "no description"}</span>
                    </span>
                    <span style="display:flex;align-items:center;gap:5px">
                      <span class={`scope ${skill.scope}`}>{skill.scope === "project" ? "prj" : "gbl"}</span>
                      <span class={`status-glyph ${status.level}`} title={status.label}>{status.glyph}</span>
                    </span>
                  </button>
                </div>
              {/each}
            </section>
          {/each}
        {/if}
      </div>
    </aside>

    <article class:mobile-open={mobileDetail} class="detail">
      {#if selectedSkill}
        <div class="detail-header">
          <div class="detail-title">
            <div class="detail-title-main">
              <h1>{selectedSkill.name} <span class={`scope ${selectedSkill.scope}`}>{selectedSkill.scope}</span></h1>
              <p>{selectedSkill.description || "No description in SKILL.md."}</p>
            </div>
            <div class="detail-actions">
              <button class="button compact ghost" on:click={() => mobileDetail = false}>back</button>
              <button class="button compact" on:click={() => actionsOpen = true} disabled={payload?.read_only}>actions <span>c</span></button>
              <button class="button compact primary" on:click={() => requestSkillAction("reinstall_update")} disabled={payload?.read_only}>update <span>u</span></button>
              <button class="button compact danger" on:click={() => requestSkillAction("remove")} disabled={payload?.read_only}>remove <span>x</span></button>
            </div>
          </div>
        </div>
        <nav class="tabs" aria-label="Skill details">
          {#each [["metadata", "metadata"], ["content", "SKILL.md"], ["health", `health ${selectedSkill.health_issues?.length || 0}`], ["visibility", "agents"]] as [id, label]}
            <button class="tab" class:active={detailTab === id} on:click={() => detailTab = id as DetailTab}>{label}</button>
          {/each}
        </nav>
        <div class="detail-scroll">
          <div class="detail-content">
            {#if detailTab === "metadata"}
              <h2 class="section-title">identity</h2>
              <div class="facts">
                <div class="fact-key">source</div><div class="fact-value">{sourceFor(selectedSkill)}</div>
                <div class="fact-key">scope</div><div class="fact-value">{selectedSkill.scope}</div>
                <div class="fact-key">canonical path</div><div class="fact-value path">{selectedSkill.canonical_path || "none"}</div>
                <div class="fact-key">SKILL.md</div><div class="fact-value path">{selectedSkill.skill_path || "missing"}</div>
                <div class="fact-key">state</div><div class="fact-value">{selectedSkill.disabled ? "disabled" : "active"}</div>
              </div>
              <h2 class="section-title">observed paths</h2>
              {#if selectedSkill.observed_paths?.length}
                <div class="facts">
                  {#each selectedSkill.observed_paths ?? [] as observed}
                    <div class="fact-key">{observed.agent} / {observed.status}</div>
                    <div class="fact-value path">{observed.path}{observed.target_path ? ` -> ${observed.target_path}` : ""}</div>
                  {/each}
                </div>
              {:else}<div class="plain-state">no skill files observed</div>{/if}
              <h2 class="section-title">lock data</h2>
              <pre class="pre">{JSON.stringify(selectedSkill.local_lock ?? selectedSkill.global_lock ?? {}, null, 2)}</pre>
            {:else if detailTab === "content"}
              {#if contentLoading === keyFor(selectedSkill)}
                <div class="plain-state">rendering SKILL.md...</div>
              {:else if contentError}
                <div class="error-line">{contentError}</div>
              {:else if contentCache[keyFor(selectedSkill)]}
                <div class="markdown">{@html contentCache[keyFor(selectedSkill)]}</div>
              {:else}
                <div class="plain-state">SKILL.md is empty or unavailable</div>
              {/if}
            {:else if detailTab === "health"}
              {#if selectedSkill.health_issues?.length}
                <div class="issues">
                  {#each selectedSkill.health_issues as issue}
                    <div class:error={issue.severity === "error"} class="issue">
                      <div class="issue-level">{issue.severity}</div>
                      <div class="issue-body"><strong>{issue.type.replaceAll("_", " ")}</strong><p>{issue.message}{issue.path ? ` (${issue.path})` : ""}</p></div>
                    </div>
                  {/each}
                </div>
              {:else}<div class="plain-state">no health issues</div>{/if}
            {:else}
              <div class="matrix-wrap">
                <table class="matrix">
                  <thead><tr><th>agent</th><th>visible</th><th>status</th><th>reason</th><th>path</th></tr></thead>
                  <tbody>
                    {#each selectedSkill.visibility ?? [] as item}
                      <tr>
                        <td>{item.display}</td>
                        <td class={item.visible ? "visible" : "hidden"}>{item.visible ? "✓ yes" : "✗ no"}</td>
                        <td>{item.status || "-"}</td>
                        <td>{item.reason.replaceAll("_", " ")}</td>
                        <td class="path">{item.path || "-"}</td>
                      </tr>
                    {/each}
                  </tbody>
                </table>
              </div>
            {/if}
          </div>
        </div>
      {:else}
        <div class="plain-state">select a skill to inspect it</div>
      {/if}
    </article>
  </section>

  <footer class="footer">
    <span class="status">{refreshing ? "scanning" : error ? "scan failed" : "ready"}</span>
    <span>{allSkills.length} skills</span>
    <span>{payload?.result.agents?.filter((item) => item.detected).length ?? 0} agents</span>
    {#if selectedKeys.size}<span>{selectedKeys.size} selected</span>{/if}
    {#if payload?.read_only}<span style="color:var(--amber)">read-only</span>{/if}
    <span class="footer-spacer"></span>
    <span class="keys"><span class="key">/</span> search &nbsp; <span class="key">a</span> agent &nbsp; <span class="key">f</span> scope &nbsp; <span class="key">space</span> select &nbsp; <span class="key">?</span> help</span>
  </footer>
</main>

{#if previewBusy && !preview}<div class="overlay"><div class="modal" role="dialog" aria-modal="true" aria-label="Building command preview" use:dialog><div class="modal-body plain-state" role="status" aria-live="polite">building command preview...</div></div></div>{/if}

{#if preview}
  <div class="overlay" role="presentation" on:click={(event) => event.target === event.currentTarget && closeModals()}>
    <div class="modal" role="dialog" aria-modal="true" aria-label={preview.title} use:dialog>
      <div class="modal-header"><h2>{preview.title}</h2><button class="button compact ghost" on:click={closeModals}>close</button></div>
      <div class="modal-body">
        <p class="modal-copy">{preview.description}</p>
        <h3 class="section-title">exact command</h3>
        <pre class="command">{preview.command}</pre>
        {#if preview.dangerous}<p class="modal-copy" style="color:var(--red);margin-top:10px">This action deletes local skill state. Review the command before continuing.</p>{/if}
        {#if previewError}<div class="error-line">{previewError}</div>{/if}
      </div>
      <div class="modal-footer"><button class="button" on:click={closeModals}>cancel</button><button class:danger={preview.dangerous} class:primary={!preview.dangerous} class="button" disabled={previewBusy} on:click={executePreview}>{previewBusy ? "starting..." : preview.dangerous ? "confirm destructive action" : "confirm and run"}</button></div>
    </div>
  </div>
{/if}

{#if actionsOpen && selectedSkill}
  <div class="overlay" role="presentation" on:click={(event) => event.target === event.currentTarget && closeModals()}>
    <div class="modal" role="dialog" aria-modal="true" aria-label="Skill actions" use:dialog>
      <div class="modal-header"><h2>actions for {selectedSkill.name}</h2><button class="button compact ghost" on:click={closeModals}>close</button></div>
      <div class="modal-body"><div class="action-list">
        {#each currentActions as action}
          <button class="action-row" on:click={() => requestSkillAction(action.id)}><div><strong style:color={action.dangerous ? "var(--red)" : ""}>{action.label}</strong><span>{action.description}</span></div><kbd>{action.id === "reinstall_update" ? "u" : action.id === "remove" ? "x" : "enter"}</kbd></button>
        {/each}
      </div></div>
      <div class="modal-footer"><button class="button" on:click={closeModals}>cancel</button></div>
    </div>
  </div>
{/if}

{#if registryOpen}
  <div class="overlay" role="presentation" on:click={(event) => event.target === event.currentTarget && closeModals()}>
    <div class="modal wide" role="dialog" aria-modal="true" aria-label="Find skills" use:dialog>
      <div class="modal-header"><h2>find skills on skills.sh</h2><button class="button compact ghost" on:click={closeModals}>close</button></div>
      <div class="modal-body">
        <form class="registry-form" on:submit|preventDefault={searchRegistry}><input class="input" bind:value={registryQuery} minlength="2" placeholder="search registry" aria-label="Registry query" data-autofocus /><button class="button primary" disabled={registryLoading || registryQuery.trim().length < 2}>{registryLoading ? "searching..." : "search"}</button></form>
        {#if registryError}<div class="error-line">{registryError}</div>{/if}
        {#if !registryLoading && registryQuery && !registryResults.length && !registryError}<div class="plain-state">no registry results</div>{/if}
        {#if registrySelected.size}
          <div class="selection-actions"><span>{registrySelected.size} selected</span><button class="button compact primary" disabled={payload?.read_only} on:click={() => requestInstallCandidates(registrySelected, false)}>install to project</button><button class="button compact" disabled={payload?.read_only} on:click={() => requestInstallCandidates(registrySelected, true)}>install globally</button></div>
        {/if}
        <div class="results">
          {#each registryResults as skill}
            <div class="result"><input class="checkbox" type="checkbox" aria-label={`Select ${skill.display_name || skill.slug}`} disabled={payload?.read_only || skill.invalid || skill.installed || !skill.candidate_id} checked={skill.candidate_id ? registrySelected.has(skill.candidate_id) : false} on:change={() => skill.candidate_id && (registrySelected = toggleCandidate(registrySelected, skill.candidate_id))} /><div class="result-main"><strong>{skill.display_name || skill.slug}</strong><p>{skill.source} / {skill.slug} / {skill.installs.toLocaleString()} installs{skill.reason ? ` / ${skill.reason}` : ""}</p></div><div class="result-actions">{#if skill.installed}<span class="installed-label">installed</span>{:else}<button class="button compact primary" disabled={payload?.read_only || skill.invalid || !skill.candidate_id} on:click={() => requestInstall(skill, false)}>project</button><button class="button compact" disabled={payload?.read_only || skill.invalid || !skill.candidate_id} on:click={() => requestInstall(skill, true)}>global</button>{/if}</div></div>
          {/each}
        </div>
      </div>
      <div class="modal-footer"><button class="button" on:click={closeModals}>close</button></div>
    </div>
  </div>
{/if}

{#if sourceOpen}
  <div class="overlay" role="presentation" on:click={(event) => event.target === event.currentTarget && closeModals()}>
    <div class="modal wide" role="dialog" aria-modal="true" aria-label="Source discovery" use:dialog>
      <div class="modal-header"><h2>skills in {sourceName}</h2><button class="button compact ghost" on:click={closeModals}>close</button></div>
      <div class="modal-body">
        {#if sourceLoading}<div class="plain-state">cloning and scanning source...</div>{/if}
        {#if sourceError}<div class="error-line">{sourceError}</div>{/if}
        {#if !sourceLoading && !sourceError && !sourceSkills.length}<div class="plain-state">no additional skills found</div>{/if}
        {#if sourceSelected.size}
          <div class="selection-actions"><span>{sourceSelected.size} selected</span><button class="button compact primary" disabled={payload?.read_only} on:click={() => requestInstallCandidates(sourceSelected, false)}>install to project</button><button class="button compact" disabled={payload?.read_only} on:click={() => requestInstallCandidates(sourceSelected, true)}>install globally</button></div>
        {/if}
        <div class="results">
          {#each sourceSkills as skill}
            <div class="result"><input class="checkbox" type="checkbox" aria-label={`Select ${skill.name}`} disabled={payload?.read_only || skill.installed} checked={skill.candidate_id ? sourceSelected.has(skill.candidate_id) : false} on:change={() => skill.candidate_id && (sourceSelected = toggleCandidate(sourceSelected, skill.candidate_id))} /><div class="result-main"><strong>{skill.name}</strong><p>{skill.description || skill.skill_path}</p></div><div class="result-actions">{#if skill.installed}<span class="installed-label">installed</span>{:else}<button class="button compact primary" disabled={payload?.read_only} on:click={() => requestInstall(skill, false)}>project</button><button class="button compact" disabled={payload?.read_only} on:click={() => requestInstall(skill, true)}>global</button>{/if}</div></div>
          {/each}
        </div>
      </div>
      <div class="modal-footer"><button class="button" on:click={closeModals}>close</button></div>
    </div>
  </div>
{/if}

{#if helpOpen}
  <div class="overlay" role="presentation" on:click={(event) => event.target === event.currentTarget && closeModals()}>
    <div class="modal" role="dialog" aria-modal="true" aria-label="Keyboard shortcuts" use:dialog>
      <div class="modal-header"><h2>keyboard shortcuts</h2><button class="button compact ghost" on:click={closeModals}>close</button></div>
      <div class="modal-body"><dl class="help-grid">
        <dt>/</dt><dd>focus skill search</dd><dt>a</dt><dd>cycle detected agent filters</dd><dt>f</dt><dd>cycle all, project, and global scopes</dd><dt>j / k</dt><dd>move through visible skills</dd><dt>enter</dt><dd>open selected skill details on narrow screens</dd><dt>c</dt><dd>show actions for the selected skill</dd><dt>space</dt><dd>toggle bulk selection</dd><dt>u</dt><dd>preview reinstall / update</dd><dt>x</dt><dd>preview removal</dd><dt>n</dt><dd>search the registry</dd><dt>esc</dt><dd>close the active panel</dd>
      </dl></div>
      <div class="modal-footer"><button class="button primary" on:click={closeModals}>done</button></div>
    </div>
  </div>
{/if}

{#if previewError && !preview && !previewBusy}<div class="error-line" style="position:fixed;z-index:30;right:9px;bottom:36px;max-width:520px">{previewError} <button class="button compact" on:click={() => previewError = ""}>dismiss</button></div>{/if}

{#if terminalOpen}
  <section class="terminal-drawer" aria-label="Command output">
    <div class="terminal-head"><strong>{terminalTitle}</strong><span>{terminalStatus}</span><span style="flex:1"></span><button class="button compact ghost" on:click={() => terminalOpen = false}>close</button></div>
    <pre class="terminal-body">{terminalOutput}</pre>
  </section>
{/if}
