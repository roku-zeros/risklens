# Первичные источники и выбор зависимостей

Проверены 5 ноября 2026 года. Реальные версии исполнения и результаты отделены от сведений из документации в REPORT.md.

- [Go 1.26 release notes](https://go.dev/doc/go1.26) — целевая линия Go и изменения платформ.
- [golang.org/x/tools v0.52.0 go.mod](https://raw.githubusercontent.com/golang/tools/v0.52.0/go.mod) — минимальная версия Go 1.26.0. Поставка реально собрана и протестирована Go 1.26.6.
- [go/packages API](https://pkg.go.dev/golang.org/x/tools/go/packages) — загрузка с AST, типами, test variants и overlay.
- [Go module reference](https://go.dev/ref/mod) — `-mod=readonly`, модульный кэш и GOPROXY.
- [multilingual-e5-small](https://huggingface.co/intfloat/multilingual-e5-small) — MIT, 384 измерения, multilingual retrieval, префиксы query/passage, mean pooling, L2 и лимит 512 токенов.
- [Закреплённая модель](https://huggingface.co/intfloat/multilingual-e5-small/tree/614241f622f53c4eeff9890bdc4f31cfecc418b3) — используемая ревизия. Скрипт загружает ONNX FP32 и tokenizer.json с этой ревизии.
- [BGE-M3](https://huggingface.co/BAAI/bge-m3) — альтернативная multilingual-модель с контекстом 8192, MIT; в поставке не запускается.
- [Qwen3-Embedding-0.6B](https://huggingface.co/Qwen/Qwen3-Embedding-0.6B) — альтернатива с явно заявленным code retrieval, 0.6B параметров, размерность до 1024, Apache 2.0; в поставке не запускается.
- [ONNX Runtime Python](https://onnxruntime.ai/docs/get-started/with-python.html) — CPU runtime и платформенные варианты установки. Наличие wheel для конкретной ОС/архитектуры проверяется при установке; реальные запуски в отчёте относятся только к macOS arm64.
- [MCP stdio transport, 2025-11-25](https://modelcontextprotocol.io/specification/2025-11-25/basic/transports) — JSON-RPC, сообщения по строкам, отдельный stderr.
- [MCP tools](https://modelcontextprotocol.io/specification/2025-11-25/server/tools) — tools/list, tools/call, input schemas и structuredContent.
- [Официальный MCP Python SDK](https://github.com/modelcontextprotocol/python-sdk) и [версия 1.26.0](https://pypi.org/project/mcp/1.26.0/) — независимая проверка интероперабельности stdio.

Сервер реализует необходимый stdio JSON-RPC набор самостоятельно, без MCP SDK в production-зависимостях Go. Поддерживаемые версии handshake: 2024-11-05, 2025-03-26, 2025-06-18, 2025-11-25. Интероперабельность фактически проверена для 2025-11-25. Более новые протокольные версии не объявляются проверенными.
