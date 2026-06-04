# OpenFabric REST & SSE API Specification

The OpenFabric agent exposes a local-only (binds to `127.0.0.1:4892` by default) REST + Server-Sent Events (SSE) API. This is the API used by both the local dashboard UI and the CLI tool.

---

## API Summary by Subsystem

### 1. Cluster Core
- `GET  /api/status` - Retrieve cluster summary (node count, total pooled RAM, total storage).
- `GET  /api/nodes` - List detailed resource utilization (CPU, RAM, GPU, Disk, OS) for all online cluster devices.
- `GET  /api/nodes/:id` - Get resource specs for a single node.
- `DELETE /api/nodes/:id` - Evict a node from the trusted cluster.

### 2. Shared Storage
- `GET  /api/storage` - Browse file metadata in the shared fabric storage.
- `POST /api/storage/upload` - Upload a file to the cluster storage.
- `GET  /api/storage/*` - Download/stream a file.
- `DELETE /api/storage/*` - Delete a file from the shared storage.

### 3. Task Execution
- `GET  /api/tasks` - List active running tasks and execution logs history.
- `POST /api/tasks` - Submit a new command task for scheduling: `{ command, preferred_node? }`.
- `GET  /api/tasks/:id` - Retrieve stdout/stderr logs and execution status of a task.
- `DELETE /api/tasks/:id` - Cancel a running task command.
- `GET  /api/scheduler/stats` - Retrieve scheduler operational metrics (queue depth, in-flight, breaker states).

### 4. Distributed LLM & Chat
- `GET  /api/llm/models` - List local/remote models and model sharding planner RAM feasibility.
- `DELETE /api/llm/models/:model` - Remove a model from the local disk.
- `GET  /api/llm/status` - Get local Ollama service status and list of local models.
- `POST /api/llm/pull` - Pull/download a model, streaming progress over SSE.
- `POST /api/llm/chat` - Submit a streaming chat completion request (supports `use_brain` RAG, `use_memory`, `mcp_servers`).
- `GET  /api/llm/sessions` - List all stored chat session histories.
- `POST /api/llm/sessions` - Start a new chat session.
- `GET  /api/llm/sessions/:id` - Get full message log of a chat session.
- `DELETE /api/llm/sessions/:id` - Delete a chat session and its local log.
- `PUT  /api/llm/sessions/:id/title` - Rename a chat session title.
- `GET  /api/llm/inference/sessions` - List active distributed inference sessions.
- `GET  /api/llm/inference/sessions/:id` - Retrieve detailed token/performance telemetry for an active distributed session.
- `GET  /api/llm/inference/capabilities` - Query and register local/cluster-wide model execution capabilities.

### 5. Fabric Brain (Semantic Knowledge RAG)
- `GET  /api/brain/status` - Get knowledge store vector index statistics.
- `POST /api/brain/reindex` - Trigger manual directory scan and vector re-indexing.
- `GET  /api/brain/search?q=` - Search the HNSW vector database directly.
- `DELETE /api/brain/index/:hash` - Delete index entries matching a file hash.

### 6. Fabric Flow (Automation)
- `GET  /api/flows` - List workflow automation configurations.
- `POST /api/flows` - Save a workflow definition (YAML/JSON).
- `GET  /api/flows/:id` - Get single workflow definition.
- `PUT  /api/flows/:id` - Update workflow definition.
- `DELETE /api/flows/:id` - Delete workflow definition and logs.
- `POST /api/flows/:id/run` - Manually trigger workflow execution.
- `PUT  /api/flows/:id/toggle` - Enable/disable workflow triggers.
- `GET  /api/flows/:id/runs` - Get runs execution history (up to 50).
- `GET  /api/flows/:id/runs/:run_id` - Get detailed step status per run.
- `DELETE /api/flows/:id/runs/:run_id` - Delete a run record.

### 7. Fabric SDN (Software Defined Networking)
- `GET  /api/sdn/status` - Retrieve current SDN synchronization status, active version, hash, and rules dump.
- `POST /api/sdn/apply` - Upload and deploy a new YAML network topology configuration.
- `POST /api/sdn/rollback` - Revert the SDN topology to the previous version.
- `GET  /api/sdn/telemetry` - Retrieve real-time connection flow logs and policy matches.

### 8. Model Context Protocol (MCP)
- `GET  /api/mcp/builtins` - List pre-installed integration specs.
- `GET  /api/mcp/servers` - List active and configured MCP servers.
- `POST /api/mcp/servers` - Save/connect credentials to an MCP integration.
- `DELETE /api/mcp/servers/:name` - Disconnect/uninstall an MCP integration.
- `PUT  /api/mcp/servers/:name/toggle` - Enable/disable an MCP server.
- `GET  /api/mcp/servers/:name/tools` - List tools exposed by a server.
- `POST /api/mcp/servers/:name/test` - Spawn server and test tool return payload.
- `GET  /api/mcp/servers/tools/all` - List all active tools cluster-wide.

