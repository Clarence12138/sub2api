# Fork 维护指南

本仓库是 [Wei-Shaw/sub2api](https://github.com/Wei-Shaw/sub2api) 的二开 fork。

## 远程与分支模型

| 名称 | 含义 |
|------|------|
| `origin` | `git@github.com:Clarence12138/sub2api.git`（本 fork） |
| `upstream` | `https://github.com/Wei-Shaw/sub2api.git`（官方，只读跟踪） |
| **`origin/main`** | **唯一产品主干 / 可部署线** = 官方代码 + 本 fork 定制 |
| `upstream/main` | 官方主干（不要在上面开发，也不要假装 `origin/main` 是纯官方） |
| `feat/*` `fix/*` `chore/*` | 短生命周期分支，从 `main` 拉出，合回 `main` 后删除 |

**不采用**「`main` 纯同步官方 + 长期 `dev` 二开」双主干模型。  
「纯官方」只通过 `upstream` remote 查看；所有开发与部署都以 `origin/main` 为准。

```text
upstream/main  ──定期 merge──►  origin/main（官方 + 二开，可部署）
                                   │
                              feat/* / fix/* / chore/upgrade-*
```

## 日常开发

单人开发，合回 `main` **默认本地 fast-forward**，不提 PR。
`main` 若有新提交，先把功能分支 rebase 到 `origin/main`，再快进。不要把官方直接 merge 进功能分支。

```bash
# 1. 更新本地 main
git fetch origin
git switch main
git pull --ff-only origin main

# 2. 从 main 开功能分支（命名见 AGENTS.md）
git switch -c feat/your-feature

# 3. 开发、小步 commit（Conventional Commits + 中文）

# 4. 合回 main（fast-forward）
git fetch origin
git switch feat/your-feature
git rebase origin/main          # main 没动也可以跑，是空操作
git switch main
git pull --ff-only origin main
git merge --ff-only feat/your-feature
git push origin main

# 5. 删功能分支
git branch -d feat/your-feature
git push origin --delete feat/your-feature   # 若曾推过远端分支
```

若功能分支已经推过远端、rebase 后需要更新远端分支：`git push --force-with-lease`。
**不要**对已共享的 `main` 做 rebase + force。

PR 只用于冲突多的官方升级（见下文），或明确需要单独留变更说明的情况。

## 同步官方（merge，不是 rebase）

`main` 上已有二开提交与发版 tag，主干同步使用 **merge**，避免对 `main` 做 rebase + force-push。

### 推荐：脚本

```bash
# 工作区必须干净
./scripts/sync-upstream.sh

# 或指定本地分支 / 上游引用（默认 main + upstream/main）
./scripts/sync-upstream.sh main upstream/main

# 合并到官方某个 tag
UPSTREAM_REF=v0.1.170 ./scripts/sync-upstream.sh
```

脚本会：

1. `git fetch upstream --tags`
2. 切换到目标分支（默认 `main`）
3. `git merge` 上游引用

若有冲突：按提示解决后 `git add` + `git commit` 完成合并。

### 手动等价流程

```bash
git fetch upstream --tags
git switch main
git merge upstream/main
# 冲突则解决后 commit
# 建议 commit message：
#   chore(upgrade): 升级 Sub2API 到 0.1.xxx
#   （正文可写：合并官方、保留哪些 fork 定制、VERSION 设为 x.y.z-clarence.N）
```

### 冲突多时：升级专用分支 + PR

```bash
git fetch upstream --tags
git switch -c chore/upgrade-sub2api-v0.1.xxx main
git merge upstream/main   # 固定 Release 使用已获取的 v0.1.xxx tag 或解引用后的提交 SHA
# 解冲突、本地验证
git push -u origin HEAD
# 开 PR：chore/upgrade-… → main，合并后再打 tag
```

### 同步后

```bash
# 验证构建 / 测试后推送（merge 后一般不需要 force）
git push origin main

# 需要发版镜像时再打 tag（见下一节）
```

对比官方差异（不改分支）：

```bash
git fetch upstream
git log --oneline main..upstream/main    # 官方多了什么
git diff main...upstream/main            # 代码差异
```

## 发版与上线

镜像仓库：`ghcr.io/clarence12138/sub2api`。

| 类型 | Git tag | 镜像 tag | 示例 |
|------|---------|----------|------|
| 官方版本 | `vX.Y.Z` | `X.Y.Z` | `v0.1.178` |
| 本 fork 发版 | `vX.Y.Z-clarence.N` | `X.Y.Z-clarence.N` | `v0.1.178-clarence.3` |

同一官方版本上的二次发补丁递增 `N`：`clarence.2`、`clarence.3`。

生产机（`ccs`）**禁止** `go build` / `pnpm build` / `docker build`。详见 `AGENTS.md`。

### 日常改动：本机热部署（默认）

小功能 / bugfix 不要打 tag 等 GHCR（Actions 大约 12 分钟）。在本机交叉编译 `linux/amd64`，把镜像 load 到 `ccs` 再换 pin：

```bash
./scripts/hot-deploy.sh                     # 自动下一个 clarence.N
./scripts/hot-deploy.sh 0.1.178-clarence.4  # 指定版本
./scripts/hot-deploy.sh --yes               # 非交互
./scripts/hot-deploy.sh --dry-run           # 只看计划
```

脚本会：构建前端 → `GOOS=linux GOARCH=amd64` 编进 embed 二进制 → 用 `Dockerfile.goreleaser` 打运行时镜像 → `docker save` 到 `ccs` → 改 compose pin → `up -d`。

不会创建或推送 git tag。**不要**为热部署 `git push origin v*`，那会触发 Release workflow。
也不要在 `ccs` 上对热部署 tag 执行 `docker compose pull`（GHCR 里还没有这个 tag）。

### 合并升级官方：走 GHCR

`./scripts/sync-upstream.sh` 或 `chore/upgrade-*` 合进 `main` 并验证后，打 tag 推送，等 Actions 出镜像，再在 `ccs` pull 换 pin：

```bash
git tag v0.1.178-clarence.1
git push origin main
git push origin v0.1.178-clarence.1
# 或只推 tag：
# git push origin v0.1.178-clarence.1
```

推送后会发布例如：

```text
ghcr.io/clarence12138/sub2api:0.1.178-clarence.1
ghcr.io/clarence12138/sub2api:latest
```

## 当前定制（摘要）

本 fork 在官方之上保留的产品向改动包括：

- 部署默认指向 Clarence fork 与 GHCR 镜像
- Ops 看板健康分阈值可配置
- TTFT 诊断与配置阈值对齐（非写死常量）
- 上游额度刷新邮件通知
- 管理员批量重置订阅额度，周/月窗口锚定操作时刻
- OpenAI 7d 额度窗口快照、官方重置识别与分钟级抖动容忍；不记录/展示 5h 统计窗口
- 账号金额/额度占比图、每 1% 花费、缩放和平移
- 请求入口 `edge_name` / `entry_host` 全链路记录，与官方 `upstream_request_id` 并存
- cyber_policy 完整请求/提示词落库，与 WebSocket 多路径标记、去重、失败切换兼容
- GPT-5.5（含现有 Pro 定制）/5.6/GPT-6 Astra Fast/priority **默认按标准价 2.5 倍扣额度**；GPT-6 支持 `gpt-6`、`gpt-6-astra` 及 Astra 日期/版本后缀、供应商前缀。保留显式渠道 `FastMultiplier`（含 0）优先级，不使用目录 API priority 2 倍价再次计费
- 普通档不提价，Flex 和 GPT-5.4 保持原规则；Ultrafast 保留上游独立 2 倍规则，不归入 Fast 2.5 倍
- `free_openai_fast` 默认关闭；管理员显式开启时仅用户 ActualCost/扣额度按普通档，TotalCost 与上游档位仍保留 Fast 记录；`force_openai_fast` 默认关闭
- 认证快照保留长上下文分组开关；旧快照版本淘汰回源

### v0.2.1 升级核对

- 官方固定基线：`578785ee7fb35030b094b69624efe25670a36f5f`。
- 从 `0.1.185-clarence.1` merge；上游完整差异 353 个文件，与 fork 共同修改 44 个文件。
- 手写用量 INSERT/SELECT 同时保留入口字段和上游请求 ID，参数表、扫描与批量路径同步。
- 账号精简列表的编辑和统计入口先获取完整详情，不用精简 DTO 覆盖通知配置。
- 保留已发布的 `192_add_usage_log_edge_ingress.sql`、`222_account_usage_windows.sql`、`223_account_usage_window_samples.sql`，不改名、不改 checksum。
- 新增 Fast 分组开关默认关闭；数据库升级与重启幂等在隔离 PostgreSQL 演练，测试不连接生产或发送真实邮件。

同步官方时若冲突，优先保留上述定制语义，再吸收上游修复/功能。

## 相关文档

- 分支命名与 commit 规范：`AGENTS.md`
- 本地环境与常见坑：`DEV_GUIDE.md`
