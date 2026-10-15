# Models

Local models for the assistant, run by llama.cpp on the server. A model's recipe lists the model file under `downloads`, describes the model under `model` for the model picker, and creates the `ai.model` resource that points the assistant at it. Models download in the background, resume after interruptions and are checked against their checksum before use.

Rules for models, besides the catalog's:

- `model` says what the model is: its family, parameter count, quantization, file format (`gguf`), context length, size class (`small`, `medium` or `large`) and capabilities. The picker recommends a size class by the server's memory.
- `requires.memoryMB` is the memory the model needs at its context length, and at least the size of its files.
- Models only need to be free to download and use. A model under a license the Open Source Initiative hasn't approved is labelled `free-to-use`, with the license's SPDX identifier or a `LicenseRef-` naming it.
- Files come from the model's publisher, pinned by checksum and size.

```
models/qwen3-4b/
  recipe.yaml
  icon.svg
```

```yaml
model:
  family: qwen3
  parameters: 4B
  quantization: Q4_K_M
  format: gguf
  contextLength: 32768
  size: medium
  capabilities: [chat, tools]
```
