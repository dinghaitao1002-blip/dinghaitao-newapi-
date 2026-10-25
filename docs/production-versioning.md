# Nicole NewAPI 版本管理

此目录保存 2026-10-25 的生产源码快照；建立 Git 版本不等于部署。

## 代码位置

- GitHub 私有仓库：https://github.com/dinghaitao1002-blip/dinghaitao-newapi-
- 版本分支：`codex/versioned-baseline`
- 服务器版本目录：`/home/user1/dingjx/newapi/source-managed`
- 本地版本目录：`/Users/doudou/Documents/ChatGPT/newapi/source`
- 当前生产构建源码目录：`/home/user1/dingjx/newapi/src`。它仍保留原分支和配置，运行服务未切换。
- 官方上游：`https://github.com/Calcium-Ion/new-api.git`
- 上游基线：`bc14c18f6024e79cba1c08d02cd007796e12d668`。本次没有顺带升级上游。

当前渠道分组、密价、分组调度、渠道故障保护及图片处理等定制源码均从生产目录保存，包含此前未提交的修改和新增源码。用户、渠道、价格等数据库内容必须单独备份，Git 不保存这些运行数据。

## 以后保存修改

先在版本目录创建工作分支，再修改和验证：

```sh
git switch codex/versioned-baseline
git switch -c codex/my-change
git status --short
git diff
# 显式选择完成审查的文件；不要加入数据库、日志、密钥和排障备份。
git add path/to/changed-file
git commit -m "说明本次修改"
git push -u origin codex/my-change
```

代码审查和测试完成后，再合并回维护分支。提交作者默认标识为自动维护程序；可以在这个仓库内设置自己的真实 Git 作者信息。

## 合并官方更新

```sh
git fetch upstream
git switch -c codex/upstream-update codex/versioned-baseline
git merge upstream/main
```

解决冲突时保留自定义功能，执行对应 Go 测试和前端构建。不要直接用官方代码覆盖生产目录，也不要在生产上盲目运行 `git pull` 后重启。使用单独版本标签构建镜像，经验证再通过现有部署流程切换。保留上一个镜像用于回退。

## 回看基线与配置

```sh
git show nicole-baseline-20261025 --stat
git diff nicole-baseline-20261025..HEAD
git switch -c codex/restore-baseline nicole-baseline-20261025
```

以上只操作源码；回退运行服务需要选择相应镜像及兼容的数据库，不能把源码切换当作服务已回退。

`docker-compose.custom.yml` 是从旧配置脱敏得到的参考文件，保留原有镜像名、挂载路径等，不代表目前生产入口。真实部署以现有生产 Compose 为准。环境变量通过仓库外的私有文件提供，`.env.custom.example` 只有空占位符；沿用原有会话密钥，不能在本次版本保存时更换。

旧 `custom-nicole` 历史的自定义提交含有运行凭据，已保存在服务器和本机私有恢复备份中，不上传到 GitHub。只推送明确指定的新分支和标签，禁止对旧仓库执行 `git push --all` 或 `--mirror`。
