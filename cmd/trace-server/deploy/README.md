# TRACE 远程版部署说明

`trace-server` 是 TRACE 的远程版，放在 Linux VPS（Debian 12、Ubuntu 22.04 一类，amd64 或 arm64）上运行。它只做四件事：按本地同步过去的订阅检查**启动之后**的新内容、按规则缓存、按规则录制直播、用 rclone 把完成的文件上传到网盘（如 Google Drive）。没有界面，本地 TRACE 在“设置 › 远程”查看状态、同步订阅与 Cookie。

包里有：`trace-server`（程序）、`trace-server.service`（systemd 示例）、本说明、`THIRD-PARTY-NOTICES.txt`、`VERSION.txt`。下面的命令按默认布局写：

| 用途 | 路径 |
| --- | --- |
| 程序 | `/opt/trace/trace-server` |
| 数据目录（设置、证书、凭证、数据库、日志） | `/var/lib/trace/data` |
| 导出目录（缓存的文件，布局与本地媒体库相同） | `/srv/trace/export` |
| rclone 配置 | `/var/lib/trace/rclone/rclone.conf` |

## 1. 安装程序与专用用户

```sh
sudo useradd --system --home-dir /var/lib/trace --shell /usr/sbin/nologin trace
sudo mkdir -p /opt/trace /var/lib/trace/rclone /srv/trace/export
sudo tar -xzf trace-server-*-linux-amd64.tar.gz -C /opt/trace --strip-components=1
sudo chown -R trace:trace /var/lib/trace /srv/trace
sudo chmod 700 /var/lib/trace /srv/trace/export
/opt/trace/trace-server version
```

arm64 的机器换成 `linux-arm64` 的包。解压前可以用包旁边的 `SHA256SUMS` 核对：`sha256sum -c SHA256SUMS --ignore-missing`。

## 2. 初始化，拿到连接码

用 `trace` 用户运行，数据目录才属于它：

```sh
sudo -u trace /opt/trace/trace-server init \
  --data /var/lib/trace/data --export /srv/trace/export \
  --public-address <VPS 的公网 IP 或域名> --name 东京VPS
```

- 打印的**连接码**只显示这一次（令牌只存哈希）。复制到本地 TRACE 的“设置 › 远程”。
- `--public-address` 写进连接码；不填时用本机第一个非回环地址，VPS 在 NAT 后面时那不是公网地址。默认监听 `:47380`，可用 `--listen` 改。
- 证书是自签名的，本地按连接码里的 SHA-256 指纹校验，不需要域名和 CA 证书。重新 `init` 会换证书，本地要粘贴新的连接码。
- 令牌丢了或泄露了：`sudo -u trace /opt/trace/trace-server token --data /var/lib/trace/data` 换新令牌并打印新连接码，旧令牌立即失效。

## 3. 防火墙

只需要放行一个 TCP 端口（默认 47380）。能固定本地出口 IP 时只放行它：

```sh
sudo ufw allow from <本地出口 IP> to any port 47380 proto tcp   # 或：sudo ufw allow 47380/tcp
```

云服务商的安全组也要放行同一端口。接口要求令牌；1 分钟内失败 5 次的来源 10 分钟内都会被拒绝。

## 4. 配置上传（rclone）

远程自己调用 rclone 上传：条目的文件全部写完后上传，直播每收尾一段就上传该段与更新后的 `item.json`，`item.json` 总在媒体文件之后上传；rclone 确认成功后才删除 VPS 上的文件。没有配置上传时文件留在导出目录。

1. 安装 rclone（官方安装脚本或发行版的包），确认 `rclone version` 可用。
2. 配置网盘，配置文件放在服务能写的位置（rclone 会刷新令牌并写回）：

   ```sh
   sudo -u trace env RCLONE_CONFIG=/var/lib/trace/rclone/rclone.conf rclone config
   ```

   Google Drive 建议用自己在 Google Cloud 控制台创建的 OAuth client_id 与 client_secret（rclone 文档的“Making your own client_id”），共用的默认 client_id 速度受限。VPS 没有浏览器时，按 rclone 的提示在有浏览器的电脑上运行它给出的 `rclone authorize` 命令，把结果粘回来。Google Drive 每个账号每天大约能上传 750 GiB，超过后远程状态显示配额错误并按退避重试。
3. 编辑 `/var/lib/trace/data/server.json` 的 `upload` 部分：

   ```json
   "upload": {
     "mode": "rclone",
     "remote": "gdrive:trace",
     "deleteAfterUpload": true
   }
   ```

   `remote` 是 rclone 的远端路径；`rclone`（程序路径，默认在 PATH 里找）、`config`（配置文件，不填时用环境变量 `RCLONE_CONFIG`）、`extraArgs`（额外参数，如 `["--bwlimit", "8M"]`）可选。
4. 测试一次（写入并删除一个测试文件）：

   ```sh
   sudo -u trace env RCLONE_CONFIG=/var/lib/trace/rclone/rclone.conf \
     /opt/trace/trace-server upload-test --data /var/lib/trace/data
   ```

改完 `server.json` 后重启服务才生效。

**不用内置上传、自己定时运行 rclone 时**，保持 `"mode": "none"`，并且一定排除未写完的文件：

```sh
rclone move /srv/trace/export gdrive:trace --exclude "*.part" --exclude ".trace-*" --min-age 2m --delete-empty-src-dirs
```

`trace-server rclone-filter` 会打印这组规则。远程不依赖导出目录里的文件继续存在，文件被搬走不影响缓存和录制。

## 5. systemd 服务

```sh
sudo cp /opt/trace/trace-server.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now trace-server
systemctl status trace-server
journalctl -u trace-server -f
```

示例服务用 `trace` 用户运行，`ProtectSystem=strict` 下只有 `/var/lib/trace` 和导出目录可写。改了路径时，`ExecStart`、`ReadWritePaths` 与 `RCLONE_CONFIG` 一起改。`systemctl stop` 发 SIGTERM：远程暂停任务、给录制收尾，最多 30 秒。日志也写在 `/var/lib/trace/data/logs/`，不含 Cookie、令牌和签名地址。

## 6. 连接本地 TRACE

本地“设置 › 远程”粘贴连接码 → 连接 → “同步订阅”，再在每个站点点“同步 Cookie”。之后本地续期、扫码登录或粘贴登录成功时会自动推送 Cookie；远程离线时本地标“Cookie 待同步”，恢复后自动重试。远程只缓存同步之后的新内容，不续期 Cookie，Cookie 失效时状态显示“需要重新同步”。

网盘上的文件下载到本地后，在本地媒体库“⋯ → 导入…”选那个文件夹，按预览确认即可；不需要停下远程。

## 7. 凭证保护的边界

站点 Cookie 用数据目录里的密钥（`credentials/secret.key`，`0600`）加密保存，令牌只存哈希，证书私钥 `0600`。这能防止只拷走数据库或日志就拿到 Cookie，**不能**防止拿到 VPS 的 root 或 `trace` 用户权限的人：他们能读密钥，也就能解密 Cookie。不要在别人能登录的机器上运行；不再使用时在本地“设置 › 远程”点“清除远程登录”，或删除数据目录。

## 8. 升级

两边需要同一协议版本，版本不同时本地显示“两边版本不同”。换包升级：

```sh
sudo systemctl stop trace-server
sudo tar -xzf trace-server-<新版本>-linux-amd64.tar.gz -C /opt/trace --strip-components=1
sudo systemctl start trace-server
```

数据目录保留不动。新版程序写过的数据，旧版程序会拒绝打开，不要降级。
