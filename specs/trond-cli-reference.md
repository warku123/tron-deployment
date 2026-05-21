# trond CLI Reference

> 2026-05-21 整理。`feat/monitoring` 分支的完整命令参考。

## Command Hierarchy

```
trond (root)
├── apply (alias: deploy)
├── bootstrap
├── build
├── remove <node>
├── restart <node>
├── start <node>
├── stop <node>
├── upgrade <node>
├── rollback <node>
├── status [node]
├── list
├── inspect [node]
├── logs <node>
├── events
├── exec <node> -- <cmd> [args...]
├── wait <node>
├── verify
├── verify-config <node>
├── health <node>
├── diagnose <node>
├── doctor
├── auto-heal <node>
├── preflight
├── plan
├── recipe
│   ├── list
│   ├── show <name>
│   └── run <name>
├── files
│   ├── put <node> <local-src> <remote-dst>
│   └── get <node> <remote-src> <local-dst>
├── version
├── schema [command-path]
├── mcp
├── disconnect <node-a> <node-b>
├── connect <node-a> <node-b>
├── partition --groups 'a,b|c,d'
├── heal --groups 'a,b|c,d'  (chaos)
├── knowledge [topic]
├── completion
├── config
│   ├── validate <intent-path>
│   ├── render <intent-path>
│   ├── diff <intent-path>
│   └── docs <key> (alias: explain)
├── network
│   ├── create
│   ├── add
│   ├── status
│   ├── destroy
│   └── upgrade <network-name>
└── snapshot
    ├── sources
    ├── list
    ├── download
    ├── jobs
    ├── logs <job-id>
    ├── stop <job-id>
    └── prune
```

---

## 全局 Flags（所有子命令可用）

| Flag | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `--output, -o` | string | `text` | 输出格式: text / json |
| `--log-format` | string | `text` | 日志格式: text / json |
| `--quiet, -q` | bool | `false` | 静默非必要输出 |
| `--verbose, -v` | bool | `false` | 增加日志详细度 |
| `--no-color` | bool | `false` | 禁用 ANSI 颜色 |
| `--config` | string | `""` | 配置文件路径 (默认 ~/.trond/config.yaml) |
| `--state-dir` | string | `""` | 状态目录 (默认 ~/.trond, 环境变量 TROND_STATE_DIR) |

---

## 部署

### `trond apply`（别名: deploy）

部署或更新节点。

| Flag | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `--intent` | string | `""` | intent.yaml 路径（必填） |
| `--auto-approve` | bool | `false` | 跳过确认（CI 模式） |
| `--wait` | bool | `false` | 等待节点 HTTP API 可达 |
| `--wait-timeout` | duration | `5m` | wait 超时时间 |
| `--monitor` | bool | `false` | 部署 Prometheus + Grafana 监控栈 |
| `--no-monitor` | bool | `false` | 跳过监控部署（覆盖 intent 中的 monitoring.enabled=true） |

```bash
trond apply --intent node.yaml
trond apply --intent node.yaml --monitor --auto-approve
trond apply --intent node.yaml --wait
```

### `trond plan`

预览变更，不执行。

| Flag | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `--intent` | string | `""` | intent.yaml 路径（必填） |
| `--diff` | bool | `false` | 包含逐行 HOCON diff |

```bash
trond plan --intent node.yaml
trond plan --intent node.yaml --diff
```

### `trond preflight`

部署前检查目标机是否就绪。

| Flag | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `--intent` | string | `""` | intent.yaml 路径（必填） |

### `trond bootstrap`

在目标机上安装依赖（Docker 或 JDK）。

| Flag | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `--intent` | string | `""` | intent.yaml 路径（必填） |

### `trond verify`

部署后健康检查。

| Flag | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `--intent` | string | `""` | intent.yaml 路径（必填） |
| `--timeout` | duration | `10m` | 超时时间 |

### `trond verify-config <node>`

对比运行中节点的配置与 intent 的差异（配置漂移检测）。

| Flag | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `--intent` | string | `""` | intent.yaml 路径（必填） |
| `--context` | int | `0` | diff 上下文行数 |

---

## 生命周期管理

### `trond upgrade <node>`

升级节点到新版本。

| Flag | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `--version` | string | `""` | 目标版本（必填） |

### `trond rollback <node>`

回滚到上一个版本。无额外 flag。

### `trond restart <node>`

重启节点（stop + start）。无额外 flag。

### `trond start <node>`

启动已停止的节点。无额外 flag。

### `trond stop <node>`

停止运行中的节点。无额外 flag。

### `trond remove <node>`

移除节点。

| Flag | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `--keep-data` | bool | `false` | 保留数据卷/目录 |
| `--confirm` | string | `""` | 重复节点名确认删除 |

---

## 观测

### `trond status [node]`

显示节点状态。无参数时列出所有节点。

### `trond list`

列出所有受管节点。

