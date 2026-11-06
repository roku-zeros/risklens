# RiskLens 0.5.0 — пакетное исследование, контракты и надёжное владение кэшем

6 ноября 2026. Серия улучшений по [полному внешнему отчёту 0.3.0](FIELD-REPORT-ANALYSIS-2026-11-06.md). Код закрытого модуля и его исходные CSV/JSON недоступны; результаты на нём не объявляются повторно проверенными. Предпочтительный запуск через go run сохранён.

## Что изменилось

### 1. OS-held lock вместо файла, блокирующего запуск после аварии

Кэш защищён flock на macOS/Linux и LockFileEx на Windows. Дескриптор удерживается весь срок владения; ОС освобождает блокировку при завершении процесса, даже без Engine.Close. Постоянный server.lock не удаляется: его наличие не означает занятой кэш. Удалять файл нельзя из-за гонки разных inode. Engine.Close идемпотентен.

Маркер старых выпусков мигрирует при достоверно мёртвом PID. Работающий/непроверяемый legacy PID и неизвестный формат не перезаписываются. Один владелец кэша сохранён — поддержки нескольких независимых readers/writers этим не добавлено. Проверки реализованы subprocess-тестами для EOF, выхода без Close, второго владельца и миграции. Платформенные реализации Linux/Windows проходят cross-build; реальное исполнение проверено на macOS/arm64. Семантика сетевых FS не проверена.

Очистка vector-файлов перенесена на тот же OS lock: штатный `--clear-cache`; Python-скрипт вызывает его через go run или переданный новый бинарник. Он не удаляет lock и отказывается работать при активном владельце. Чужие файлы кэша сохраняются. Для отката на старую версию нужен отдельный cache.

### 2. Строгий batch: две проверки содержимого на весь пакет

Новый `batch_tools` принимает до 20 независимых read-only calls. На один immutable Go snapshot выполняются full content checks до и после пакета. Item-вызовы не делают отдельных полных Go scans. Если исходники изменились, результаты пакета не публикуются. Это изменение границы проверки, а не metadata-only freshness: прежняя защита от same-size edit с восстановленным mtime сохранена.

Outer fresh/index_snapshot определяют актуальность всех возвращённых результатов; inner fresh=false означает отсутствие самостоятельной проверки. Каждый item имеет index/tool/is_error/result|error. Batch не выполняет control/status/refresh/nested batch и не подставляет результаты одного элемента в аргументы другого. Общий deadline/отмена распространяются на операции.

Items budget 512 KiB; при truncated возвращается remaining_from. Большой элемент может быть вычислен, но не попасть в ответ — вызовите его отдельно или уменьшите лимит. Изменение файла во время выполнения, ошибки отдельных элементов, неверный внешний snapshot и byte-budget покрыты регрессиями. Документ проверяет свой hash в каждом audit-item, не общий транзакционный snapshot всех документов.

### 3. Структурированные контрактные имена и typed field paths

Новый `resolve_reference` возвращает canonical field ID, owner_id, namespace, Go/JSON имя, тип, access_path и позицию. Namespace go/json/auto; точное сопоставление с учётом регистра. Go-пути разрешаются по типизированным scopes (named/anonymous structs, embedding/pointers), а не совпадению предпоследней части строки с предполагаемым owner. Явный owner_id ограничивает выбор. Target_scope_id может обозначать вложенную анонимную структуру и не обязан быть get_symbol ID.

JSON declaration aliases связываются с полем и типом. Это не реализация полной семантики encoding/json и не доказательство wire path или runtime-объекта. Вложенные контейнерные индексы/произвольные выражения не решаются. Число scopes ограничено 50 000; неполнота явно сообщается.

`get_symbol.meta.fields` дополнен json_name/json_tag/target_scope_id. Неправильный регистр не исправляется автоматически, несколько полей с одним alias остаются неоднозначными. В регрессиях проверены цепочка через anonymous structs, scoped JSON alias, одинаковые алиасы разных owners, исключённое json:"-" и отсутствие угадывания field-call expressions.

### 4. Аудит разделяет формы, вместо массового missing_symbol

Добавлены contract_name_verified и field_path_verified для точных исходных привязок; unbound_path и unrecognized_token для неразрешённого пути/токена. Последние не являются доказанными ошибками документа. bindings показывают реальные поля; candidate_signature_groups — ограниченные группы формы кандидатов. Совпадение сигнатур не снимает неоднозначность бизнес-смысла. Source/contract binding по-прежнему не проверяет бизнес-прозу и применение правил.

Неуспешный typed field path больше не подтверждается устаревшей эвристикой частичного owner-name. Сохраняются source hashes, document hash/pagination и проверки root. Абсолютный document path теперь отвергается понятной ошибкой до обращения к движку/файлам.

