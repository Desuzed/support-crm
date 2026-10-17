'use strict';
const $ = (id) => document.getElementById(id);
const state = {tickets: [], users: [], selected: '', filter: 'all', view: 'tickets', busy: new Set(), general: [], selection: 0, pendingDelete: null};
const labels = {open: 'Открыто', in_progress: 'В работе', resolved: 'Решено'};
function el(tag, cls, value) { const node = document.createElement(tag); if (cls) node.className = cls; if (value !== undefined) node.textContent = String(value); return node; }
function notice(message = '') { $('notice').textContent = message; $('notice').hidden = !message; }
async function api(path, options = {}) {
  const response = await fetch(path, {...options, headers: {'Content-Type': 'application/json', ...options.headers}});
  let result; try { result = await response.json(); } catch { throw new Error('Сервис вернул неожиданный ответ'); }
  if (response.status === 401) { $('workspace').hidden = true; $('login').hidden = false; }
  if (!response.ok) { const error = new Error(result.error || 'Не удалось выполнить запрос'); error.trace = result.trace; throw error; }
  return result;
}
function formatTime(value) { const date = new Date(value); return Number.isNaN(date.getTime()) ? '' : date.toLocaleTimeString('ru-RU', {hour: '2-digit', minute: '2-digit'}); }
function visibleTickets() {
  const query = $('search').value.toLowerCase().trim();
  return state.tickets.filter(t => (state.filter === 'all' || t.status === state.filter) && `${t.id} ${t.title}`.toLowerCase().includes(query));
}
function renderTickets() {
  document.querySelectorAll('[data-filter]').forEach(button => button.classList.toggle('active', button.dataset.filter === state.filter));
  const tickets = visibleTickets();
  $('ticket-count').textContent = state.tickets.length; $('list-count').textContent = tickets.length;
  $('ticket-list').replaceChildren();
  if (!tickets.length) $('ticket-list').append(el('p', 'empty-list', 'Обращения не найдены'));
  for (const ticket of tickets) {
    const row = el('button', 'ticket-row' + (ticket.id === state.selected ? ' selected' : ''));
    row.type = 'button'; row.setAttribute('aria-pressed', String(ticket.id === state.selected));
    const top = el('div', 'row-top'); top.append(el('span', '', ticket.id.length > 16 ? ticket.id.slice(0, 15) + '…' : ticket.id), el('span', '', ticket.demo ? 'ДЕМО · ' + ticket.category : ticket.category));
    const bottom = el('div', 'row-bottom'); bottom.append(el('span', 'badge ' + ticket.status, labels[ticket.status] || ticket.status), el('span', ticket.priority === 'high' ? 'badge high' : '', ticket.priority === 'high' ? 'Высокий' : 'Обычный'));
    row.append(top, el('h3', '', ticket.title), el('p', '', ticket.description), bottom);
    row.addEventListener('click', () => selectTicket(ticket.id).catch(e => notice(e.message)));
    $('ticket-list').append(row);
  }
}
async function loadTickets() { state.tickets = await api('/api/tickets'); renderTickets(); }
function renderMessage(message) {
  const node = el('article', 'message ' + message.role);
  const header = el('div', 'message-header');
  const title = message.role === 'assistant' ? 'Project Assistant' : message.role === 'operator' ? 'Заметка оператора' : 'Пользователь';
  header.append(el('span', 'avatar', message.role === 'assistant' ? '✧' : message.role === 'operator' ? 'OP' : 'П'), el('strong', '', title));
  const time = el('time', '', formatTime(message.createdAt)); time.dateTime = message.createdAt || ''; header.append(time);
  node.append(header, el('div', 'message-content', message.content));
  if (message.sources?.length) {
    const sources = el('details', 'sources'); sources.append(el('summary', '', `Источники и точные цитаты · ${message.sources.length}`));
    for (const source of message.sources) {
      const item = el('div', 'source-item');
      const kind = {document: 'Документ', ticket: 'Обращение', diagnostic: 'Диагностика', user: 'Пользователь'}[source.kind] || 'Источник';
      item.append(el('strong', '', `${kind} · ${source.source}`), el('blockquote', '', source.quote)); sources.append(item);
    }
    node.append(sources);
  }
  if (message.trace) {
    const t = message.trace;
    const mode = t.retrieval === 'rag' ? 'RAG' : t.retrieval === 'direct_documents' ? 'Чтение документов · RAG недоступен или не дал результатов' : 'Документы не получены';
    node.append(el('div', 'trace', `${t.model} · ${mode} · ${t.crm === 'mcp' ? 'CRM MCP · ' : ''}${(t.durationMs / 1000).toFixed(1)} с`));
    node.append(renderMCPJournal(t));
  }
  return node;
}

