# claude-tui — design spec

**Статус:** draft v0.1 · рабочее имя `claude-tui` (переименуемо)
**Аудитория:** Claude Code (исполнитель), Alex (ревью/решения)
**Что это:** терминальный клиент для Claude под Linux — гибрид «чат + агент»,
с проектной группировкой разговоров, сохранением истории и approve/deny-петлёй
для действий на машине. Движок — Claude Code в headless-режиме (на подписке).

---

## 1. Продукт

Один бинарь на Go + Bubble Tea. Стартует как чат (обсуждение, чтение вложений),
но в любой момент переключается в **агентный режим**, где Claude может править файлы
и запускать команды — каждое мутирующее действие проходит через модалку approve/deny
с превью (дифф для правок, команда для Bash). Разговоры сгруппированы по **проектам**;
треды внутри проекта делят рабочую директорию и контекст; история переживает перезапуск.

### Ключевые свойства
- Чат со стримингом ответов токен-в-токен, markdown через glamour.
- Гибридные режимы `chat` / `agent`, переключаемые на лету.
- Approve/deny для тулзов с превью диффа/команды и «запомнить» (правило allow).
- Проекты = рабочая директория + конфиг + общий контекст + набор тредов.
- Persisted история, `/resume`, браузер тредов.
- Вложения файлов/картинок (`@path` + файл-пикер).
- Наследование MCP-серверов и `CLAUDE.md` от Claude Code.

## 2. Цели / Не-цели

**Цели:** удобный личный TUI-клиент; безопасная агентность (ничего не мутируется без
подтверждения в `agent`, в `chat` — вообще без мутаций); проектная организация; работа
на подписке без отдельного API-ключа.

**Не-цели (v1):** мультипользовательский режим; web/GUI; собственный agent-loop в обход
Claude Code; поддержка не-Claude бэкендов; синхронизация между машинами.

---

## 3. Решения (ADR)

**ADR-1 — Движок: Claude Code headless, не прямой API.**
Гоняем `claude -p`. Почему: работа на подписке, наследуем тулзы/agent-loop/MCP/CLAUDE.md
Claude Code, не переписываем агентную петлю. Следствие: зависим от CLI и его форматов;
с 15.06.2026 расход идёт из месячного Agent SDK-кредита (Pro $20 / Max 5x $100 / Max 20x $200),
не из интерактивного лимита — см. §12.

**ADR-2 — Стек: Go + Bubble Tea + Lip Gloss + glamour.**
Почему: стек Alex, лучшая экосистема TUI, glamour для markdown. Зависимости заморожены
рабочим прототипом (bubbletea 1.3.x, bubbles 1.0, glamour 1.0, lipgloss 1.1).