### 5. Явные границы графа

Unresolved/candidate AST calls явно несут unknown_targets_possible. `get_callees.meta.call_coverage` показывает indexed direct sites, confirmed_internal/external/candidate/unresolved, примеры неизвестных границ, состояние анализа. Get_callers/find_references предупреждают, что пустой результат не доказывает отсутствие вызова.

Это наблюдаемость существующего анализа, не новый points-to solver и не восстановленный business-membership граф. Recoveries/dynamic_sites не называются полнотой; одинаковое поле не выдаётся за один runtime-объект. Конструктор, его аргументы, внутренние условия и внешние process guards требуют проверки исходников.

### 6. Исполнение reranker и корректность eval

Search meta.reranker различает disabled/not_applicable/empty/completed/cache_hit/fallback. Есть requested/explicitly_requested/configured/attempted/executed/model_key, scored_items/cache_hits/reranked_items и причина fallback. Включение флага без настроенной модели не изображается как выполненный rerank; при явном запросе выдаётся предупреждение.

`scripts/evaluate.py --require-reranker` проверяет конфигурацию и успешное применение стадии. Disabled/fallback/empty не проходят как полноценный эксперимент. Cache-hit допустим как применение ранее вычисленных оценок и отдельно виден; это не cold inference latency. Оценки моделей и подготовка embeddings не менялись, новые веса не устанавливались.

### 7. Кэш embeddings запросов

Process-local FIFO до 256 query-векторов. Ключ включает полный model/preparation key, размерность и точный запрос; ошибки/невалидные векторы не сохраняются. Meta.query_embedding_cache.hit/executed/model_key показывает reuse. Кэш не отменяет freshness и не смешивает модели. Rerank pair cache остаётся отдельно ограничен 2048 записями.

## Проверки и измерения

Регрессии: OS ownership/lifecycle/legacy migration/clear-cache; strict batch pre/post/source mutation/budget/partial errors; typed scopes/JSON names/audit statuses; unresolved coverage; configured/disabled/executed/cached reranker; query cache reuse/model scope/bounds. Пройдены race/vet, официальный MCP SDK через go run, демонстрация всех 18 tools и JSON-схемы ответов. Linux/amd64 и Windows/amd64 проверяются cross-build; фактические запуски относятся к macOS/arm64.

Локальная проверка batch: [batch-profile-v0.5.0.json](batch-profile-v0.5.0.json). Один живой прогретый процесс, 10 одинаковых коротких чтений на группу, три повторения. Проверяются одинаковые source items, 20 scans у отдельных вызовов против 2 на batch. Медиана группы: 17.05 мс отдельно против 3.37 мс batch (примерно 5.1 раза на этой локальной проверке). Это пример амортизации на маленьком demo, не обещание ускорения производственного модуля в то же число раз.

Выполнена проверка E5 на прежних 12 demo-запросах: [e5-v0.5.0.json](e5-v0.5.0.json). Реальный BGE smoke и повтор: [reranker-smoke-v0.5.0.json](reranker-smoke-v0.5.0.json). На прежнем demo точные top-10 списки совпали с baseline 0.3.0 для всех 36 пар query/mode: lexical Hit@5/10=8/12 и 8/12, vector=10/12 и 11/12, hybrid=10/12 и 11/12. Это проверка отсутствия регрессии ранжирования, а не заявление об улучшении business recall. Малый корпус не заменяет 19 закрытых запросов. Цифры первого запроса, cache hits и warm runtime нужно читать раздельно.

Артефакты: [SDK](sdk-go-run-v0.5.0.json), [demo](demo-v0.5.0.json), [race tests](tests-v0.5.0.log), [API](API.md), [tool schemas](mcp-tools.json).

## Применение

Обновите исходники и перезапустите MCP-подключение: version=0.5.0, tools/list=18. Go run пересоберёт сервер. Модели/Python переустанавливать не требуется; content-addressed совместимые векторы переиспользуются. Курсоры/ID локальных объявлений заново получить из нового snapshot. При миграции завершите старые серверы; не удаляйте активный lock.

Для независимых чтений используйте batch_tools, для контрактных имён — resolve_reference, для неоднозначностей — owner_id и реальные source anchors. После этого повторите короткий production-прогон 0.4/0.5 с тем же конфигом и отдельно измерьте latency, число проходов, качество кандидатов и итоговый бизнес-ответ.

Словарь бизнес-терминов, настройки весов RRF под размеченный набор, полное membership извлечение и metadata/watcher freshness не включены: без ground truth они не объявляются улучшением точности. Strict-режим обычного одиночного запроса по-прежнему делает два полных прохода.