| Flag | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `--label` | stringArray | `[]` | 按标签过滤 (key=value, 可重复, AND) |

### `trond inspect [node]`

输出拓扑详情（JSON）。

| Flag | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `--all` | bool | `false` | 检查所有节点 |
| `--network` | string | `""` | 按网络名过滤 |
| `--label` | stringArray | `[]` | 按标签过滤 |

### `trond health <node>`

快速健康检查。无额外 flag。

### `trond diagnose <node>`

完整诊断报告。无额外 flag。

### `trond doctor`

trond 自检。检查版本、状态目录、Docker、文件权限等。

| Flag | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `--check-update` | bool | `false` | 同时检查 GitHub 最新版本 |

### `trond auto-heal <node>`

诊断 + 自动修复已知安全的故障。

| Flag | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `--dry-run` | bool | `false` | 只打印拟操作，不执行 |
| `--only` | stringSlice | `[]` | 只检查指定的诊断项 |

### `trond logs <node>`

查看节点日志。

| Flag | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `--tail` | int | `100` | 显示行数 |
| `--follow, -f` | bool | `false` | 持续输出 |

### `trond events`

查看审计日志（JSONL）。

| Flag | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `--follow, -f` | bool | `false` | 持续输出新事件 |
| `--since` | duration | `0s` | 只显示此时间之后的事件 |

### `trond exec <node> -- <cmd> [args...]`

在节点内执行命令。

```bash
trond exec my-node -- curl -s http://127.0.0.1:8090/wallet/getnowblock
```

### `trond wait <node>`

等待节点探针满足条件。

| Flag | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `--port` | int | `0` | TCP 端口探针 |
| `--http` | string | `""` | HTTP URL 探针；{http} 展开为节点 HTTP 地址 |
| `--exec` | string | `""` | Shell 命令探针；成功 = exit 0 |
| `--timeout` | duration | `5m` | 总等待预算 |
| `--interval` | duration | `2s` | 轮询间隔 |
| `--json-path` | string | `""` | HTTP 响应中的 JSON 路径 |
| `--json-eq` | string | `""` | --json-path 的值等于此字符串时成功 |
| `--json-gt` | float64 | `0` | --json-path 的值大于此数字时成功 |

---

## 构建

### `trond build`

从 java-tron 源码构建 JAR 或镜像。

| Flag | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `--source` | string | `""` | java-tron 源码路径（必填） |
| `--revision` | string | `HEAD` | Git 版本 (HEAD/branch/tag/sha) |
| `--artifact` | string | `jar` | 产物类型: jar / image |
| `--jdk` | string | `8` | JDK 版本 (8/11/17/21) |
| `--gradle-task` | string | `""` | Gradle task（默认 jar→shadowJar, image→dockerBuild） |
| `--gradle-arg` | stringArray | `[]` | gradle 额外参数（可重复） |
| `--builder` | string | `docker` | 构建后端: docker / host |
| `--tag` | string | `""` | image 的 tag（artifact=image 时） |
| `--builder-image-override` | string | `""` | 覆盖构建镜像 |
| `--platform` | string | `""` | 构建平台 (linux/amd64 或 linux/arm64) |

```bash
trond build --source ./java-tron --artifact jar -o json
trond build --source ./java-tron --revision v4.7.7 --gradle-arg=--offline
```

---

## 多节点私网

### `trond network create`

创建私网。

| Flag | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `--intent` | string | `""` | intent.yaml 路径（必填） |
| `--monitor` | bool | `false` | **部署 Prometheus + Grafana 监控栈** |

> **与 apply 不同**：`network create` 默认不启动监控，必须显式传 `--monitor`。

```bash
trond network create --intent private-net.yaml
trond network create --intent private-net.yaml --monitor
```

### `trond network add`

向已有私网添加节点。

| Flag | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `--network` | string | `""` | 网络名（必填） |
| `--intent` | string | `""` | 单节点 intent（必填） |

### `trond network status`

显示网络内所有节点状态。无额外 flag。

### `trond network destroy`

销毁私网。

| Flag | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `--confirm` | string | `""` | 重复网络名确认 |

### `trond network upgrade <network-name>`

滚动升级网络内所有节点。

| Flag | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `--version` | string | `""` | 目标版本（必填） |
| `--auto-rollback` | bool | `false` | 失败时自动回滚 |
| `--witness-first` | bool | `false` | 先升级 witness（默认先 fullnode） |
| `--verify-timeout` | duration | `5m` | 每个节点的验证超时 |
| `--intent` | string | `""` | 原始 intent.yaml（必填） |

---

## 配置工具

### `trond config validate <intent-path>`

验证 intent 文件。

| Flag | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `--explain` | bool | `false` | 打印每个字段的值来源（显式/默认/推导） |

### `trond config render <intent-path>`

渲染配置。

| Flag | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `--output-dir` | string | `""` | 输出目录 (默认 stdout) |
| `--overlay` | string | `""` | 合并覆盖的 intent |
| `--node` | int | `-1` | 只渲染指定索引的节点 |

