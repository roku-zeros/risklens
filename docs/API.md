# MCP API RiskLens

Транспорт: newline-delimited JSON-RPC 2.0 на stdin/stdout. Логи только stderr. Production-интерфейс: initialize, notifications/initialized, ping, tools/list, tools/call, notifications/cancelled. Без HTTP, sampling, ресурсов и prompts. До 16 одновременно выполняемых обычных запросов; статусы и запуск refresh обходят этот лимит. Долгие запросы имеют timeout. EOF/interrupt/terminate завершают сервер и принадлежащий ему worker.

Полные схемы аргументов: [mcp-tools.json](mcp-tools.json), выгружаются `risklens-mcp --schema`. Неизвестные аргументы, null и несовместимые типы отвергаются. `mode` проверяется семантическим обработчиком; запросы с неверным значением не выполняются.

| Tool | Основные аргументы | Результат |
|---|---|---|
| project_status | `{}` | Status: загрузка, окружение, ошибки, снимок, readiness |
| index_status | `{}` | Тот же Status с прогрессом, cache hits, Go heap bytes |
| refresh_index | `{}` | started, index_snapshot, next; обновление асинхронное |
| search_symbols | query?, kind?, package?, path?, include_tests?, limit?, cursor?, index_snapshot? | Symbol без полного code; точное имя первым |
| get_symbol | symbol_id, index_snapshot? | Symbol с signature, code, comment; длинный code усечён явно |
| find_references | symbol_id, фильтры/пагинация | Relation kind=reference |
| get_callers | symbol_id, фильтры/пагинация | Call relations, включая вхождение в candidates |
| get_callees | symbol_id, фильтры/пагинация | Calls из функции/метода/замыкания; unresolved сохраняются |
| find_field_accesses | symbol_id поля, фильтры/пагинация | Relation read/write/read_write/address |
| get_source | file, start_line?, end_line?, index_snapshot? | Относительный путь, включительный диапазон, code |
| search_code | query, mode?, kind?, package?, path?, include_tests?, limit?, cursor?, index_snapshot? | Hit: chunk, rrf_score, channels, ranks |
| build_context | symbol_ids? / chunk_ids? / query?, mode?, depth?, budget_bytes?, фильтры, limit?, index_snapshot? | ContextItem с reason/via; omitted и продолжение |

`mode`: hybrid (по умолчанию), lexical (symbol+text), vector. `include_tests` по умолчанию true. `package` — точный import path, `path` — префикс корневого относительного пути, `kind` — function/method/type/field/constant/variable/local/import/closure/file. `query` в search_symbols — подстрока имени/сигнатуры, в search_code — запрос для каналов поиска.

У `build_context` нужен хотя бы один seed или query. `depth` по умолчанию 0; для расширения задайте 1–3. `limit` по умолчанию 20, максимум из конфигурации. Оболочка `meta` сообщает точный бюджет JSON-массива items; диагностическая оболочка не включается. В списке omitted могут быть chunk ID и symbol ID — их вид определяется по исходным метаданным, символы имеют читаемые составные ID, chunks — 24 hex-символа.

## Общая оболочка навигации/поиска

```json
{
  "index_snapshot": "24-hex-snapshot-id",
  "fresh": true,
  "complete": true,
  "items": [],
  "truncated": false,
  "next_cursor": "optional-opaque-cursor",
  "limitations": ["..."],
  "diagnostics": [],
  "meta": {}
}
```

`items` — массив у перечисляющих tools, объект у get_symbol/get_source. Пустой список — `[]`. `complete` относится к загрузке и типизации, а граф всегда может быть неполон. Позиции 1-based: line, UTF-8 byte column. Путь всегда относительно корня, с `/`, включая Windows.

Symbol: id, name, kind, package, receiver?, signature, position(file,line,column), end_line, test, code?, comment?. ID не является обещанием стабильности после произвольного рефакторинга.

Relation: symbol_id? (цель), from_symbol? (содержащая функция/метод/замыкание), kind, resolution, position, candidates?, code?. У calls `resolution` — confirmed/candidates/unresolved; у типизированных references/field accesses — confirmed. Место unresolved-вызова сохраняется без придуманной цели. В интерфейсном call symbol_id может указывать на абстрактный метод, candidates — на возможные конкретные реализации.

Chunk: chunk_id, symbol_id, package_path, file_path, start_line, end_line, content_hash, index_snapshot, embedding_model_revision, code, test. content_hash вычислен от подготовленного embedding-входа; у вспомогательных file-фрагментов крайний whitespace нормализуется. Полный input хранится только в памяти; persisted cache хранит hashes/векторы/токены/окна. `embedding_model_revision` содержит полный model_key подготовки, а не только имя модели.

Hit: chunk, rrf_score, channels (symbol/text/vector), ranks. RRF score — ранговая величина, не вероятность. Несколько соседних непересекающихся chunks большого символа могут занимать несколько мест top-k. Курсор учитывает snapshot, параметры, готовность векторов и фактически использованные каналы.

ContextItem: symbol_id, chunk_id?, file, start_line, end_line, code, reason, via?. Расширение может включать callers/callees, references и поля; interface candidates остаются помеченными кандидатами. Это ограниченная выборка, не полный граф.

## Статусы и ошибки

Status: index_snapshot, state (unloaded/loading/ready/failed), fresh, loading, vectors (not_ready/building/ready/degraded), vector_done, vector_total, cache_hits, go_heap_bytes, last_error?, embedding_error?, symbols, chunks, files, complete, diagnostics, environment, module. `files` — наблюдаемые файлы корня, включая go.mod/go.sum; source symbols/chunks только для загруженной конфигурации сборки.

Ошибки аргументов/метода — JSON-RPC error. Ошибка выполнения tool возвращается как `isError=true` с JSON в text content: error, index_snapshot, fresh. Успех содержит и text JSON, и structuredContent. Ошибка stale запускает refresh и требует повторения; на неудачном refresh навигация блокируется до успешного обновления.

`refresh_index` отвечает сразу; `started=false` означает уже запущенную загрузку. `index_status`/`project_status` проверяют наблюдаемые файлы и остаются доступны во время анализа/inference. Отмена асинхронного refresh отдельным MCP-tool не предоставлена; сервер отменяет фоновые задачи при завершении и старую векторизацию при новом refresh. Отмена обычных долгих запросов — `notifications/cancelled` с requestId.

## Пример

```json
{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"client","version":"1"}}}
{"jsonrpc":"2.0","method":"notifications/initialized"}
{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"index_status","arguments":{}}}
{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"search_code","arguments":{"query":"льготный период обучения","mode":"hybrid","limit":5}}}
```

Передайте каждый объект одной строкой. После `search_symbols` используйте настоящий возвращённый id, не конструируйте его по имени. Для контекста сохраняйте index_snapshot; после изменения снимка повторите поиск.
