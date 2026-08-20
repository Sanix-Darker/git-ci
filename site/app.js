const API_ROOT = '/api/v1';

const state = {
  selectedRunId: null,
  runsById: new Map(),
  logsOffset: 0,
  logsTimer: null,
  logAutoscroll: true,
  lastRun: null,
  systemPoll: null,
  webhookEvents: [],
  discoveredWorkflows: [],
  runStatusFilter: 'all',
  runSearch: '',
  cronRuns: [],
  runFilterCounts: {},
};

const el = {
  kpiServiceState: document.getElementById('kpiServiceState'),
  kpiServiceMode: document.getElementById('kpiServiceMode'),
  kpiRunTotal: document.getElementById('kpiRunTotal'),
  kpiRunRunning: document.getElementById('kpiRunRunning'),
  kpiWebhookEvents: document.getElementById('kpiWebhookEvents'),
  kpiCronJobs: document.getElementById('kpiCronJobs'),

  serviceStatus: document.getElementById('serviceStatus'),
  serviceStatusText: document.getElementById('serviceStatusText'),

  pipelineForm: document.getElementById('pipelineForm'),
  pipelineOutput: document.getElementById('pipelineOutput'),
  validateOutput: document.getElementById('validateOutput'),
  validateBtn: document.getElementById('validateBtn'),
  discoverContainer: document.getElementById('discoverContainer'),
  discoverBtn: document.getElementById('discoverBtn'),
  discoverDirectory: document.getElementById('discoverDirectory'),
  strictValidation: document.getElementById('strictValidation'),
  reloadJobsBtn: document.getElementById('reloadJobsBtn'),

  jobsContainer: document.getElementById('jobsContainer'),

  runForm: document.getElementById('runForm'),
  runStartOutput: document.getElementById('runStartOutput'),
  runFromLastBtn: document.getElementById('runFromLastBtn'),
  runWorkdir: document.getElementById('runWorkdir'),

  runsContainer: document.getElementById('runsContainer'),
  refreshRuns: document.getElementById('refreshRuns'),
  runStatusFilter: document.getElementById('runStatusFilter'),
  runSearchInput: document.getElementById('runSearchInput'),
  runSearchClear: document.getElementById('runSearchClear'),
  runFilterCount: document.getElementById('runFilterCount'),

  selectedRunInfo: document.getElementById('selectedRunInfo'),
  runLogs: document.getElementById('runLogs'),
  cancelRun: document.getElementById('cancelRun'),
  webhookContainer: document.getElementById('webhookContainer'),
  refreshWebhookEvents: document.getElementById('refreshWebhookEvents'),
  stackDump: document.getElementById('stackDump'),
  refreshStackDump: document.getElementById('refreshStackDump'),
  featureContractOutput: document.getElementById('featureContractOutput'),
  loadFeatureCatalog: document.getElementById('loadFeatureCatalog'),
  loadWorkflowsContract: document.getElementById('loadWorkflowsContract'),
  loadSecretsContract: document.getElementById('loadSecretsContract'),
  loadCronContract: document.getElementById('loadCronContract'),
  dispatchWorkflow: document.getElementById('dispatchWorkflow'),
  getSecretByName: document.getElementById('getSecretByName'),
  workflowActionOutput: document.getElementById('workflowActionOutput'),
  workflowDispatchWorkdir: document.getElementById('workflowDispatchWorkdir'),
  workflowDispatchFile: document.getElementById('workflowDispatchFile'),
  workflowDispatchRef: document.getElementById('workflowDispatchRef'),
  workflowDispatchRepository: document.getElementById('workflowDispatchRepository'),
  workflowDispatchRepositoryUrl: document.getElementById('workflowDispatchRepositoryUrl'),
  workflowDispatchSecretRefs: document.getElementById('workflowDispatchSecretRefs'),
  workflowIdLookup: document.getElementById('workflowIdLookup'),
  lookupWorkflow: document.getElementById('lookupWorkflow'),
  workflowLookupOutput: document.getElementById('workflowLookupOutput'),
  storeSecret: document.getElementById('storeSecret'),
  refreshSecrets: document.getElementById('refreshSecrets'),
  secretActionOutput: document.getElementById('secretActionOutput'),
  secretName: document.getElementById('secretName'),
  secretValue: document.getElementById('secretValue'),
  secretScope: document.getElementById('secretScope'),
  createCronRun: document.getElementById('createCronRun'),
  refreshCron: document.getElementById('refreshCron'),
  cronActionOutput: document.getElementById('cronActionOutput'),
  cronRunList: document.getElementById('cronRunList'),
  cronName: document.getElementById('cronName'),
  cronWorkflowFile: document.getElementById('cronWorkflowFile'),
  cronInterval: document.getElementById('cronInterval'),
  cronRepository: document.getElementById('cronRepository'),
  cronRef: document.getElementById('cronRef'),
  cronSecretRefs: document.getElementById('cronSecretRefs'),

  systemStatus: document.getElementById('systemStatus'),
  refreshRunLogs: document.getElementById('refreshRunLogs'),
  autoScrollLogs: document.getElementById('autoScrollLogs'),
  runMeta: document.getElementById('runMeta'),
  clearRunLogs: document.getElementById('clearRunLogs'),

  quickRefreshBtn: document.getElementById('quickRefreshBtn'),
  quickLoadPipelineBtn: document.getElementById('quickLoadPipelineBtn'),
  quickDiscoverBtn: document.getElementById('quickDiscoverBtn'),
  quickJobsBtn: document.getElementById('quickJobsBtn'),
  quickWebhookBtn: document.getElementById('quickWebhookBtn'),
  pipelineSubmitBtn: document.getElementById('pipelineSubmitBtn'),
  runSubmitBtn: document.getElementById('runSubmitBtn'),
};

const DEFAULT_RUN_FIELDS = {
  workdir: '.',
  file: '',
  job: '',
  stage: '',
  maxParallel: 4,
  timeout: 30,
  network: 'bridge',
};

function asList(raw) {
  return raw
    .split('\n')
    .map((line) => line.trim())
    .filter(Boolean);
}

function toCSV(value) {
  return asList(value)
    .join(',')
    .split(',')
    .map((item) => item.trim())
    .filter(Boolean);
}

function setText(node, value) {
  if (!node) return;
  node.textContent = value;
  node.classList.remove('result-pulse');
  node.classList.add('result-pulse');
}

