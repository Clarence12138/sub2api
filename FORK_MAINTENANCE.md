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
UPSTREAM_REF=upstream/v0.1.170 ./scripts/sync-upstream.sh
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
git merge upstream/main   # 或 upstream/v0.1.xxx
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

## 发版镜像

本 fork 发布到：

```text
ghcr.io/clarence12138/sub2api
```

在已验证的 `main` 上打 tag 并推送：

```bash
git tag v0.1.169-clarence.1
git push origin main --tags
# 或只推 tag：
# git push origin v0.1.169-clarence.1
```

约定：

| 类型 | 格式 | 示例 |
|------|------|------|
| 官方版本 | `vX.Y.Z` | `v0.1.169` |
| 本 fork 发版 | `vX.Y.Z-clarence.N` | `v0.1.169-clarence.1` |

同一官方版本上的二次发补丁递增 `N`：`clarence.2`、`clarence.3`。

推送后会发布例如：

```text
ghcr.io/clarence12138/sub2api:0.1.169-clarence.1
ghcr.io/clarence12138/sub2api:latest
```

生产机（`ccs`）只拉已发布镜像并重启，**禁止**在上面 `go build` / `pnpm build` / `docker build`。GitHub Actions 或 GHCR 不可用时停下来，不要把生产机当构建回退。详见 `AGENTS.md`。

## 当前定制（摘要）

本 fork 在官方之上保留的产品向改动包括：

- 部署默认指向 Clarence fork 与 GHCR 镜像
- Ops 看板健康分阈值可配置
- TTFT 诊断与配置阈值对齐（非写死常量）
- 上游额度刷新邮件通知
- 管理员批量重置订阅额度等二开能力

同步官方时若冲突，优先保留上述定制语义，再吸收上游修复/功能。

## 相关文档

- 分支命名与 commit 规范：`AGENTS.md`
- 本地环境与常见坑：`DEV_GUIDE.md`
