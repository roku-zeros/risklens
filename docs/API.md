# MCP API RiskLens

Транспорт: newline-delimited JSON-RPC 2.0 на stdin/stdout. Логи только stderr. Production-интерфейс: initialize, notifications/initialized, ping, tools/list, tools/call, notifications/cancelled. Без HTTP, sampling, ресурсов и prompts. До 16 одновременно выполняемых обычных запросов; статусы и запуск refresh обходят этот лимит. Долгие запросы имеют timeout. EOF/interrupt/terminate завершают сервер и принадлежащий ему worker.

Полные схемы аргументов: [mcp-tools.json](mcp-tools.json), выгружаются `risklens-mcp --schema`. Неизвестные аргументы, null и несовместимые типы отвергаются. `mode` проверяется валидатором схемы; запросы с неверным значением не выполняются.

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


## Дополнения 0.2.0

- `search_symbols.owner_id`: ID типа для получения его прямых/встроенных полей. Ответ использует ID объявления, а `access_path` показывает путь от внешнего типа. Get_symbol типа возвращает эти сведения в `meta.fields`, `field_count`, `fields_truncated`; `get_symbol.limit` ограничивает список, а `search_symbols` обеспечивает пагинацию. Для `definition_indexed=false` позиция не выдаётся.
- `search_code.paths` (OR), `exclude_paths`, `distinct_symbols` (по умолчанию true). Дополнительные поля Hit: `evidence=semantic_only|lexical_supported`, `requires_source_verification=true`, `other_matching_chunks`. Группировка не объединяет независимые символы и не доказывает продуктовую принадлежность.
- Ошибка неизвестного ID: `isError=true`, `structuredContent.error/index_snapshot/fresh/details`; details содержит `code`, `requested_symbol_id`, `hint`, `candidates`. Text content содержит тот же JSON. Автоматического разрешения по похожему имени нет.
- При нуле результатов search_symbols: `meta.exact_match=false`, `suggestions`, `hint`; suggestions не являются проверенной заменой устаревшего имени.
- Status: `version`, `freshness_scope`, `go_heap_reserved_bytes`, `go_total_alloc_bytes`, `go_gc_cycles`. Freshness не проверяет документы и бизнес-утверждения.

## Дополнения 0.3.0

Всего 15 tools. Новые аргументы/ответы опубликованы в mcp-tools.json.

| Tool | Аргументы | Результат |
|---|---|---|
| audit_document | document, limit?, cursor?, index_snapshot? | EvidenceCheck[], document_hash, status_counts, parser_scope, semantic_verification=false |
| verify_evidence | claims[1..100], limit?, cursor?, index_snapshot? | EvidenceCheck[], status_counts, semantic_verification=false |
| trace_field | symbol_id поля, entry_symbol_id функции, depth?, include_candidates?, budget_bytes?, path?, package?, include_tests?, limit?, index_snapshot? | FieldTraceItem[], unresolved_or_external_calls, frontier, call_analysis |

EvidenceClaim: symbol_id?, file?, start_line?, end_line?, expected_text? (до 4096 bytes), source_hash?. EvidenceCheck: claim, status, actual?, source_excerpt?, candidates?, reference?, document_line?, note. Actual — предложение на основе существующего ID/уникального текстового якоря, не разрешение применить бизнес-правку автоматически.

Relation дополнена analysis (ssa_static/vta_module/ssa_alias), flow_candidates, unknown_targets_possible и conditions. Conditions — исходные лексические guards, не вычисленные условия достижимости. У SSA alias доступа resolution=candidates: это возможная привязка к объявлению поля, не доказательство объекта.

Status.call_analysis описывает готовность и бюджеты. Search_code/build_context принимают rerank?; Hit содержит rerank_score/reranker_model_key, если стадия включена. Static_facts в Chunk — извлечённые факты синтаксиса/типов. Они не являются LLM-резюме или доказательством поведения.


## Исправления 0.4.0

Всего **16 tools**, включая `list_files`. Это пагинируемый список наблюдаемых исходников/config-файлов (Go/go.mod/go.sum), а не всех файлов репозитория. Поля items: file_path, total_lines, bytes, source_hash. Аргументы: path/paths/exclude_paths/include_tests/limit/cursor/index_snapshot.

`get_source`: end_line=-1 читает до EOF в пределах max_source_bytes; 0/omitted оставляет максимум 40 строк. Диапазон за EOF обрезается, items возвращает total_lines, returned_lines, eof, range_clamped. Завершающий перевод строки не добавляет фиктивную строку; пустой файл имеет total_lines=0 и ответ start_line=1/end_line=0/code="". Неверный start_line и обратный диапазон остаются ошибками с total_lines в details. При byte truncation используйте meta.next_line; если целая строка не помещается, нужно увеличить лимит, а не повторять тот же запрос.

У списковых ответов: meta.total_results/returned_results/offset/has_more; truncated и next_cursor. Meta.results_complete относится к выдаче, complete — к диагностике загрузки/типизации. Не делайте вывод о полноте по items.length=100. В следующем вызове можно добавить index_snapshot, возвращённый первой страницей; cursor всё равно проверяет снимок и остальные параметры запроса.