function normalizeStatusForDisplay(value) {
  return String(value || 'pending').toLowerCase().replace(/[^a-z]/g, '');
}

async function withBusy(button, label, task) {
  if (!button || !task) {
    return task();
  }

  const originalLabel = button.textContent;
  button.classList.add('is-busy');
  button.disabled = true;
  button.textContent = `${label}…`;

  try {
    return await task();
  } finally {
    button.classList.remove('is-busy');
    button.textContent = originalLabel;
    button.disabled = false;
  }
}

function escapeText(value) {
  return String(value == null ? '' : value)
    .replaceAll('&', '&amp;')
    .replaceAll('<', '&lt;')
    .replaceAll('>', '&gt;')
    .replaceAll('"', '&quot;')
    .replaceAll("'", '&#39;');
}

function nowLabel() {
  return new Date().toLocaleTimeString();
}

function formatIso(value) {
  if (!value) return 'n/a';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return String(value);
  return date.toLocaleString();
}

function normalizeCronWorkdirFallback() {
  return (
    (el.workflowDispatchWorkdir && el.workflowDispatchWorkdir.value.trim()) ||
    (el.discoverDirectory && el.discoverDirectory.value.trim()) ||
    (el.runWorkdir && el.runWorkdir.value && el.runWorkdir.value.trim()) ||
    '.'
  );
}

function normalizeDispatchPayload() {
  return {
    workdir: (el.workflowDispatchWorkdir && el.workflowDispatchWorkdir.value.trim()) || normalizeCronWorkdirFallback(),
    file: (el.workflowDispatchFile && el.workflowDispatchFile.value.trim()) || '.github/workflows/ci.yml',
    ref: el.workflowDispatchRef && el.workflowDispatchRef.value.trim(),
    repository: el.workflowDispatchRepository && el.workflowDispatchRepository.value.trim(),
    repositoryUrl: el.workflowDispatchRepositoryUrl && el.workflowDispatchRepositoryUrl.value.trim(),
    secretRefs: toCSV((el.workflowDispatchSecretRefs && el.workflowDispatchSecretRefs.value) || ''),
    maxLogEntries: 3000,
  };
}

function normalizeSecretPayload() {
  return {
    name: (el.secretName && el.secretName.value.trim()) || '',
    value: (el.secretValue && el.secretValue.value) || '',
    scope: (el.secretScope && el.secretScope.value.trim()) || 'global',
  };
}

function normalizeCronPayload() {
  return {
    name: (el.cronName && el.cronName.value.trim()) || 'cron-workflow',
    workdir: normalizeCronWorkdirFallback(),
    workflowFile: (el.cronWorkflowFile && el.cronWorkflowFile.value.trim()) || '.github/workflows/ci.yml',
    interval: (el.cronInterval && el.cronInterval.value.trim()) || '',
    repository: (el.cronRepository && el.cronRepository.value.trim()) || '',
    ref: (el.cronRef && el.cronRef.value.trim()) || '',
    secretRefs: toCSV((el.cronSecretRefs && el.cronSecretRefs.value) || ''),
  };
}

async function requestJSON(path, init = {}) {
  const response = await fetch(`${API_ROOT}${path}`, {
    headers: {
      Accept: 'application/json',
      'Content-Type': 'application/json',
      ...(init.headers || {}),
    },
    ...init,
  });

  const raw = await response.text();
  let body = null;
  if (raw) {
    try {
      body = JSON.parse(raw);
    } catch {
      body = { raw };
    }
  }

  if (!response.ok) {
    const message = body && body.error ? body.error : `Request failed (${response.status})`;
    const error = new Error(message);
    error.status = response.status;
    error.payload = body;
    throw error;
  }

  return body;
}

function setStatus(kind, label) {
  const dot = el.serviceStatus.querySelector('.dot');
  dot.classList.remove('ok', 'warn', 'bad');
  if (el.kpiServiceState && el.kpiServiceMode) {
    setText(el.kpiServiceState, kind === 'ok' ? 'online' : kind === 'warn' ? 'degraded' : 'offline');
    setText(el.kpiServiceMode, label || kind);
  }

  if (kind === 'ok') {
    dot.classList.add('ok');
    setText(el.serviceStatusText, label || 'API reachable');
    return;
  }

  if (kind === 'warn') {
    dot.classList.add('warn');
    setText(el.serviceStatusText, label || 'Partial data available');
    return;
  }

  dot.classList.add('bad');
  setText(el.serviceStatusText, label || 'API unreachable');
}

function wireSectionNav() {
  const links = Array.from(document.querySelectorAll('.jump-link'));
  if (!links.length) return;

  const updateActiveLink = () => {
    const targetId = window.location.hash ? window.location.hash.substring(1) : 'pipelinePanel';
    links.forEach((link) => {
      const isActive = link.getAttribute('href') === `#${targetId}`;
      link.classList.toggle('jump-link--active', isActive);
    });
  };

  links.forEach((link) => {
    link.addEventListener('click', () => {
      requestAnimationFrame(updateActiveLink);
    });
  });

  window.addEventListener('hashchange', updateActiveLink);
  updateActiveLink();
}

function showError(node, error) {
  const message =
    error && error.payload && (error.payload.error || error.payload.message || error.payload.raw)
      ? error.payload.error || error.payload.message || error.payload.raw
      : error.message;

  setText(node, `Error: ${message}`);
}