**ADR-3 — Транспорт v1: per-turn `claude -p --resume`.**
На каждый ход — новый процесс, continuity через `--resume <session_id>`. Почему: проще и
без недокументированного bidirectional stdin-протокола (`--input-format stream-json`,
gap описан в anthropics/claude-code#24594). Approve-петля от транспорта **не зависит**
(см. ADR-4). Следствие: холодный старт на ход; persistent-стрим — апгрейд в M4.

**ADR-4 — Approvals: in-process MCP-сервер + `--permission-prompt-tool`.**
TUI поднимает **в своём же процессе** MCP-сервер по HTTP/SSE на `127.0.0.1` (или unix-сокете).
Claude зовём с `--permission-prompt-tool mcp__permctl__approve` и `--mcp-config`, указывающим
на этот сервер. Контракт тула: вход `{tool_use_id, tool_name, input}`, ответ — JSON-текст
`{"behavior":"allow","updatedInput":<input>}` либо `{"behavior":"deny","message":"..."}`.
Хендлер при вызове делает `program.Send(approvalRequestMsg)`, ждёт решение пользователя
через канал, возвращает allow/deny. Почему in-process по HTTP/SSE, а не stdio: stdio-сервер
был бы дочерним для `claude` и отдельным от TUI — пришлось бы городить IPC обратно в UI;
HTTP/SSE-сервер живёт прямо в TUI, запрос на подтверждение приходит туда же, где рисуется модалка.
⚠️ Флаг частично недокументирован/версионно-зависим — CC сверяет точную форму
`--mcp-config` и имя `mcp__server__tool` с текущими доками перед M3.

**ADR-5 — Проект = cwd + конфиг + контекст + треды.**
«Делить контекст в рамках проекта» реализуем так: проект имеет рабочую директорию;
все треды проекта запускают `claude` с `cwd` = эта директория, т.е. автоматически делят
её `CLAUDE.md` и файловый контекст. Плюс проектный `memory.md` подмешивается в каждый тред
через `--append-system-prompt-file`. Сессии Claude Code и так бакетируются по cwd
(`~/.claude/projects/<hash>`), так что наши треды ложатся на этот же бакет; мы храним свои
ссылки на `session_id` + собственный транскрипт.

**ADR-6 — Persistence: файлы в XDG-путях (см. §5).** Без БД для v1; JSON/TOML на диске.

**ADR-7 — Два режима, политика тулзов на режим.**
`chat`: read-only (`Read,Grep,Glob`) либо вовсе без тулзов — мутаций нет.
`agent`: полный набор, всё непреаппрувленное идёт в модалку через ADR-4; запомненные
разрешения становятся allow-правилами проекта и передаются через `--settings` (inline JSON).

---

## 4. Архитектура

```
┌──────────────────────────── claude-tui (один процесс, Go) ───────────────────────────┐
│                                                                                        │
│  Bubble Tea UI ──► Engine ──spawn──► `claude -p ... ` (cwd = проект)                    │
│      ▲                 │                      │ stdout: stream-json (NDJSON)            │
│      │  approvalReq     │                      ▼                                        │
│      │  (program.Send)  │                 события: text-delta / tool_use / result       │
│      │                  └──────────────────────┐                                       │
│  Approval Modal                                 ▼  (когда нужен доступ к тулзе)         │
│      │ decision (chan) ──► MCP Permission Server (HTTP/SSE @127.0.0.1) ◄─call─ claude   │
│      ▼                          возвращает {allow|deny|updatedInput}                    │
│  Project Store ──► диск: config.toml · projects/<slug>/{project.toml, memory.md,        │
│                                                          threads/<id>.json}             │
└────────────────────────────────────────────────────────────────────────────────────┘
```

**Поток одного хода (agent):**
1. Пользователь вводит текст (+вложения) → Enter.
2. Engine собирает команду (см. §9), `cwd` = проект, спавнит `claude -p`.
3. Парсит NDJSON: `system/init` (модель, session_id) → `stream_event` (text_delta — стримим;
   content_block_start/tool_use — индикатор «🔧 …») → `result` (стоимость, финал).
4. Если Claude хочет непреаппрувленный тул → зовёт MCP `approve` → хендлер шлёт
   `program.Send(approvalRequest{tool, input, diff?})` → модалка (дифф для Edit/Write,
   команда для Bash) → пользователь: **allow / allow+remember / deny** → решение в канал →
   хендлер возвращает JSON. На «remember» — добавляем allow-правило в `project.toml`.
5. На `result` — дописываем транскрипт треда и обновлённый `session_id` на диск.

**Поток `chat`:** то же, но без `--permission-prompt-tool`, с read-only `--allowedTools`
и `--permission-mode` отклоняющим прочее (мутаций нет в принципе).

---

## 5. Модель данных / раскладка на диске

```
~/.config/claude-tui/config.toml
~/.local/share/claude-tui/
  projects/
    <project-slug>/
      project.toml          # name, cwd, model, mode, allowed_tools, permission rules, ts
      memory.md             # общий контекст проекта (в --append-system-prompt-file)
      threads/
        <thread-id>.json    # {title, created, updated, claude_session_id, messages[]}
```

`config.toml`: `claude_bin`, `default_model`, `default_mode`, `theme`, `last_project`.
`project.toml`: `name`, `cwd`, `model`, `mode`, `allowed_tools=[]`, `permissions.allow=[]`,
`permissions.deny=[]`, `created`, `updated`.
`thread.json.messages[]`: `{role: user|assistant|tool|system, content, ts, tool_meta?}`.

Реальное состояние сессии Claude Code остаётся в `~/.claude/projects/`; мы храним только
ссылку (`claude_session_id`) + свой богатый транскрипт (для отрисовки/поиска/экспорта).

---

## 6. UX

**Экраны:**
- **Чат** (основной): шапка (проект · режим · модель · session · $cost), вьюпорт разговора,
  трей вложений, поле ввода, статус-бар.
- **Модалка approve**: тул + цель; для Edit/Write — цветной дифф; для Bash — команда;
  кнопки `[a]llow` / `[r]emember+allow` / `[d]eny`. Блокирует, пока нет решения.
- **Свитчер проектов** (overlay): список проектов, создать/выбрать, «использовать текущую папку».
- **Браузер тредов** (overlay): треды проекта, открыть/продолжить/удалить/переименовать.
- **Файл-пикер** (overlay): выбор файла/картинки для вложения.

**Хоткеи:** `Enter` — отправить; `Ctrl+J` — перенос строки; `Ctrl+O` — вложение;
`Ctrl+P` — проекты; `Ctrl+T` — треды; `Tab`/`Ctrl+G` — переключить chat↔agent;
`Esc` — отмена ответа/закрыть overlay; `PgUp/PgDn` — прокрутка; `Ctrl+C` — выход.

**Команды:** `/project [name]`, `/new`, `/threads`, `/resume <id>`, `/mode chat|agent`,
`/attach <path>`, `/files`, `/memory` (правка memory.md проекта), `/clear`, `/help`, `/quit`.
В тексте сообщения `@/path` проходит в Claude Code как есть.

---

## 7. Сборка CLI-инвокации (по режиму)

Общее (оба режима):
```
claude -p "<prompt + ' @'+each attachment>"
  --output-format stream-json --verbose --include-partial-messages
  [--resume <session_id>]
  [--model <model>]
  [--append-system-prompt-file <project memory.md>]
  # cwd процесса = project.cwd
```

`chat` добавляет:
```
  --allowedTools "Read,Grep,Glob"
  --permission-mode dontAsk        # всё, кроме read-only, отклоняется без запроса
```

`agent` добавляет:
```
  --permission-prompt-tool mcp__permctl__approve
  --mcp-config '<inline json: permctl → http/sse @127.0.0.1:<port>>'
  --settings   '<inline json: {"permissions":{"allow":[…],"deny":[…]}} из project.toml>'
  --permission-mode default        # непреаппрувленное → через permission-tool в модалку
```

⚠️ **CC обязан свериться с актуальными доками** по: точной схеме `--mcp-config` для HTTP/SSE-сервера;
имени `mcp__<server>__<tool>` для `--permission-prompt-tool`; принимает ли `--settings`/`--mcp-config`
inline-JSON или нужен файл; формату NDJSON на stdin (если возьмётся за persistent в M4).
Эти места недокументированы/версионно-зависимы — не хардкодить вслепую.

Парсинг событий, continuity и обработка вложений уже отлажены в прототипе (`engine.go`).

---

## 8. План по фазам (для CC)

**M0 — Скелет (переиспользовать прототип).**
Bubble Tea чат-цикл, per-turn `claude -p` stream-json, glamour, статус/стоимость,
одна эфемерная сессия, вложения через `@path` + файл-пикер.
*Приёмка:* чат работает, ответы стримятся и рендерятся, continuity внутри запуска.
*(≈ готово в прототипе — берётся как старт.)*

**M1 — Persistence + проекты.**
Стор на диске (§5); проект = cwd + конфиг + треды; создание/переключение проектов
(`Ctrl+P`, `/project`, «использовать текущую папку»); сохранение транскриптов и session_id;
браузер тредов (`Ctrl+T`), `/resume`; `memory.md` через `--append-system-prompt-file`.
*Приёмка:* перезапуск восстанавливает треды; смена проекта меняет cwd и контекст;
треды проекта делят `CLAUDE.md`/cwd.

**M2 — Вложения (доработка).**
Полировка пикера, трей, поддержка картинок, file-aware рендер. *Приёмка:* картинка+файл
прикрепляются, Claude их читает.

**M3 — Агентный режим + approvals.**
In-process MCP permission-сервер (HTTP/SSE); `--permission-prompt-tool`; тумблер chat↔agent;
модалка с диффом (Edit/Write) и командой (Bash); `allow / remember+allow / deny`; запись
allow/deny-правил в `project.toml` и проброс через `--settings`; таймлайн действий тула
в разговоре.
*Приёмка:* в `agent` правки/Bash вызывают модалку; allow применяет, deny блокирует;
запомненное не спрашивается повторно; в `chat` мутаций нет.

**M4 — Полировка.**
Persistent bidirectional stream (опц., снижает латентность); темы; конфиг хоткеев; поиск
по истории; экспорт треда; управление MCP-серверами; предупреждение о бюджете
(привязка к Agent SDK-кредиту).

---

## 9. Открытые вопросы (нужен выбор Alex)

1. **Бутстрап проекта:** авто-создавать проект из текущей папки при старте, или всегда
   через свитчер? *Предлагаю:* при запуске в директории — предложить «использовать как проект»,
   плюс свитчер для остальных.
2. **«Общий контекст проекта»** = (cwd + CLAUDE.md) + `memory.md` в каждый тред — достаточно?
   Или нужен ещё перенос саммари между тредами (кросс-тред память)? *Предлагаю:* для v1 — без
   кросс-тред саммари (отложить).
3. **Гранулярность «запомнить»:** по префиксу команды (`Bash(go test *)`), по тулу, или по пути
   (`Edit(./src/*)`)? *Предлагаю:* предлагать в модалке умный дефолт (префикс для Bash, путь-глоб
   для Edit/Write), редактируемый перед сохранением.
4. **`chat`-режим:** строго без тулзов или read-only (чтение/греп разрешены)? *Предлагаю:* read-only —
   полезно для «посмотри на эти файлы», но без мутаций.

---

## 10. Заметки

- **Биллинг:** обёртка — «стороннее приложение на Agent SDK», ест месячный Agent SDK-кредит
  (с 15.06.2026), не интерактивный лимит; за пределом — по API-тарифам, если включены usage credits.
- **Безопасность:** дефолт безопасный (chat=read-only; agent спрашивает всё непреаппрувленное);
  deny-правила проекта бьют allow; модалка показывает дифф/команду до выполнения. Хорошие кандидаты
  на ревью сабагентами `architecture-guard` / `security-reviewer`.
- **Прототип:** `engine.go` (spawn + парсинг stream-json + continuity) и базовый `ui.go`
  переиспользуются как M0; дальше наращиваем по фазам.
- **Дистрибуция (вне v1-дизайна):** один статический бинарь; позже — AUR/`.rpm`/AppImage.
