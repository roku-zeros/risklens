# Third-party components

Go binaries include golang.org/x/tools, golang.org/x/mod and golang.org/x/sync under BSD-3-Clause; license texts are in licenses/. The Go standard library/toolchain has its own BSD-style license.

Runtime dependencies are installed separately: ONNX Runtime (MIT), Hugging Face tokenizers (Apache-2.0), NumPy (BSD-3-Clause), and their dependencies. Consult the license metadata in the installed packages. runtime/requirements-lock.txt fixes the tested installation; it does not vendor those libraries.

intfloat/multilingual-e5-small uses the MIT license according to its official model card. Weights/tokenizer are downloaded separately at a pinned revision and are not included in the source archive. Model card: https://huggingface.co/intfloat/multilingual-e5-small

The optional official MCP Python SDK integration test uses mcp==1.26.0 (MIT); it is not a production dependency.