function updateDashboardMetrics() {
  const runs = Array.from(state.runsById.values());
  const running = runs.filter((run) => (run.status || '').toLowerCase() === 'running').length;

  setText(el.kpiRunTotal, String(runs.length));
  setText(el.kpiRunRunning, String(running));
  setText(el.kpiWebhookEvents, String(Array.isArray(state.webhookEvents) ? state.webhookEvents.length : 0));
  setText(el.kpiCronJobs, String(Array.isArray(state.cronRuns) ? state.cronRuns.length : 0));

  const counts = new Map([
    ['all', runs.length],
    ['running', 0],
    ['succeeded', 0],
    ['failed', 0],
    ['pending', 0],
    ['canceled', 0],
  ]);

  runs.forEach((run) => {
    const status = normalizeStatusForDisplay(run.status);
    if (counts.has(status)) {
      counts.set(status, counts.get(status) + 1);
    } else if (status === 'completed' || status === 'done') {
      counts.set('succeeded', counts.get('succeeded') + 1);
    } else if (status === 'cancelled') {
      counts.set('canceled', counts.get('canceled') + 1);
    } else {
      counts.set('pending', counts.get('pending') + 1);
    }
  });

  state.runFilterCounts = Object.fromEntries(counts.entries());
  if (el.runFilterCount) {
    const activeFilter = (state.runStatusFilter || 'all').toLowerCase();
    const filteredCount = state.runsById.size
      ? String(
          activeFilter === 'all'
            ? runs.length
            : runs.filter((run) => normalizeStatusForDisplay(run.status) === activeFilter).length
        )
      : '0';

    setText(el.runFilterCount, `${filteredCount} / ${runs.length} runs`);
  }

  if (el.runStatusFilter && el.runStatusFilter.options) {
    const labelByValue = {
      all: 'All',
      running: 'Running',
      succeeded: 'Succeeded',
      failed: 'Failed',
      pending: 'Pending',
      canceled: 'Canceled',
    };

    Array.from(el.runStatusFilter.options).forEach((option) => {
      const value = option.value;
      if (!value) return;
      option.textContent = `${labelByValue[value] || value} (${counts.get(value) || 0})`;
    });
  }
}

function normalizeFormRunPayload() {
  const maybeFile = document.getElementById('runFile').value.trim();
  return {
    workdir: document.getElementById('runWorkdir').value.trim() || DEFAULT_RUN_FIELDS.workdir,
    file: maybeFile || undefined,
    repository: document.getElementById('runRepository').value.trim() || undefined,
    repositoryUrl: document.getElementById('runRepositoryURL').value.trim() || undefined,
    ref: document.getElementById('runRef').value.trim() || undefined,
    job: document.getElementById('runJob').value.trim() || undefined,
    stage: document.getElementById('runStage').value.trim() || undefined,
    only: toCSV(document.getElementById('runOnly').value),
    except: toCSV(document.getElementById('runExcept').value),
    parallel: document.getElementById('runParallel').checked,
    maxParallel: Number(document.getElementById('runMaxParallel').value) || DEFAULT_RUN_FIELDS.maxParallel,
    continueOnErr: document.getElementById('runContinueOnError').checked,
    docker: document.getElementById('runDocker').checked,
    podman: document.getElementById('runPodman').checked,
    dryRun: document.getElementById('runDryRun').checked,
    timeout: Number(document.getElementById('runTimeout').value) || DEFAULT_RUN_FIELDS.timeout,
    env: asList(document.getElementById('runEnv').value),
    envFile: document.getElementById('runEnvFile').value.trim() || undefined,
    noCache: document.getElementById('runNoCache').checked,
    volume: toCSV(document.getElementById('runVolume').value),
    network: document.getElementById('runNetwork').value.trim() || DEFAULT_RUN_FIELDS.network,
    memory: document.getElementById('runMemory').value.trim(),
    cpus: document.getElementById('runCPUs').value.trim(),
    verbose: document.getElementById('runVerbose').checked,
    quiet: document.getElementById('runQuiet').checked,
    autoFetch: document.getElementById('runAutoFetch').checked,
    maxLogEntries: 3000,
  };
}

function normalizePipelineQuery() {
  return {
    workdir: document.getElementById('workdir').value.trim() || DEFAULT_RUN_FIELDS.workdir,
    file: document.getElementById('workflowFile').value.trim(),
  };
}

function pipelineQueryPath(extra = {}) {
  const params = new URLSearchParams();
  const query = normalizePipelineQuery();

  if (query.workdir) {
    params.set('workdir', query.workdir);
  }

  if (query.file) {
    params.set('file', query.file);
  }

  Object.entries(extra).forEach(([key, value]) => {
    if (value !== undefined && value !== '') {
      params.set(key, value);
    }
  });

  return params.toString() ? `?${params.toString()}` : '';
}

function snapshotStatus(status) {
  return `<span class="pill ${status}">${status}</span>`;
}

async function pingHealth() {
  try {
    const data = await requestJSON('/health');
    setStatus('ok', `API reachable • ${data.status} • ${nowLabel()}`);
    return data;
  } catch (error) {
    setStatus('bad', `API unreachable • ${error.message}`);
    throw error;
  }
}

async function refreshWorkspace() {
  await Promise.all([loadPipeline(), loadRuns(), loadJobs(), loadDiscover(), loadSystem(), refreshCronRuns()]);
}

async function loadSystem() {
  try {
    const system = await requestJSON('/system');
    setText(el.systemStatus, JSON.stringify(system, null, 2));
    return system;
  } catch (error) {
    setText(el.systemStatus, `Error loading system snapshot: ${error.message}`);
  }
}

async function loadStackDump() {
  try {
    const payload = await requestJSON('/stack');
    const pretty = payload && payload.stackTrace
      ? `${JSON.stringify({ ...payload, stackTrace: payload.stackTrace }, null, 2)}`
      : JSON.stringify(payload, null, 2);
    setText(el.stackDump, pretty || 'No stack returned.');
  } catch (error) {
    setText(el.stackDump, `Error loading stack dump: ${error.message}`);
  }
}

async function loadFeatureContract(path) {
  const payload = await requestJSON(path);
  setText(el.featureContractOutput, JSON.stringify(payload, null, 2));
}

async function loadFeaturesSummary() {
  await loadFeatureContract('/features');
}

async function loadWorkflowsFeatureContract() {
  await loadFeatureContract('/workflows');
}

async function loadSecretsFeatureContract() {
  await loadFeatureContract('/secrets');
}

async function loadCronRunsFeatureContract() {
  await loadFeatureContract('/cron-runs');
}

async function refreshCronRuns() {
  try {
    const payload = await requestJSON('/cron-runs');
    state.cronRuns = Array.isArray(payload) ? payload : [];
    updateDashboardMetrics();
    renderCronRuns();
    return state.cronRuns;
  } catch (error) {
    if (el.cronRunList) {
      setText(el.cronRunList, `Error loading cron runs: ${error.message}`);
    }
    throw error;
  }
}

