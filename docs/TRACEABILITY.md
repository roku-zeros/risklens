# Требования и проверка

| Раздел ТЗ | Реализация | Проверка / граница |
|---|---|---|
| 1–6: локальный инструмент, все слои MVP | cmd, internal/lens, internal/mcp, runtime, skill, examples, scripts | Поставка исходников/бинарников/инструкций; нет собственного агента |
| 7.1: один модуль, AST/types, offline, тесты, диагностика | analyze.go, files.go | TestTypedNavigation, TestPartialAndFailedRefresh; модуль/зависимости readonly |
| 7.2: идентичность пакетов/методов/полей/локальных | objectID, field owners, Origin | TestTypedNavigation, TestGenericOriginsLocalFieldsAndClosureCallers |
| 7.3: definitions, references, calls, uncertainty | Snapshot.Symbols/Relations, callTarget | confirmed/candidates/unresolved в unit tests и demo-session.json |
| 7.4: field read/write/read_write/address | typed selectors, fieldKind, composite keys | TestTypedNavigation; явные ограничения alias/reflection/whole-struct |
| 8.1: symbol/text/vector и RRF | search.go, Engine.Search | Три режима на одном наборе реальной модели, benchmark.json |
| 8.2: структурные chunks, metadata, отсутствие дублирования | makeChunks, file gaps, parent link | Полное точное покрытие длинной функции; TestSourceCoverageDoesNotOverlap |
| 8.3: локальная multilingual-модель, CPU, pin/config | runtime/worker.py, setup_model.py, model-lock.json | runtime-smoke.json: реальные векторы, нормализация, окна; CPU macOS |
| 8.4: сохраняемый индекс, update/delete/filter | vectorDisk, vectors.json, full exact scan | TestPersistenceAndExclusiveCache, TestRefreshDeleteCacheAndStalePagination |
| 8.5: bounded context/reasons/source/continuation | context.go | TestLimitsFiltersAndContext, TestExactContextBudgetAndEmptyJSON |
| 9: dirty disk files, snapshots, stale failures/conflicts | readFiles, Refresh, Current, final validation | Update/delete/line-shift/failed-refresh tests; TestChangingFilesDuringAnalysisIsRejected |
| 9: embedding cache, batches, readiness/progress | updateVectors, content/model cache | Кэш при сдвиге строк/перезапуске, одна изменённая функция; статус доступен |
| 10: MCP stdio, schemas, pagination, status, cancel | server.go, engine.go, embedding.go | TestStdioLifecycleAndResponsiveStatus, official SDK smoke; process timeout/cancel test |
| 11: skill процесса разработки | skill/SKILL.md | Содержит поиск, проверку, поля, правки, refresh, уместные тесты и неопределённости |
| 12: демонстрационный Go-модуль и негативные случаи | examples/strategy; tests создают временные изменяемые копии | Одноимённые пакеты/символы, interfaces/function vars, aliases, rewrites, long function; parse/type/change/delete в тестах |
| 12: fake provider отделён от real integration | fakeEmbed только в _test.go | benchmark/demo/runtime smoke используют реальный worker; качество fake не измеряется |
| 13: качество/скорость/память/размер/команды | scripts/evaluate.py, docs/REPORT.md | Hit@5/10, cold/warm, indexing/update, RSS/Go heap, cache size; малый корпус отмечен |
| 14: установка/модель/конфиг/диагностика/очистка | README, configure.py, clear_cache.py, config example | Подготовленные локальные пути + переносимая установка; модель скачивается отдельно |
| 14: платформы и GigaCode честно | REPORT, connection.template | macOS запуск; Linux/Windows cross-build; GigaCode недоступен и не объявлен проверенным |
| 15: единая поставка | risklens/ и архив | Все слои включены, ограничения перечислены; рабочая стратегия пользователя недоступна |

Поддержка полного межпроцедурного потока значений, динамической трассировки, бизнес-адаптера, Confluence и отдельного репозитория бизнес-тестов намеренно отсутствует согласно границам ТЗ.
