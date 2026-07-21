package lmstudio

// toolsListJSON is the static tool schema returned to tools/list. Tool names
// are relative; MCPlexer adds the namespace from DownstreamServer.ToolNamespace.
const toolsListJSON = `{
  "tools": [
    {
      "name": "status",
      "description": "Report LM Studio state on this machine: whether the lms CLI is installed, whether the local OpenAI-compatible server is up, and which models are loaded. Includes the openai_compat delegation hint for mcpx__delegate_worker.",
      "inputSchema": {"type": "object", "properties": {}}
    },
    {
      "name": "start_server",
      "description": "Start the local LM Studio server (lms server start) and wait until its /v1/models endpoint answers. Once up, workers can target it via model_provider=openai_compat.",
      "inputSchema": {"type": "object", "properties": {}}
    },
    {
      "name": "stop_server",
      "description": "Stop the local LM Studio server (lms server stop).",
      "inputSchema": {"type": "object", "properties": {}}
    },
    {
      "name": "list_models",
      "description": "List models downloaded on this machine (lms ls) and models currently loaded into the running server (/v1/models).",
      "inputSchema": {"type": "object", "properties": {}}
    },
    {
      "name": "load_model",
      "description": "Load a downloaded model into the LM Studio server (lms load <model> --yes). The model id then becomes usable as model_id in an openai_compat delegation.",
      "inputSchema": {
        "type": "object",
        "properties": {"model": {"type": "string", "description": "Model identifier from list_models, e.g. qwen/qwen3-8b"}},
        "required": ["model"]
      }
    },
    {
      "name": "unload_model",
      "description": "Unload a loaded model from the LM Studio server (lms unload <model>).",
      "inputSchema": {
        "type": "object",
        "properties": {"model": {"type": "string"}},
        "required": ["model"]
      }
    },
    {
      "name": "download_model",
      "description": "Download a model from the LM Studio catalog (lms get <model> --yes). Long-running for large models; prefer small quantized models when trying something out.",
      "inputSchema": {
        "type": "object",
        "properties": {"model": {"type": "string", "description": "Catalog identifier, e.g. qwen/qwen3-8b or llama-3.2-1b-instruct"}},
        "required": ["model"]
      }
    }
  ]
}`