function renderCronRuns() {
  if (!el.cronRunList) return;

  if (!Array.isArray(state.cronRuns) || !state.cronRuns.length) {
    el.cronRunList.innerHTML = '<div class="empty">No cron definitions available.</div>';
    return;
  }

  const rows = state.cronRuns
    .map((item) => {
      const status = (item.status || 'active').toLowerCase();
      const statusBadge = `<span class="pill ${status}">${status}</span>`;
      const pausedReason = item.pausedReason ? ` · pause reason: ${escapeText(item.pausedReason)}` : '';
      const workflow = escapeText(item.workflowFile || item.workflow || '(no workflow)');
      const workdir = escapeText(item.workdir || '.');
      const interval = escapeText(item.interval || '-');
      const lastRunAt = item.lastRunAt ? formatIso(item.lastRunAt) : 'never';
      const nextRunAt = item.nextRunAt ? formatIso(item.nextRunAt) : 'n/a';
      const lastRunId = escapeText(item.lastRunId || '-');

      return `
        <article class="jobCard" data-cron-id="${escapeText(item.id)}">
          <p class="jobTitle">${escapeText(item.name || item.id || 'cron-run')}</p>
          <p>${statusBadge} · ${workflow} · ${interval}</p>
          <p class="jobMeta">Workdir: <strong>${workdir}</strong> · Ref: <strong>${escapeText(item.ref || '-')}</strong>${pausedReason}</p>
          <p class="jobMeta">Last: ${lastRunAt} · Next: ${nextRunAt} · Last run: ${lastRunId}</p>
          <div class="runActions">
            <button type="button" class="cronRunNowBtn" data-cron-id="${escapeText(item.id)}">Run now</button>
            <button type="button" class="cronPauseBtn" data-cron-id="${escapeText(item.id)}" ${status === 'paused' ? 'disabled' : ''}>Pause</button>
            <button type="button" class="cronResumeBtn" data-cron-id="${escapeText(item.id)}" ${status !== 'paused' ? 'disabled' : ''}>Resume</button>
            <button type="button" class="cronDeleteBtn" data-cron-id="${escapeText(item.id)}">Delete</button>
          </div>
        </article>
      `;
    })
    .join('');

  el.cronRunList.innerHTML = rows;

  document.querySelectorAll('.cronRunNowBtn').forEach((button) => {
    button.addEventListener('click', async () => {
      const cronId = button.getAttribute('data-cron-id');
      if (!cronId) return;
      await withBusy(button, 'Run', () => runCronById(cronId));
    });
  });

  document.querySelectorAll('.cronPauseBtn').forEach((button) => {
    button.addEventListener('click', async () => {
      const cronId = button.getAttribute('data-cron-id');
      if (!cronId) return;
      await withBusy(button, 'Pause', () => setCronState(cronId, 'pause'));
    });
  });

  document.querySelectorAll('.cronResumeBtn').forEach((button) => {
    button.addEventListener('click', async () => {
      const cronId = button.getAttribute('data-cron-id');
      if (!cronId) return;
      await withBusy(button, 'Resume', () => setCronState(cronId, 'resume'));
    });
  });

  document.querySelectorAll('.cronDeleteBtn').forEach((button) => {
    button.addEventListener('click', async () => {
      const cronId = button.getAttribute('data-cron-id');
      if (!cronId) return;
      await withBusy(button, 'Delete', () => deleteCronById(cronId));
    });
  });
}

async function runCronById(cronId) {
  try {
    const payload = await requestJSON(`/cron-runs/${encodeURIComponent(cronId)}/run`, {
      method: 'POST',
      body: '{}',
    });
    setText(el.cronActionOutput, `${nowLabel()} — cron triggered ${payload.runId || 'n/a'}\n${JSON.stringify(payload, null, 2)}`);
    await loadRuns();
    await refreshCronRuns();
  } catch (error) {
    showError(el.cronActionOutput, error);
  }
}

async function setCronState(cronId, action) {
  const endpoint =
    action === 'pause'
      ? `/cron-runs/${encodeURIComponent(cronId)}/pause?reason=manual`
      : `/cron-runs/${encodeURIComponent(cronId)}/resume`;

  try {
    const payload = await requestJSON(endpoint, {
      method: 'POST',
      body: '{}',
    });
    setText(el.cronActionOutput, `${nowLabel()} — ${action} ${payload.id || cronId}\n${JSON.stringify(payload, null, 2)}`);
    await refreshCronRuns();
  } catch (error) {
    showError(el.cronActionOutput, error);
  }
}

async function deleteCronById(cronId) {
  try {
    const payload = await requestJSON(`/cron-runs/${encodeURIComponent(cronId)}`, {
      method: 'DELETE',
      body: '{}',
    });
    setText(el.cronActionOutput, `${nowLabel()} — deleted ${payload.id || cronId}\n${JSON.stringify(payload, null, 2)}`);
    await refreshCronRuns();
  } catch (error) {
    showError(el.cronActionOutput, error);
  }
}

function renderWebhookEvents(events) {
  if (!el.webhookContainer) return;

  const items = Array.isArray(events) ? events : [];
  if (!items.length) {
    el.webhookContainer.innerHTML = '<div class="empty">No webhook events yet.</div>';
    return;
  }

  const rows = items
    .slice()
    .reverse()
    .map((item) => {
      const status = (item.status || 'unknown').toLowerCase();
      const createdAt = formatIso(item.createdAt);
      const details = [
        `event: ${escapeText(item.event || 'push')}`,
        `repo: ${escapeText(item.repository || 'unknown')}`,
        `ref: ${escapeText(item.ref || '-')}`,
        `commit: ${escapeText(item.commit || '-')}`,
      ]
        .filter(Boolean)
        .join(' · ');

      return `
        <article class="webhookCard">
          <p class="jobTitle">${escapeText(item.provider || 'provider')} — ${escapeText(item.runId || 'no run')}</p>
          <p class="jobMeta">Status: <span class="pill ${status}">${status}</span> · ${createdAt}</p>
          <p class="jobMeta">${details}</p>
          ${item.error ? `<p class="muted">Error: ${escapeText(item.error)}</p>` : ''}
        </article>
      `;
    })
    .join('');

  el.webhookContainer.innerHTML = rows;
}

async function loadWebhookEvents() {
  try {
    const payload = await requestJSON('/webhooks');
    state.webhookEvents = Array.isArray(payload) ? payload : [];
    updateDashboardMetrics();
    renderWebhookEvents(state.webhookEvents);
    return state.webhookEvents;
  } catch (error) {
    if (el.webhookContainer) {
      setText(el.webhookContainer, `Error loading webhook events: ${error.message}`);
    }
    throw error;
  }
}

async function loadPipeline() {
  const query = pipelineQueryPath();
  try {
    const payload = await requestJSON(`/pipelines${query}`);
    setText(el.pipelineOutput, JSON.stringify(payload, null, 2));
    return payload;
  } catch (error) {
    showError(el.pipelineOutput, error);
    throw error;
  }
}

