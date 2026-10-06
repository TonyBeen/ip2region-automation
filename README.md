# ip2region automation

一个用于 Linux 的 Python updater，定时检查 `fa1seut0pia/ip2region-xdb` 最新 Release，下载 IPv4/IPv6 xdb，校验文件大小和 GitHub 提供的 SHA-256 后再一次性切换当前版本。

## 更新方式

数据目录默认是 `/var/lib/ip2region`：

```text
/var/lib/ip2region/
├── current -> releases/<version>
├── releases/<version>/
│   ├── ip2region_v4.xdb
│   ├── ip2region_v6.xdb
│   └── manifest.json
└── staging/
```

运行中的程序应从 `current/ip2region_v4.xdb` 和 `current/ip2region_v6.xdb` 读取。更新器先把两个文件完整下载到 staging，再核对大小和 SHA-256，生成 manifest 并将目录移入 releases，最后用原子 symlink 替换切换版本。失败时不会激活不完整版本。使用 `flock` 防止定时任务重叠。

更新器默认不假定消费 xdb 的服务或其 reload API。配置 `IP2REGION_RELOAD_COMMAND` 后，切换成功会执行该命令；命令可从环境变量 `IP2REGION_CURRENT` 和 `IP2REGION_VERSION` 读取新路径及版本。命令失败会把 `current` 原子回切到之前的版本。

## 手动运行

需要 Python 3.5+，无第三方 Python 依赖：

```sh
python3 updater.py --data-dir ./data
```

也可通过参数设置 HTTP 超时和 reload 命令：

```sh
python3 updater.py \
  --data-dir /var/lib/ip2region \
  --timeout 120 \
  --reload-command '/usr/local/bin/reload-ipinfo'
```

## Linux systemd 部署

系统需已安装 Python 3.5 和 venv 支持；更新器只用标准库，不需要安装 Python 包。部署脚本从仓库目录安装 updater、systemd 配置并启用定时器：

```bash
sudo bash scripts/deploy.sh
```

如果发行版把 venv 单独打包，请先安装 Python 3.5 对应的 venv 包。以后更新代码时，在仓库目录重新运行部署脚本即可。脚本会保留已有的 `/etc/default/ip2region-update` 配置。

`systemd` timer 启动后 5 分钟首次运行，随后每 6 小时检查一次；启动期间错过的任务会补跑。若要启用 reload，编辑 `/etc/default/ip2region-update`，设置 `IP2REGION_RELOAD_COMMAND`，并确保 `ip2region` 用户有权限执行该命令。

查看定时器状态和日志：

```sh
systemctl list-timers ip2region-update.timer
sudo systemctl start ip2region-update.service
journalctl -u ip2region-update.service
```

## 清理旧版本

更新器会保留所有已发布版本，不会自动删除数据库。清理脚本按目录修改时间保留当前版本和最近两个版本，并清除上次被强制中断留下的 staging 临时目录。默认先列出待删内容并要求确认：

```bash
sudo bash scripts/cleanup.sh
```

可用 `--keep` 调整要额外保留的旧版本数；`--yes` 跳过确认提示：

```bash
sudo bash scripts/cleanup.sh --keep 3
sudo bash scripts/cleanup.sh --keep 2 --yes
```

清理脚本会获取与 updater 相同的文件锁，若更新正在运行会退出，避免删除正在使用的文件。不要手动删除 `current` 指向的目录。

卸载定时任务但保留数据库：

```sh
sudo systemctl disable --now ip2region-update.timer
sudo rm -f /etc/systemd/system/ip2region-update.service /etc/systemd/system/ip2region-update.timer
sudo rm -f /etc/default/ip2region-update
sudo systemctl daemon-reload
```

确认不再需要数据库后，可再删除 `/var/lib/ip2region`；确认不再需要 updater 后，可删除 `/opt/ip2region-automation`。

## 运行约定

- 上游 Release 必须包含 `ip2region_v4.xdb` 和 `ip2region_v6.xdb`，并提供 `sha256:<hex>` digest 与正数 size。
- 每次更新两个数据库作为一个整体激活，避免 v4/v6 混用版本。
- 老版本会保留在 `releases/`，便于排查和手动回滚；参考“清理旧版本”章节定期释放磁盘空间。
- 应用需要跟随 `current` 符号链接访问文件，或在 reload 时重新打开文件。已打开的文件描述符不会因链接切换而自动指向新文件。