### `trond config diff <intent-path>`

对比渲染配置与已部署配置。

### `trond config docs <key>`（别名: explain）

查询 HOCON 配置项的文档。

---

## 快照

### `trond snapshot sources`

列出已知快照源。无额外 flag。

### `trond snapshot list`

列出可用的备份。

| Flag | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `--network` | string | `""` | 网络: mainnet / nile |
| `--domain` | string | `""` | 镜像域名 |
| `--type` | string | `lite` | 快照类型: lite / full |
| `--region` | string | `""` | 区域: singapore / america |
| `--db-engine` | string | `""` | 引擎: leveldb / rocksdb |

### `trond snapshot download`

下载快照。

| Flag | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `--network` | string | `""` | 网络: mainnet / nile |
| `--domain` | string | `""` | 镜像域名 |
| `--type` | string | `lite` | 类型: lite / full |
| `--region` | string | `""` | 区域 |
| `--db-engine` | string | `""` | 引擎 |
| `--backup` | string | `""` | 指定备份名 (默认 latest) |
| `--to` | string | `""` | 目标目录 (默认 ./output-directory) |
| `--node` | string | `""` | 受管节点名，自动推导 --to |
| `--force` | bool | `false` | 覆盖已有数据 |
| `--no-verify` | bool | `false` | 跳过 MD5 校验 |
| `--dry-run` | bool | `false` | 只打印不执行 |
| `--detach` | bool | `false` | 后台运行 |

### `trond snapshot jobs`

列出后台下载任务。无额外 flag。

### `trond snapshot logs <job-id>`

查看后台任务日志。

| Flag | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `--follow, -f` | bool | `false` | 持续输出 |
| `--lines, -n` | int | `0` | 最后 N 行 (0=全部) |

### `trond snapshot stop <job-id>`

停止后台任务。

| Flag | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `--force` | bool | `false` | 使用 SIGKILL |

### `trond snapshot prune`

清理旧的后台任务记录。

| Flag | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `--older-than` | duration | `168h` | 清理此时间之前的任务 |
| `--dry-run` | bool | `false` | 只打印不执行 |
| `--all` | bool | `false` | 清除所有（同 --older-than 0） |

---

## 混沌工程

### `trond disconnect <node-a> <node-b>`

隔离两个节点（docker 网络层）。无额外 flag。

### `trond connect <node-a> <node-b>`

恢复两个节点的连接。无额外 flag。

### `trond partition --groups 'a,b|c,d'`

将节点划分为隔离组。

| Flag | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `--groups` | string | `""` | 管道分隔的逗号分隔分组（必填） |

### `trond heal --groups 'a,b|c,d'`

恢复 partition 操作。

| Flag | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `--groups` | string | `""` | 与 partition 相同格式（必填） |

---

## 其他

### `trond files`

文件传输。

```bash
trond files put <node> <local-src> <remote-dst>   # 上传
trond files get <node> <remote-src> <local-dst>   # 下载
```

### `trond version`

打印版本。

| Flag | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `--check-update` | bool | `false` | 同时检查最新版本 |

### `trond schema [command-path]`

输出 CLI 结构清单（JSON Schema），供 AI agent 消费。

| Flag | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `--output-only` | bool | `false` | 只输出指定命令的 JSON Schema |

### `trond mcp`

启动 MCP 服务器（stdio），供 AI agent 直接调用。

### `trond knowledge [topic]`

查询部署知识库。无额外 flag。

### `trond completion`

生成 shell 补全脚本。

| Flag | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `--install` | bool | `false` | 写入 shell 默认位置 |

### `trond recipe`

预构建工作流。

```bash
trond recipe list
trond recipe show <name>
trond recipe run <name>
  --param key=value        # 参数 (可重复)
  --dry-run                # 只打印不执行
  --resume-from <step>     # 从指定步骤恢复
```

---

## 监控功能对比（feat/monitoring）

| 命令 | 默认行为 | `--monitor` |
|------|---------|------------|
| `trond apply` | 跟随 intent 中 `monitoring.enabled` | 强制启用 |
| `trond network create` | **不启动监控** | 启用监控 |
| `trond remove <node>` | 自动清理监控栈（如果存在） | — |
| `trond network destroy` | 自动清理监控栈（如果存在） | — |

### 监控 intent 示例

```yaml
name: my-node
target: {type: local, runtime: docker}
network: mainnet

monitoring:
  enabled: true            # apply 时生效，network create 时需 --monitor
  prometheus:
    port: 9090             # 默认 9090
    retention: 7d          # 默认 7d
  grafana:
    port: 3000             # 默认 3000

nodes:
  - type: fullnode
```

部署后访问：
- **Grafana**: http://localhost:3000 (admin/admin)，5 个预置 dashboard
- **Prometheus**: http://localhost:9090