async function validatePipeline() {
  const query = pipelineQueryPath({
    provider: 'auto',
    strict: String(document.getElementById('strictValidation').checked),
  });

  try {
    const payload = await requestJSON(`/validate${query}`);
    setText(el.validateOutput, JSON.stringify(payload, null, 2));
  } catch (error) {
    showError(el.validateOutput, error);
  }
}

async function loadJobs() {
  const query = pipelineQueryPath();
  try {
    const payload = await requestJSON(`/jobs${query}`);
    const jobs = Array.isArray(payload.jobs) ? payload.jobs : [];
    if (payload.file) {
      const runFileInput = document.getElementById('runFile');
      if (!runFileInput.value) {
        runFileInput.value = payload.file;
      }
    }

    if (!jobs.length) {
      el.jobsContainer.innerHTML = '<div class="empty">No jobs found in selected workflow.</div>';
      return;
    }

    const rows = jobs
      .map((job) => {
        const needs = job.needs && job.needs.length ? job.needs.join(', ') : 'none';
        return `
          <article class="jobCard" data-job-name="${job.name}">
            <p class="jobTitle">${job.name}</p>
            <p class="jobMeta">Stage: <strong>${job.stage || '(default)'}</strong> · Runner: <strong>${job.runner || 'default'}</strong> · Needs: <strong>${needs}</strong></p>
            <p class="jobMeta">Steps: <strong>${job.stepCount || 0}</strong> · Script blocks: <strong>${job.scriptCount || 0}</strong></p>
            <button type="button" class="quickRunBtn" data-job-name="${job.name}">Run this job</button>
          </article>
        `;
      })
      .join('');

    el.jobsContainer.innerHTML = rows;

    document.querySelectorAll('.quickRunBtn').forEach((button) => {
      button.addEventListener('click', async () => {
        const name = button.getAttribute('data-job-name') || '';
        if (!name) return;

        const payload = normalizeFormRunPayload();
        payload.job = name;
        payload.stage = '';
        await withBusy(button, 'Run', () => startRunPayload(payload));
      });
    });
  } catch (error) {
    setText(el.jobsContainer, `Error loading jobs: ${error.message}`);
  }
}

async function loadDiscover() {
  const query = normalizePipelineQuery();
  const directory = (el.discoverDirectory && el.discoverDirectory.value.trim()) || '.';
  const params = new URLSearchParams();
  params.set('workdir', query.workdir || '.');
  params.set('directory', directory);

  try {
    const payload = await requestJSON(`/discover?${params.toString()}`);
    state.discoveredWorkflows = Array.isArray(payload.files) ? payload.files : [];
    renderDiscover();
    return payload;
  } catch (error) {
    if (el.discoverContainer) {
      setText(el.discoverContainer, `Error loading workflows: ${error.message}`);
    }
    return { files: [] };
  }
}

function renderDiscover() {
  if (!el.discoverContainer) return;

  if (!state.discoveredWorkflows.length) {
    el.discoverContainer.innerHTML = '<div class="empty">No workflow files detected in directory.</div>';
    return;
  }

  const rows = state.discoveredWorkflows
    .map((file) => {
      const status = file.detected ? 'detected' : 'unparseable';
      const jobs = file.jobs || 0;
      const filePath = file.path || '(unknown)';

      return `
        <article class="workflowCard">
          <p class="jobTitle">${escapeText(filePath)}</p>
          <p class="jobMeta">Provider: <strong>${escapeText(file.provider || 'Unknown')}</strong> · Jobs: <strong>${jobs}</strong> · <span class="pill ${status}">${status}</span></p>
          <button type="button" class="quickLoadWorkflowBtn" data-workflow-file="${encodeURIComponent(filePath)}">Use this workflow</button>
        </article>
      `;
    })
    .join('');

  el.discoverContainer.innerHTML = rows;

  document.querySelectorAll('.quickLoadWorkflowBtn').forEach((button) => {
    button.addEventListener('click', async () => {
      const workflowFile = decodeURIComponent(button.getAttribute('data-workflow-file') || '');
      if (!workflowFile) return;

      const runFileInput = document.getElementById('runFile');
      const workflowFileInput = document.getElementById('workflowFile');

      if (workflowFileInput) workflowFileInput.value = workflowFile;
      if (runFileInput) runFileInput.value = workflowFile;
      setStatus('warn', `Selected workflow ${workflowFile}`);
      await loadJobs();
    });
  });
}

async function loadRuns() {
  try {
    const payload = await requestJSON('/runs');

    state.runsById = new Map(payload.map((session) => [session.id, session]));
    updateDashboardMetrics();
    renderRuns();

    if (!state.selectedRunId && payload[0]) {
      state.selectedRunId = payload[0].id;
      selectRun(payload[0].id);
    } else if (state.selectedRunId && state.runsById.has(state.selectedRunId)) {
      updateSelectedRun(state.selectedRunId, state.runsById.get(state.selectedRunId));
      if (state.runsById.get(state.selectedRunId).status === 'running') {
        await loadRunLogs(state.selectedRunId);
      }
    } else {
      stopLogLoop();
      state.selectedRunId = null;
      state.logsOffset = 0;
      setText(el.selectedRunInfo, 'Select a run to view logs.');
      setText(el.runLogs, 'No logs yet.');
      setText(el.runMeta, 'No run selected.');
    }
  } catch (error) {
    setText(el.runsContainer, `Error loading runs: ${error.message}`);
  }
}