JSON-RPC -32602 содержит error.data: code/tool/field/allowed_fields/expected/example_arguments/hint. Вложенные аргументы валидируются с путём, например claims[0].symbol. Строгие имена полей сохранены. Runtime InputError содержит details с code/field и сведениями о диапазоне/лимите/пути. Ошибка инструмента не запускает отдельное чтение всего модуля ради статуса; fresh=false/freshness_checked=false означает, что актуальность после ошибки не подтверждена, а не автоматически доказанную устарелость индекса. Сначала исправьте конкретную ошибку, не запускайте refresh на любую неудачу.

В Response.meta и Status.meta: timings_ms (total, freshness, freshness_read_hash, freshness_walk_metadata; у search_code дополнительно symbol_text/query_embedding/vector_scan_sort/fusion/rerank при выполнении этапа), freshness_scans, freshness_file_reads, freshness_source_bytes. Последние два — суммарное число чтений/размер наблюдаемых файлов за все проходы, а не уникальный размер модуля. Freshness включает вложенные этапы, поэтому их нельзя складывать с родительским. Total исключает сериализацию ответа, очередь клиента и запуск процесса. Пустая/нулевая фаза не подтверждает выполненную inference. Status.last_refresh содержит отдельные timings последнего завершённого refresh, включая load_analysis (типизация, SSA, chunking вместе); это не нагрузка текущего поискового запроса и не время фоновой векторизации.

Проверки freshness по-прежнему читают полное содержимое файлов. Они потоково хешируют данные без сохранения временной копии исходников. Ослабленного режима с доверием только mtime нет.


## Дополнения 0.5.0

Всего 18 tools. `batch_tools(calls, index_snapshot?)` принимает 1..20 объектов {tool, arguments}. Навигация/search/evidence/context разрешены; nested batch и control/status/refresh запрещены. Один snapshot, full content pre/post checks на весь пакет, общий items budget 512 KiB, remaining_from при обрезке. Budget исключает внешний envelope; oversized item может быть исполнен, но не помещён в выдачу — выполните его отдельно. У каждого BatchItem index/tool/is_error/result|error; позиции соответствуют входным вызовам. При изменении Go sources весь результат отклоняется. Inner Response.fresh=false означает, что независимая freshness-проверка не проводилась; проверяйте outer fresh и snapshot. Документы проверяют собственный hash в каждом audit-item, а не общий транзакционный snapshot всех документов. Запросы не поддерживают зависимости результатов одного элемента в аргументах следующего. Отмена/timeout всего пакета отменяет работу; общий Go snapshot не является runtime-транзакцией.

`resolve_reference(query, namespace?, owner_id?, package?, path/paths/exclude_paths?, limit/cursor/index_snapshot?)` возвращает ReferenceMatch[]. Namespace auto/go/json, точное сопоставление с учётом регистра. Go selectors разрешаются через типизированные field scopes, включая anonymous structs/embedding/pointers; container indices, полное JSON serialization resolution и выполнение программы не моделируются. target_scope_id — непрозрачный ID scope, который не обязательно является get_symbol ID. Явный owner_id ограничивает выбор. Поля отражают JSON declaration aliases; для promoted полей wire-path может отличаться. Metadata сообщает scope_truncated, semantic_verification=false. Таблица scopes ограничена 50 000.

Get_symbol.meta.fields дополнен json_name/json_tag/target_scope_id. Audit EvidenceCheck может содержать bindings/binding_count/candidate_signature_groups. Contract_name_verified и field_path_verified подтверждают исходную привязку, а не бизнес-утверждение. Unbound_path и unrecognized_token не являются командой исправлять документ. Одинаковые signature groups не превращают ambiguous в verified.

Get_callees.meta.call_coverage: counts confirmed_internal/confirmed_external/candidate/unresolved, indexed_direct_sites, bounded unknown_or_external_examples, examples_truncated, analysis_state и scope. Это direct relations в пределах фильтров, не runtime coverage и не соотношение recovered/dynamic как полнота. Get_callers/find_references возвращают absence_is_not_proven.

Search meta.reranker содержит requested/explicitly_requested/configured/attempted/executed/status/model_key/candidate_items/scored_items/cache_hits/reranked_items/reason/fallback_reason по применимому состоянию. Requested — разрешение стадии в hybrid, не доказательство inference. Cache_hit применяет ранее полученные оценки; executed=false. Meta.query_embedding_cache.hit/executed/model_key отличает повторный embedding от inference. Оба кэша process-local и bounded; query cache — 256 записей, rerank cache — 2048 пар. Не используйте timings как подтверждение исполнения стадий.

Cache ownership перешёл на OS file locks; marker остаётся после выхода, но restart не требует удаления. `--clear-cache` использует тот же протокол. Рабочие ОС этой поставки: macOS/Linux/Windows; остальные платформы явно отвергают cache locking. Сетевые файловые системы с иной семантикой locks не заявлены проверенными. Миграция активного legacy PID не допускается.