### 9. Fabric Memory & Pulse
- `GET  /api/memory` - List all context memory facts.
- `POST /api/memory` - Add a new fact manually.
- `DELETE /api/memory/:id` - Remove a memory fact.
- `DELETE /api/memory` - Clear all context memories.
- `GET  /api/memory/search?q=` - Query semantic context.
- `GET  /api/pulse/insights` - List proactive resource warning cards.
- `POST /api/pulse/insights/:id/dismiss` - Dismiss an active insight card.
- `GET  /api/pulse/history` - Get Pulse observer check histories.
- `GET  /api/pulse/weekly` - Get weekly digest usage stats.

### 10. Autonomous Agents
- `GET  /api/agents` - List goal execution logs and history.
- `POST /api/agents` - Spawn an agent: `{ goal, tools }`.
- `GET  /api/agents/templates` - Get pre-made task templates.
- `GET  /api/agents/:id` - Get status and timeline steps.
- `POST /api/agents/:id/cancel` - Cancel active agent planning.
- `GET  /api/agents/:id/log` - Fetch plain text timeline logging.
- *Swarm tools available to agents*:
  - `list_cluster_nodes` - Lists available cluster nodes for sub-agent allocation.
  - `spawn_sub_agent` - Spawns a sub-agent actor on a target node: `{ goal, node_id?, tools? }`.

### 11. Multi-Modal Pipelines
- `POST /api/pipelines/run` - Start a multi-device pipeline run. Accepts `multipart/form-data`:
  - `pipeline` (string): JSON representation of a Pipeline.
  - `audio` (file): Raw PCM audio input file.

### 12. GPU Pooling
- `GET  /api/gpu/status` - GPU cluster statistics (pooled VRAM usage, devices count).
- `GET  /api/gpu/nodes` - List detailed specs of GPU-enabled nodes.
- `POST /api/gpu/generate` - Route prompt to AUTOMATIC1111/ComfyUI: `{ prompt, model, size }`.
- `GET  /api/gpu/generate/:id` - Check generation job status.
- `GET  /api/gpu/models` - Query available safetensors model checkpoints.
- `POST /api/gpu/install/:model` - Download/install a safetensors model.

### 13. Cluster Onboarding & Joins
- `GET  /api/cluster/join-token` - Generate 10-minute temporary join codes.
- `POST /api/cluster/join` - Run coordinator onboarding join handshake.
- `POST /api/cluster/join-remote` - Direct local agent to connect to remote coordinator.
- `GET  /join/:token` - Serve landing onboarding helper webpage.

### 14. Fabric Tunnel (Secure Remote Access)
- `GET  /api/tunnel/status` - Current tunnel status (connecting/connected/disconnected) and peers.
- `POST /api/tunnel/enable` - Start WireGuard tunneling and proxy remote request handshakes.
- `POST /api/tunnel/disable` - Terminate WireGuard remote tunnel daemon process.
- `GET  /api/tunnel/peers` - Fetch lists of tunnel IP nodes connected to this relay interface.
- `POST /api/tunnel/pin/generate` - Produce a secure 6-digit Remote Access PIN.
- `DELETE /api/tunnel/pin` - Revoke remote PIN access security.
- `GET  /api/tunnel/config` - Export raw client WireGuard peer configurations.
- `PUT  /api/tunnel/relay` - Configure custom self-hosted proxy relay URLs.
- `GET  /api/tunnel/bandwidth` - Read real-time download and upload bytes.

### 15. Wake-on-LAN (WOL)
- `GET  /api/wol/devices` - Query list of registered MAC device targets.
- `POST /api/wol/devices` - Save a new target device configuration.
- `DELETE /api/wol/devices/:mac` - Unregister a WoL MAC profile from settings.
- `POST /api/wol/wake/:mac` - Direct agent to build and send 102-byte UDP magic packets.
- `GET  /api/wol/scan` - Execute local network scanner fetching target physical ARP addresses.

### 16. Fabric Bench (Benchmarking)
- `GET  /api/bench/reports` - List all stored benchmark reports.
- `GET  /api/bench/reports/:id` - Fetch a specific benchmark report.
- `GET  /api/bench/latest` - Fetch the most recent benchmark report.
- `GET  /api/bench/payload` - Serve fixed-size zero-allocation payload for network throughput test.
- `POST /api/bench/run` - Trigger a benchmark run and stream progress over SSE.

### 17. Fabric Reliability & Operational Metrics
- `GET  /api/health` - Retrieve overall cluster/node health checks report.
- `GET  /api/metrics` - Query active operational metrics and expvar counters.

### 18. OpenAI Compatibility API
- `POST /v1/chat/completions` - OpenAI compatible Completions stream endpoint.
- `GET  /v1/models` - OpenAI compatible models catalogue.