function renderRunCards(sessions) {
  const statusFilter = (state.runStatusFilter || 'all').toLowerCase();
  const searchFilter = (state.runSearch || '').trim().toLowerCase();
  const filtered = sessions
    .filter((session) => statusFilter === 'all' || (session.status || 'pending').toLowerCase() === statusFilter)
    .filter((session) =>
      !searchFilter
        ? true
        : [session.id, session.file, session.workdir, session.ref, session.repository, session.repositoryUrl, ...(session.command || [])]
            .filter(Boolean)
            .join(' ')
            .toLowerCase()
            .includes(searchFilter)
    )
    .sort((left, right) => new Date(right.startedAt).getTime() - new Date(left.startedAt).getTime());

  if (!filtered.length) {
    if (searchFilter || statusFilter !== 'all') {
      el.runsContainer.innerHTML = '<div class="empty">No runs match current filters.</div>';
      return;
    }
    el.runsContainer.innerHTML = '<div class="empty">No runs yet.</div>';
    return;
  }

  const content = filtered
    .map((session) => {
      const command = (session.command || []).join(' ');
      const status = normalizeStatusForDisplay(session.status);
      const active = session.id === state.selectedRunId ? 'runSelect--active' : '';

      return `
        <article class="runCard">
          <button type="button" class="runSelect ${active}" data-run-id="${session.id}">
            <p class="runHead">${escapeText(session.id)}</p>
            <p>${snapshotStatus(status)} ${escapeText(session.file || '(no file)')} • Exit ${session.exitCode}</p>
            <p class="muted">${escapeText(session.startedAt || 'n/a')} • ${escapeText(session.updatedAt || 'n/a')}</p>
            <p class="runCmd">${escapeText(command)}</p>
          </button>
          <div class="runActions">
            <button type="button" class="retryRunBtn" data-run-id="${session.id}" ${session.status === 'running' ? 'disabled' : ''}>Retry</button>
            <button type="button" class="cancelRunBtn" data-run-id="${session.id}" ${session.status === 'running' ? '' : 'disabled'}>Cancel</button>
          </div>
        </article>
      `;
    })
    .join('');

  if (el.runFilterCount) {
    setText(
      el.runFilterCount,
      `${filtered.length} / ${sessions.length} runs`
    );
  }

  el.runsContainer.innerHTML = content;

  document.querySelectorAll('.runSelect').forEach((button) => {
    button.addEventListener('click', () => {
      const runId = button.getAttribute('data-run-id');
      selectRun(runId);
    });
  });

  document.querySelectorAll('.retryRunBtn').forEach((button) => {
    button.addEventListener('click', async (event) => {
      event.stopPropagation();
      const runId = button.getAttribute('data-run-id');
      await withBusy(button, 'Retry', () => retryRun(runId));
    });
  });

  document.querySelectorAll('.cancelRunBtn').forEach((button) => {
    button.addEventListener('click', async (event) => {
      event.stopPropagation();
      const runId = button.getAttribute('data-run-id');
      await withBusy(button, 'Cancel', () => manualCancelRun(runId));
    });
  });
}

function renderRuns() {
  const sessions = Array.from(state.runsById.values());
  renderRunCards(sessions);
}

function setAutoScroll(enabled) {
  state.logAutoscroll = Boolean(enabled);
  if (el.autoScrollLogs) {
    setText(el.autoScrollLogs, `Auto-scroll ${state.logAutoscroll ? 'ON' : 'OFF'}`);
  }
}

function renderRunMeta(run) {
  if (!el.runMeta) return;

  const payload = {
    id: run.id || state.selectedRunId || 'unknown',
    status: run.status || 'pending',
    exitCode: run.exitCode,
    workdir: run.workdir || 'n/a',
    file: run.file || 'n/a',
    ref: run.ref || '',
    repository: run.repository || '',
    repositoryUrl: run.repositoryUrl || '',
    autoFetch: !!run.autoFetch,
    command: run.command || [],
    secretRefs: run.secretRefs || [],
    startedAt: run.startedAt || null,
    updatedAt: run.updatedAt || null,
    finishedAt: run.finishedAt || null,
  };

  setText(el.runMeta, JSON.stringify(payload, null, 2));
}

function startLogLoop(runId) {
  stopLogLoop();
  state.logsOffset = 0;
  setText(el.runLogs, 'Waiting for output…');

  state.logsTimer = setInterval(() => {
    if (state.selectedRunId === runId) {
      loadRunLogs(runId).catch(() => {});
    }
  }, 1500);
}

function stopLogLoop() {
  if (state.logsTimer) {
    clearInterval(state.logsTimer);
    state.logsTimer = null;
  }
}

function updateSelectedRun(runId, run) {
  if (!run) return;

  state.selectedRunId = runId;
  const stateLabel = run.status || 'pending';

  setText(el.selectedRunInfo, `${runId} • ${stateLabel} • file: ${run.file || '(no file)'} • exit: ${run.exitCode}`);
  renderRunMeta(run);
  el.cancelRun.disabled = stateLabel !== 'running';
}

function selectRun(runId) {
  const run = state.runsById.get(runId);
  if (!run) {
    setText(el.selectedRunInfo, `Run ${runId} not found.`);
    return;
  }

  state.selectedRunId = runId;
  state.logsOffset = 0;

  updateSelectedRun(runId, run);

  startLogLoop(runId);
  loadRunLogs(runId);
}

async function loadRunLogs(id) {
  if (!id) return;

  const url = new URL(`${API_ROOT}/runs/${id}/logs`, window.location.origin);
  if (state.logsOffset > 0) {
    url.searchParams.set('offset', String(state.logsOffset));
  }

  try {
    const payload = await requestJSON(`${url.pathname}${url.search}`);
    const lines = payload.lines || [];
    const output = state.logsOffset === 0 ? '' : (el.runLogs.textContent || '');
    const merged = lines.length
      ? `${output}${output && !output.endsWith('\n') ? '\n' : ''}${lines.join('\n')}`
      : output;
    setText(el.runLogs, merged || 'No logs yet.');
    if (typeof payload.totalLines === 'number') {
      state.logsOffset = payload.totalLines;
    }

    const updatedRun = state.runsById.get(id) || { id };
    updateSelectedRun(id, {
      ...updatedRun,
      status: payload.status || updatedRun.status,
      exitCode: typeof payload.exitCode === 'number' ? payload.exitCode : updatedRun.exitCode,
    });

    const run = state.runsById.get(id);
    if (run && run.status !== 'running') {
      stopLogLoop();
    }

    if (el.autoScrollLogs && state.logAutoscroll) {
      requestAnimationFrame(() => {
        if (el.runLogs) {
          el.runLogs.scrollTop = el.runLogs.scrollHeight;
        }
      });
    }
  } catch (error) {
    setText(el.runLogs, `Log error: ${error.message}`);
    stopLogLoop();
  }
}

async function refreshSelectedRunLogs() {
  if (!state.selectedRunId) {
    setText(el.runLogs, 'No selected run to refresh.');
    return;
  }

  await loadRunLogs(state.selectedRunId);
}

function clearRunLogBuffer() {
  state.logsOffset = 0;
  stopLogLoop();
  setText(el.runLogs, 'Log buffer cleared.');
}