function callPurpose(call) {
  const purposes = {
    crm_get_ticket: 'Получение обращения, окружения и переписки',
    crm_get_user: 'Получение данных пользователя',
    project_get_info: 'Версия проекта и готовность RAG-индекса',
    project_search_docs: 'Поиск фрагментов по RAG проекта',
    project_read_document: 'Прямое чтение документа — резервный путь',
  };
  if (call.tool === 'crm_add_message') return call.arguments?.role === 'assistant' ? 'Сохранение ответа в CRM' : 'Сохранение вопроса в CRM';
  return purposes[call.tool] || 'Вызов инструмента MCP';
}

function renderMCPJournal(trace) {
  const journal = el('details', 'mcp-journal');
  const recorded = Array.isArray(trace.mcpCalls);
  const calls = recorded ? trace.mcpCalls : [];
  const errors = calls.filter(call => call.error).length;
  journal.append(el('summary', '', recorded ? `Журнал MCP · вызовов: ${calls.length}${errors ? ' · ошибок: ' + errors : ''}` : 'Журнал MCP · нет записи'));
  let built = false;
  journal.addEventListener('toggle', () => {
    if (!journal.open || built) return;
    built = true;
    if (!recorded) { journal.append(el('p', 'journal-note', 'Для этого старого ответа журнал не записывался. Задайте новый вопрос, чтобы увидеть фактические вызовы.')); return; }
    journal.append(el('p', 'journal-note', 'Фактические вызовы для этого вопроса. Аргументы и результаты получены сервером, независимо от текста ответа модели.'));
    if (trace.modelContext) {
      const context = trace.modelContext;
      const prepared = el('details', 'prepared-context'); prepared.append(el('summary', '', 'Контекст, подготовленный для DeepSeek'));
      prepared.append(el('p', 'journal-note', `${context.ticketId ? 'Тикет: ' + context.ticketId + '. ' : 'Общий вопрос без тикета. '}Предыдущих сообщений пользователя и оператора: ${context.historyMessages}.`));
      const list = el('ul', 'context-evidence');
      for (const source of context.evidence || []) {
        const item = el('li');
        const kind = {document:'Документ', ticket:'Обращение', user:'Пользователь', diagnostic:'Диагностика'}[source.kind] || source.kind;
        item.append(el('span', '', `${kind} · ${source.source}`), el('code', '', source.id)); list.append(item);
      }
      prepared.append(list); journal.append(prepared);
    }
    if (!calls.length) journal.append(el('p', 'journal-note', 'MCP-инструменты в этом запросе не вызывались.'));
    for (const call of calls) {
      const detail = el('details', 'mcp-call' + (call.error ? ' failed' : ''));
      const summary = el('summary');
      const top = el('span', 'call-heading');
      const service = call.server === 'crm' ? 'CRM' : call.server === 'project' ? 'Проект · Mac' : 'MCP';
      top.append(el('span', 'call-number', call.sequence), el('span', 'call-server', service), el('code', 'call-tool', call.tool), el('span', 'call-outcome', `${call.error ? 'Ошибка' : 'Успешно'} · ${call.durationMs} мс`));
      summary.append(top, el('span', 'call-purpose', callPurpose(call))); detail.append(summary);
      let populated = false;
      detail.addEventListener('toggle', () => {
        if (!detail.open || populated) return;
        populated = true;
        const body = el('div', 'call-body');
        body.append(el('p', 'journal-note', 'Начало вызова: ' + new Date(call.startedAt).toLocaleString('ru-RU')));
        if (call.error) body.append(el('p', 'call-error', call.error));
        for (const [label, value] of [['Аргументы', call.arguments], ['Результат', call.result]]) {
          body.append(el('h4', '', label), el('pre', 'call-json', JSON.stringify(value ?? null, null, 2)));
        }
        if (call.truncated) body.append(el('p', 'journal-note', 'Большой ответ или аргументы сокращены до 64 КиБ; полный размер указан в originalBytes.'));
        if (call.omittedNestedLogs) body.append(el('p', 'journal-note', 'Вложенные журналы из предыдущих ответов исключены. Остальные данные показаны в пределах лимита.'));
        body.append(el('p', 'journal-note', 'Служебные ключи и токены скрыты; поля паролей и секретов маскируются.'));
        detail.append(body);
      });
      journal.append(detail);
    }
  });
  return journal;
}
function renderMessages(container, messages) { container.replaceChildren(...messages.map(renderMessage)); container.scrollTop = container.scrollHeight; }
function renderContext(ticket) {
  const box = $('ticket-context'); box.replaceChildren();
  const grid = el('div', 'context-grid');
  for (const [key, value] of Object.entries(ticket.environment || {})) grid.append(el('span', 'context-chip', `${key}: ${typeof value === 'object' ? JSON.stringify(value) : value}`));
  box.append(grid);
  if (ticket.context?.reportedError) box.append(el('p', 'context-note', 'Сообщённая ошибка: ' + ticket.context.reportedError));
  if (ticket.context?.alreadyTried?.length) box.append(el('p', 'context-note', 'Уже проверено: ' + ticket.context.alreadyTried.join('; ')));
  if (ticket.context?.unknown?.length) box.append(el('p', 'context-note', 'Нужно уточнить: ' + ticket.context.unknown.join('; ')));
  if (ticket.demo) box.append(el('p', 'context-note', 'Демонстрационное обращение по функциям проекта. Эти данные не являются текущей диагностикой вашего компьютера.'));
}
function updateBusy() { const busy = state.busy.has(state.selected); $('send-ticket').disabled = busy; $('question').disabled = busy; $('delete-ticket').disabled = busy || !state.selected; $('chat-progress').hidden = !busy || state.pendingDelete === state.selected; $('send-general').disabled = state.busy.has('general'); }
function clearTicket() {
  ++state.selection; state.selected = '';
  $('ticket-content').hidden = true; $('empty-ticket').hidden = false;
  $('ticket-title').textContent = ''; $('ticket-id').textContent = ''; $('question').value = '';
  $('messages').replaceChildren(); $('ticket-context').replaceChildren();
  updateBusy(); renderTickets();
}
async function selectTicket(id) {
  const sequence = ++state.selection; state.selected = id; renderTickets(); updateBusy(); notice();
  const ticket = await api('/api/tickets/' + encodeURIComponent(id)); if (sequence !== state.selection) return;
  $('empty-ticket').hidden = true; $('ticket-content').hidden = false;
  $('ticket-id').textContent = ticket.id; $('ticket-title').textContent = ticket.title; $('demo-badge').hidden = !ticket.demo; $('ticket-status').value = ticket.status;
  $('question').value = ''; $('as-note').checked = false; renderContext(ticket); renderMessages($('messages'), ticket.messages || []);
}
function setView(view) {
  state.view = view; notice();
  for (const key of ['tickets', 'general', 'users']) { $('nav-' + key).classList.toggle('active', key === view); $(key === 'tickets' ? 'ticket-view' : key + '-view').hidden = key !== view; }
  const content = {tickets: ['Обращения', 'Каждое обращение — в контексте.', 'История пользователя, документация проекта и проверяемые ответы.'], general: ['База знаний', 'Проект отвечает на ваши вопросы.', 'Ответы по документации и коду, доступным через MCP на Mac.'], users: ['Пользователи', 'Люди и их контекст.', 'Пользователи проекта и связанные с ними обращения.']}[view];
  $('page-name').textContent = content[0]; $('page-title').textContent = content[1]; $('page-subtitle').textContent = content[2]; $('new-ticket').hidden = view !== 'tickets';
  if (view === 'users') loadUsers().catch(e => notice(e.message));
}
async function loadUsers() {
  state.users = await api('/api/users'); $('users-list').replaceChildren();
  for (const user of state.users) { const node = el('div', 'user-row'); node.append(el('span', 'avatar', user.name.slice(0, 1))); const info = el('div'); info.append(el('h3', '', user.name), el('p', '', Object.values(user.environment || {}).join(' · ') || 'Окружение не указано')); node.append(info, el('small', '', user.id)); $('users-list').append(node); }
}
async function loadStatus() {
  $('project-status').textContent = 'Проверка Mac…'; $('project-dot').className = '';
  try { const status = await api('/api/status'); const ready = status.project === 'connected'; $('project-status').textContent = ready ? 'Mac подключён' : 'Mac недоступен'; $('project-dot').className = ready ? 'ready' : 'error'; $('model-status').textContent = status.deepseekConfigured ? `DeepSeek · ${status.model}` : 'DeepSeek · нет ключа'; $('project-status').parentElement.title = ready ? `Индекс: ${status.info?.indexStatus}; версия: ${status.info?.commit?.slice(0, 8) || 'не определена'}` : 'Проверьте Project MCP и сетевое подключение'; } catch (e) { $('project-status').textContent = 'Нет соединения'; $('project-dot').className = 'error'; }
}
async function boot() {
  await loadTickets(); $('login').hidden = true; $('workspace').hidden = false; loadStatus();
  if (state.tickets.length) await selectTicket(state.tickets.find(t => t.id === 'SUP-001')?.id || state.tickets[0].id);
}
$('login-form').addEventListener('submit', async e => { e.preventDefault(); $('login-error').textContent = ''; const button = e.submitter; button.disabled = true; try { await api('/api/login', {method: 'POST', body: JSON.stringify({token: $('token').value})}); $('token').value = ''; await boot(); } catch (err) { $('login-error').textContent = err.message; } finally { button.disabled = false; } });
$('logout').addEventListener('click', async () => { await api('/api/logout', {method: 'POST', body: '{}'}); state.general = []; $('workspace').hidden = true; $('login').hidden = false; });
for (const view of ['tickets', 'general', 'users']) $('nav-' + view).addEventListener('click', () => setView(view));
$('search').addEventListener('input', renderTickets);
document.querySelectorAll('[data-filter]').forEach(button => button.addEventListener('click', () => { state.filter = button.dataset.filter; document.querySelectorAll('[data-filter]').forEach(b => b.classList.toggle('active', b === button)); renderTickets(); }));
$('reload-tickets').addEventListener('click', () => loadTickets().catch(e => notice(e.message)));
$('refresh-status').addEventListener('click', loadStatus);
$('ticket-status').addEventListener('change', async () => { try { await api('/api/tickets/' + encodeURIComponent(state.selected), {method: 'PATCH', body: JSON.stringify({status: $('ticket-status').value})}); await loadTickets(); } catch (e) { notice(e.message); } });
$('delete-ticket').addEventListener('click', () => {
  const ticket = state.tickets.find(t => t.id === state.selected);
  if (!ticket || state.busy.has(ticket.id)) return;
  state.pendingDelete = ticket.id;
  $('delete-ticket-name').textContent = ticket.title;
  $('delete-ticket-error').textContent = '';
  $('delete-ticket-dialog').showModal();
  $('cancel-delete-ticket').focus();
});
$('cancel-delete-ticket').addEventListener('click', () => $('delete-ticket-dialog').close());
$('delete-ticket-dialog').addEventListener('close', () => { state.pendingDelete = null; });
$('delete-ticket-dialog').addEventListener('cancel', event => { if (state.busy.has(state.pendingDelete)) event.preventDefault(); });
$('delete-ticket-form').addEventListener('submit', async event => {
  event.preventDefault(); const id = state.pendingDelete;
  if (!id || state.busy.has(id)) return;
  state.busy.add(id); updateBusy();
  $('confirm-delete-ticket').disabled = true; $('cancel-delete-ticket').disabled = true;
  $('confirm-delete-ticket').textContent = 'Удаляем…'; $('delete-ticket-error').textContent = '';
  try {
    await api('/api/tickets/' + encodeURIComponent(id), {method: 'DELETE'});
    state.tickets = state.tickets.filter(ticket => ticket.id !== id);
    if (state.selected === id) clearTicket(); else renderTickets();
    $('delete-ticket-dialog').close();
    const next = visibleTickets()[0];
    if (!state.selected && next) {
      try { await selectTicket(next.id); } catch { clearTicket(); }
    }
    notice('Обращение удалено');
  } catch (error) {
    $('delete-ticket-error').textContent = error.message;
  } finally {
    state.busy.delete(id); updateBusy();
    $('confirm-delete-ticket').disabled = false; $('cancel-delete-ticket').disabled = false;
    $('confirm-delete-ticket').textContent = 'Удалить обращение';
  }
});
$('ticket-chat').addEventListener('submit', async event => {
  event.preventDefault(); const id = state.selected; const question = $('question').value.trim(); if (!id || !question || state.busy.has(id)) return;
  state.busy.add(id); updateBusy(); notice();
  try {
    if ($('as-note').checked) { await api('/api/tickets/' + encodeURIComponent(id) + '/notes', {method: 'POST', body: JSON.stringify({content: question})}); }
    else { $('messages').append(renderMessage({role: 'user', content: question, createdAt: new Date().toISOString()})); $('messages').scrollTop = $('messages').scrollHeight; await api('/api/chat', {method: 'POST', body: JSON.stringify({ticketId: id, question})}); }
    await loadTickets(); if (state.selected === id) await selectTicket(id);
  } catch (error) { notice(error.message); if (state.selected === id) { const t = await api('/api/tickets/' + encodeURIComponent(id)).catch(() => null); if (t) renderMessages($('messages'), t.messages || []); if (error.trace) $('messages').append(renderMessage({role:'assistant',content:error.message,trace:error.trace,createdAt:new Date().toISOString()})); } }
  finally { state.busy.delete(id); updateBusy(); }
});
$('general-chat').addEventListener('submit', async event => {
  event.preventDefault(); const question = $('general-question').value.trim(); if (!question || state.busy.has('general')) return;
  state.busy.add('general'); updateBusy(); notice(); state.general.push({role: 'user', content: question, createdAt: new Date().toISOString()}); renderMessages($('general-messages'), state.general); $('general-question').value = '';
  const pending = el('p', 'chat-progress', 'Ищу документы на Mac и готовлю ответ DeepSeek…'); $('general-messages').append(pending);
  try { const result = await api('/api/chat', {method: 'POST', body: JSON.stringify({question})}); state.general.push({role: 'assistant', content: result.answer, sources: result.sources, trace: result.trace, createdAt: new Date().toISOString()}); }
  catch (error) { notice(error.message); state.general.push({role: 'assistant', content: error.message, trace:error.trace, createdAt: new Date().toISOString()}); }
  finally { state.busy.delete('general'); updateBusy(); renderMessages($('general-messages'), state.general); }
});
document.querySelectorAll('#suggestions button').forEach(button => button.addEventListener('click', () => { $('general-question').value = button.textContent; $('general-chat').requestSubmit(); }));
for (const id of ['question', 'general-question']) $(id).addEventListener('keydown', e => { if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) { e.preventDefault(); $(id).form.requestSubmit(); } });
document.querySelectorAll('.close-dialog').forEach(button => button.addEventListener('click', () => button.closest('dialog').close()));
$('new-ticket').addEventListener('click', async () => { try { await loadUsers(); $('create-user').replaceChildren(...state.users.map(u => { const option = el('option', '', u.name); option.value = u.id; return option; })); if (!state.users.length) { $('user-dialog').showModal(); notice('Сначала добавьте пользователя'); return; } $('ticket-dialog').showModal(); } catch (e) { notice(e.message); } });
$('create-ticket').addEventListener('submit', async event => { event.preventDefault(); event.submitter.disabled = true; try { const ticket = await api('/api/tickets', {method: 'POST', body: JSON.stringify({userId: $('create-user').value, title: $('create-title').value, description: $('create-description').value, environment: {description: $('create-environment').value}, priority: $('create-priority').value})}); $('ticket-dialog').close(); $('create-ticket').reset(); state.filter = 'all'; $('search').value = ''; await loadTickets(); await selectTicket(ticket.id); } catch (e) { notice(e.message); } finally { event.submitter.disabled = false; } });
$('add-user').addEventListener('click', () => $('user-dialog').showModal());
$('create-user-form').addEventListener('submit', async event => { event.preventDefault(); event.submitter.disabled = true; try { await api('/api/users', {method: 'POST', body: JSON.stringify({name: $('user-name').value, environment: {description: $('user-environment').value}})}); $('user-dialog').close(); $('create-user-form').reset(); await loadUsers(); } catch (e) { notice(e.message); } finally { event.submitter.disabled = false; } });
boot().catch(() => { $('workspace').hidden = true; $('login').hidden = false; });