async function startRunPayload(payload) {
  const body = {
    ...payload,
    only: payload.only || [],
    except: payload.except || [],
    volume: payload.volume || [],
  };

  const runResp = await requestJSON('/runs', {
    method: 'POST',
    body: JSON.stringify(body),
  });

  state.lastRun = payload;
  setText(el.runStartOutput, `${nowLabel()} — started ${runResp.id}\n${JSON.stringify(runResp, null, 2)}`);

  state.runsById.set(runResp.id, runResp);
  selectRun(runResp.id);
  renderRuns();
  await loadRuns();
}

async function dispatchWorkflowFromUI() {
  try {
    const payload = normalizeDispatchPayload();
    const response = await requestJSON('/workflows', {
      method: 'POST',
      body: JSON.stringify(payload),
    });
    setText(
      el.workflowActionOutput,
      `${nowLabel()} — dispatched ${response.id || '(unknown id)'}\n${JSON.stringify(response, null, 2)}`
    );
    state.runsById.set(response.id, response);
    await loadRuns();
  } catch (error) {
    showError(el.workflowActionOutput, error);
  }
}

async function storeSecretFromUI() {
  const payload = normalizeSecretPayload();
  if (!payload.name || !payload.value) {
    setText(el.secretActionOutput, 'Error: both name and value are required');
    return;
  }

  try {
    const response = await requestJSON('/secrets', {
      method: 'POST',
      body: JSON.stringify(payload),
    });
    setText(el.secretActionOutput, `${nowLabel()} — stored ${response.name || payload.name}\n${JSON.stringify(response, null, 2)}`);
  } catch (error) {
    showError(el.secretActionOutput, error);
  }
}

async function listSecretsFromUI() {
  try {
    const scope = (el.secretScope && el.secretScope.value.trim()) || 'global';
    const response = await requestJSON(`/secrets?scope=${encodeURIComponent(scope)}`);
    setText(el.secretActionOutput, `${nowLabel()} — secrets in ${scope}\n${JSON.stringify(response, null, 2)}`);
  } catch (error) {
    showError(el.secretActionOutput, error);
  }
}

async function lookupWorkflowFromUI() {
  const workflowId = (el.workflowIdLookup && el.workflowIdLookup.value.trim()) || '';
  if (!workflowId) {
    setText(el.workflowLookupOutput, 'Error: workflow ID is required.');
    return;
  }

  try {
    const payload = await requestJSON(`/workflows/${encodeURIComponent(workflowId)}`);
    setText(el.workflowLookupOutput, JSON.stringify(payload, null, 2));
  } catch (error) {
    showError(el.workflowLookupOutput, error);
  }
}

async function getSecretByNameFromUI() {
  const name = (el.secretName && el.secretName.value.trim()) || '';
  if (!name) {
    setText(el.secretActionOutput, 'Error: secret name is required');
    return;
  }

  try {
    const scope = (el.secretScope && el.secretScope.value.trim()) || 'global';
    const response = await requestJSON(`/secrets/${encodeURIComponent(name)}?scope=${encodeURIComponent(scope)}`);
    setText(el.secretActionOutput, `${nowLabel()} — get ${name}\n${JSON.stringify(response, null, 2)}`);
  } catch (error) {
    showError(el.secretActionOutput, error);
  }
}

async function createCronFromUI() {
  const payload = normalizeCronPayload();

  if (!payload.interval) {
    setText(el.cronActionOutput, 'Error: interval is required');
    return;
  }

  try {
    const response = await requestJSON('/cron-runs', {
      method: 'POST',
      body: JSON.stringify(payload),
    });
    setText(
      el.cronActionOutput,
      `${nowLabel()} — scheduled ${response.name || response.id}\n${JSON.stringify(response, null, 2)}`
    );
    await refreshCronRuns();
  } catch (error) {
    showError(el.cronActionOutput, error);
  }
}

async function runPipeline(event) {
  if (event && event.preventDefault) {
    event.preventDefault();
  }

  try {
    const payload = normalizeFormRunPayload();
    await startRunPayload(payload);
  } catch (error) {
    showError(el.runStartOutput, error);
  }
}

async function retryRun(runId) {
  if (!runId) {
    return;
  }

  try {
    const run = await requestJSON(`/runs/${runId}/retry`, {
      method: 'POST',
      body: '{}',
    });
    state.runsById.set(run.id, run);
    setText(el.runStartOutput, `${nowLabel()} — retry started ${run.id}`);
    selectRun(run.id);
    await loadRuns();
  } catch (error) {
    showError(el.runStartOutput, error);
  }
}

async function manualCancelRun(runId) {
  if (!runId) {
    return;
  }

  try {
    const run = await requestJSON(`/runs/${runId}/cancel`, {
      method: 'POST',
      body: '{}',
    });
    state.runsById.set(run.id, run);
    renderRuns();
    updateSelectedRun(run.id, run);
    await loadRuns();
  } catch (error) {
    showError(el.selectedRunInfo, error);
  }
}

async function cancelSelectedRun() {
  const runId = state.selectedRunId;
  if (!runId) return;
  await manualCancelRun(runId);
}

function bindUI() {
  el.pipelineForm.addEventListener('submit', async (event) => {
    event.preventDefault();
    await withBusy(el.pipelineSubmitBtn, 'Loading', async () => {
      try {
        await Promise.all([loadPipeline(), validatePipeline(), loadJobs(), loadDiscover()]);
        const wf = document.getElementById('workflowFile').value.trim();
        const wd = document.getElementById('workdir').value.trim();
        if (wf) {
          document.getElementById('runFile').value = wf;
        }
        if (wd) {
          document.getElementById('runWorkdir').value = wd;
        }
        setStatus('warn', 'Pipeline refreshed from query.');
      } catch {
        setStatus('bad', 'Pipeline refresh failed.');
      }
    });
  });

  el.validateBtn.addEventListener('click', () => withBusy(el.validateBtn, 'Validating', validatePipeline));
  el.reloadJobsBtn.addEventListener('click', async () => {
    await withBusy(el.reloadJobsBtn, 'Loading', loadJobs);
  });
  el.discoverBtn.addEventListener('click', () => withBusy(el.discoverBtn, 'Discover', loadDiscover));

  el.runForm.addEventListener('submit', async (event) => {
    event.preventDefault();
    await withBusy(el.runSubmitBtn, 'Dispatch', runPipeline);
  });

  el.refreshRuns.addEventListener('click', async () => {
    await withBusy(el.refreshRuns, 'Refresh', async () => {
      await loadRuns();
      if (state.selectedRunId) {
        updateSelectedRun(state.selectedRunId, state.runsById.get(state.selectedRunId));
      }
    });
  });

  el.cancelRun.addEventListener('click', cancelSelectedRun);
  el.runStatusFilter.addEventListener('change', () => {
    state.runStatusFilter = el.runStatusFilter.value || 'all';
    renderRuns();
  });
  if (el.runSearchInput) {
    el.runSearchInput.addEventListener('input', () => {
      state.runSearch = el.runSearchInput.value || '';
      renderRuns();
    });
  }
  if (el.runSearchClear) {
    el.runSearchClear.addEventListener('click', () => {
      if (el.runSearchInput) {
        el.runSearchInput.value = '';
      }
      state.runSearch = '';
      renderRuns();
    });
  }
  if (el.refreshStackDump) {
    el.refreshStackDump.addEventListener('click', () => withBusy(el.refreshStackDump, 'Capture', loadStackDump));
  }
  if (el.quickRefreshBtn) {
    el.quickRefreshBtn.addEventListener('click', async () => {
      setStatus('warn', 'Refreshing workspace…');
      await withBusy(el.quickRefreshBtn, 'Sync', async () => {
        try {
          await refreshWorkspace();
          setStatus('ok', 'Workspace refreshed.');
        } catch {
          setStatus('warn', 'Workspace refresh partially failed.');
        }
      });
    });
  }
  if (el.quickLoadPipelineBtn) {
    el.quickLoadPipelineBtn.addEventListener('click', async () => {
      await withBusy(el.quickLoadPipelineBtn, 'Refresh', async () => {
        await loadPipeline();
        await loadJobs();
        await loadDiscover();
        setStatus('ok', 'Pipeline, jobs, and discovery refreshed.');
      });
    });
  }
  if (el.quickDiscoverBtn) {
    el.quickDiscoverBtn.addEventListener('click', async () => {
      await withBusy(el.quickDiscoverBtn, 'Discover', loadDiscover);
    });
  }
  if (el.quickJobsBtn) {
    el.quickJobsBtn.addEventListener('click', async () => {
      await withBusy(el.quickJobsBtn, 'Reload', loadJobs);
    });
  }
  if (el.quickWebhookBtn) {
    el.quickWebhookBtn.addEventListener('click', async () => {
      await withBusy(el.quickWebhookBtn, 'Refresh', loadWebhookEvents);
    });
  }
  if (el.loadFeatureCatalog) {
    el.loadFeatureCatalog.addEventListener('click', () => withBusy(el.loadFeatureCatalog, 'Load', loadFeaturesSummary));
  }
  if (el.loadWorkflowsContract) {
    el.loadWorkflowsContract.addEventListener('click', () =>
      withBusy(el.loadWorkflowsContract, 'Load', loadWorkflowsFeatureContract)
    );
  }
  if (el.loadSecretsContract) {
    el.loadSecretsContract.addEventListener('click', () =>
      withBusy(el.loadSecretsContract, 'Load', loadSecretsFeatureContract)
    );
  }
  if (el.loadCronContract) {
    el.loadCronContract.addEventListener('click', () =>
      withBusy(el.loadCronContract, 'Load', loadCronRunsFeatureContract)
    );
  }
  if (el.dispatchWorkflow) {
    el.dispatchWorkflow.addEventListener('click', () => withBusy(el.dispatchWorkflow, 'Dispatch', dispatchWorkflowFromUI));
  }
  if (el.storeSecret) {
    el.storeSecret.addEventListener('click', () => withBusy(el.storeSecret, 'Save', storeSecretFromUI));
  }
  if (el.refreshSecrets) {
    el.refreshSecrets.addEventListener('click', () => withBusy(el.refreshSecrets, 'Load', listSecretsFromUI));
  }
  if (el.createCronRun) {
    el.createCronRun.addEventListener('click', () => withBusy(el.createCronRun, 'Create', createCronFromUI));
  }
  if (el.refreshCron) {
    el.refreshCron.addEventListener('click', () => withBusy(el.refreshCron, 'Reload', refreshCronRuns));
  }
  if (el.lookupWorkflow) {
    el.lookupWorkflow.addEventListener('click', () => withBusy(el.lookupWorkflow, 'Lookup', lookupWorkflowFromUI));
  }
  if (el.getSecretByName) {
    el.getSecretByName.addEventListener('click', () => withBusy(el.getSecretByName, 'Load', getSecretByNameFromUI));
  }
  if (el.refreshRunLogs) {
    el.refreshRunLogs.addEventListener('click', () => withBusy(el.refreshRunLogs, 'Refresh', refreshSelectedRunLogs));
  }
  if (el.clearRunLogs) {
    el.clearRunLogs.addEventListener('click', clearRunLogBuffer);
  }
  if (el.autoScrollLogs) {
    el.autoScrollLogs.addEventListener('click', () => {
      setAutoScroll(!state.logAutoscroll);
    });
  }

  el.runFromLastBtn.addEventListener('click', async () => {
    if (!state.lastRun) {
      setText(el.runStartOutput, 'No last run payload available. Start one manually first.');
      return;
    }

    await withBusy(el.runFromLastBtn, 'Rerun', async () => {
      try {
        await startRunPayload(state.lastRun);
      } catch (error) {
        showError(el.runStartOutput, error);
      }
    });
  });

  el.serviceStatus.addEventListener('dblclick', loadSystem);
  if (el.refreshWebhookEvents) {
    el.refreshWebhookEvents.addEventListener('click', () => withBusy(el.refreshWebhookEvents, 'Refresh', loadWebhookEvents));
  }
}

async function bootstrap() {
  bindUI();
  wireSectionNav();
  setAutoScroll(state.logAutoscroll);

  await pingHealth();
  await Promise.all([
    loadPipeline(),
    loadRuns(),
    loadJobs(),
    loadSystem(),
    loadDiscover(),
    loadStackDump(),
    refreshCronRuns(),
  ]);
  loadFeaturesSummary().catch(() => {});
  loadWebhookEvents().catch(() => {});

  setInterval(async () => {
    try {
      await loadRuns();
      await loadWebhookEvents();
      await loadSystem();
      await refreshCronRuns();
    } catch {
      // no-op; keep last status visible
    }
  }, 6000);
}

bootstrap();
