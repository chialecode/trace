# 哔哩哔哩协议资料

核对：2026-11-25（第 16～20 节关注列表、登录续期、弹幕、字幕、直播已完成 [ACT-005](https://github.com/chialecode/trace/blob/f8af16863dbe885cfd63de048730c07a2b91e7be/USER-ACTIONS.md#act-005) 真实站点有界检查与夹具验证；第 21 节已完成 ACT-020 真实站点检查；第 7、9、13、19 节的 P1 空间投稿搜索/排序/分区、零请求视频章节与充电专属/动态 `onlyfansAssetsV2` 已完成 ACT-030 真实站点有界检查）。内容是社区观察到的网页协议，加上 TRACE 自己计算的固定向量。端点不是站点对 TRACE 的稳定承诺；站点改版后要重新核对。规则见[站点规则](../dev-rules/sources-and-safety.md#site-rules)。

## 1. 验证状态

本页协议在真实站点上做过 [ACT-002](https://github.com/chialecode/trace/blob/f8af16863dbe885cfd63de048730c07a2b91e7be/USER-ACTIONS.md#act-002)、[ACT-003](https://github.com/chialecode/trace/blob/f8af16863dbe885cfd63de048730c07a2b91e7be/USER-ACTIONS.md#act-003)（2026-11-04）、[ACT-004](https://github.com/chialecode/trace/blob/f8af16863dbe885cfd63de048730c07a2b91e7be/USER-ACTIONS.md#act-004)（2026-11-05）、[ACT-005](https://github.com/chialecode/trace/blob/f8af16863dbe885cfd63de048730c07a2b91e7be/USER-ACTIONS.md#act-005)（2026-11-07）、ACT-020（2026-11-14）与 ACT-030（2026-11-25，[#97](https://github.com/chialecode/trace/issues/97)）有界检查：

- **ACT-002 第一轮**：访客 `nav`、`card` 通过，投稿列表 `arc/search` 第 1 页返回 `-412` / HTTP 412，按停止条件停止；已登录因测试 Cookie 失效（`nav` 返回 `-101`）停止。
- **ACT-002 第二轮**（更新 Cookie 后）：访客的 `nav`、`card`、带 `dm_img_*` 与 `w_webid` 的 `arc/search` 第 1 页、`wbi/view`（按 `bvid` 与 `aid`）、`qrcode/generate` 通过；已登录的 `nav`、`card`、`arc/search` 第 1、2 页、`wbi/view` 通过。访客态没有请求第 2 页。
- **ACT-003（动态列表与缓存，仅已登录态）**：`nav`（`isLogin=true`）、`feed/space` 第 1 页（13 条，含 `DYNAMIC_TYPE_AV`、`DYNAMIC_TYPE_DRAW`、`DYNAMIC_TYPE_FORWARD`、`DYNAMIC_TYPE_LIVE_RCMD`）与第 2 页（12 条，按 `offset` 翻页）通过；从第 1 页取一条图文动态下载原图（1 张，`3,208,287` 字节）通过；从第 1 页取一条视频动态请求 `wbi/view` 与 `player/wbi/playurl`（`qn=80` 请求返回 `qn=112` AVC 视频流与 `30280` 192K 音频流），下载双流（视频 `96,039,012` 字节、音频 `3,831,296` 字节）并用 `mp4ff` 合并为渐进式 MP4（`99,895,351` 字节，解析时长 `136.51s` 与期望 `136.51s` 误差 `0.00s`）通过。单条动态详情 `/x/polymer/web-dynamic/v1/detail` 因前两页列表已含完整条目未在真实站点触发（`0/2`），由合成夹具验证。
- **2026-11-04 下载观察**：用登录 Cookie 下载一条约 6 MB 的视频流时，CDN 发出 524,288 字节（512 KiB）后关闭了连接（见第 15 节）。
- **ACT-004 真实站点检查（2026-11-05）**：走正式代码对真实站点执行 1 次全部通过：登录校验 `nav`、`feed/space` 第 1 页（13 条）、图文动态缓存（原图 1 张，`3,208,287` 字节，SHA-256 校验通过）、视频动态缓存（1P，`qn=127` 请求返回最高可用 `qn=116` `1080P 60帧` AVC `1080×1920@60.000` 视频流与 `30280` `192K` 音频流，返回与可用清晰度列表均为 `[116, 80, 64, 32, 16]`；视频流经 `upos-sz-mirrorhwo1.bilivideo.com` 3 个 8 MiB Range 分段拉取共 `17,394,145` 字节，音频流经 `upos-sz-mirrorcos.bilivideo.com` 1 段 `205,332` 字节，断连 0、重试 0；`mp4ff` 合并 MP4 `17,601,607` 字节，时长 `12.93s` 与站点 `13s` 误差 `0.07s`，`moov` 在前、交错存放、两轨带 `elst`）以及画质升级探测（`CheckUpgrade` 更新 `upgradeCheckedAt` 与最高可用档 `116` / `30280`，24 小时内二次非强制探测跳过）全部通过（[ACT-004](https://github.com/chialecode/trace/blob/f8af16863dbe885cfd63de048730c07a2b91e7be/USER-ACTIONS.md#act-004)）。另记一次计划外的请求：2026-11-05 第一次包冒烟时，打开的登录页按设计立即请求了二维码，向 `qrcode/generate` 发出约 1 次、`qrcode/poll` 约 5 次请求（无凭证、未扫码），没有遇到错误；之后冒烟改为把应用的网络请求指向本机关闭的端口。
- **ACT-005 真实站点检查（2026-11-07）**：第 16～20 节对真实站点执行 1 次全部通过：用 `card` 创建订阅并对目标用户执行 2 次检查更新（第 1 次读取 `feed/space` 第 1 页 12 条建立起点、未读数 0；第 2 次读取第 1 页匹配起点即停、未读数 0）；探测 2 个视频的 `wbi/view` 与 `player/wbi/v2`（均正常返回 `code=0`，所探 2 个视频本身无 CC 字幕轨），对 18 秒视频拉取第 1 段 protobuf 弹幕 `/x/v2/dm/wbi/web/seg.so` 解出 107 条弹幕并生成 `9,976` 字节标准 XML；读取登录账号的关注分组 `/x/relation/tags`（2 个分组）与关注列表第 1 页 `/x/relation/followings`（第 1 页 50 条、总计 244）；查询 `/x/passport-login/web/cookie/info`（返回 `refresh=true` 与毫秒时间戳，按规则不执行刷新）；查询目标用户开播状态 `/room/v1/Room/get_status_info_by_uids`（`live_status=2` 识别为 `round` 轮播）；对指定直播间调用 `/room/v1/Room/room_init`（返回 `live_status=1` 直播中）、`get_status_info_by_uids` 与 `/xlive/web-room/v2/index/getRoomPlayInfo`（不带 WBI 签名直接返回 `http_hls`/`fmp4` `avc` `qn=10000` 原画流），经 `d1--cn-gotcha207.bilivideo.com` 拉取 HLS 播放列表与 fMP4 分片（35 次请求、`8,899,963` 字节，0 断连），停止录制后收尾转成渐进式 MP4（`9,073,932` 字节，时长 `21.01s`，`moov` 在前）并校验通过（[ACT-005](https://github.com/chialecode/trace/blob/f8af16863dbe885cfd63de048730c07a2b91e7be/USER-ACTIONS.md#act-005)）。
- **ACT-030 真实站点检查（2026-11-25，[#97](https://github.com/chialecode/trace/issues/97)，脱敏回执 `artifacts/site-check/act030.json`）**：经生产 `Backend` 与 `bilibili.Client` 执行 1 次（总计 9/10 次只读 API 请求，0 次媒体请求，`secretScanClean=true`）7/7 全部通过：① `nav` 登录验证通过（HTTP 200，`loggedIn=true`）；② 默认 `arc/search` 返回第 1 页 30 条（`total=615`），解析 `data.list.tlist` 7 个分区且严格按 `count` 降序再 `tid` 升序排列，并写入基线总作品数；③ 带 `order=click` 与首个分区 `tid` 的 `arc/search` 返回 HTTP 200、30 条（筛选总数 `456`）且未覆盖账号基线总作品数；④ 带 2 字符 `keyword` 的 `arc/search` 返回 HTTP 200、30 条（命中总数 `568`）且未覆盖基线总作品数；⑤ 样本视频 `wbi/view` 返回 HTTP 200、1 个分 P 含有效 `cid`，成功解析 `is_upower_exclusive`/`is_upower_play`/`is_upower_preview`（该样本均为 `false`，已购/未购充电专属拦截分支仍由合成测试覆盖）；⑥ 样本视频单次 `/x/player/wbi/v2` 调用通过（HTTP 200，`subtitle_list` 计数严格为 1、0 额外请求；该样本本身无 CC 字幕与章节看点，非空 `view_points` 排序与播放跳转仍由合成测试覆盖）；⑦ 动态 `feed/space` 携带 `onlyfansAssetsV2` 特性标记正常返回 HTTP 200、12 条、`hasMore=true`、`degradedCount=0`（该页无充电专属锁定动态，`is_only_fans`/`MAJOR_TYPE_BLOCKED` 分支由合成测试覆盖）。

所以访客态投稿列表确实会被拦截（同一天先 `-412`、后通过），客户端必须保持 500ms 间隔，遇到 `-352` / `-412` 立即停止。TRACE 只做登录模式（DEC-018），访客结果只作历史记录。

## 2. 公共请求头

| 头 | 值 |
| --- | --- |
| `User-Agent` | 固定的桌面 Chrome UA，例如 `Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/144.0.0.0 Safari/537.36` |
| `Referer` | `https://www.bilibili.com/`（直播接口用 `https://live.bilibili.com/`，本阶段不涉及） |

TRACE 只在登录状态下请求（DEC-018），所有内容接口都附带凭证 Cookie（第 10 节）。访客 Cookie（`buvid3` 等，来自 `/x/frontend/finger/spi`）不再获取。

<a id="errors"></a>
## 3. 返回体与错误码

响应是 `{"code": <int>, "message": "...", "data": {...}}`。`code == 0` 才算成功；HTTP 状态不是 200 时按 HTTP 分类。

| code / HTTP | 含义 | TRACE 的分类 |
| --- | --- | --- |
| 0 | 成功 | — |
| -101 | 未登录 | `auth_required`（nav 例外，见第 4 节） |
| -352 | 风控校验失败，`data` 里可能有 `v_voucher` | `risk_control`：停止，不自动重试，提示用户稍后重试或登录 |
| -412 / HTTP 412 | 请求被拦截 | `rate_limited`：停止，不自动重试 |
| -404、62002（稿件不可见）、62004（审核中）、62012（仅 UP 主自己可见）、4101152（动态不可见） | 内容不可得 | `unavailable`，带具体原因 |
| -400 | 参数错误 | `bad_request`（多半是 TRACE 的缺陷） |
| -403 | 权限不足 | `forbidden` |
| 超时、连接失败、DNS | 网络问题 | `network`：可以手动重试 |
| 返回体不是预期结构 | 站点改版 | `schema_changed` |

## 4. 登录状态与 WBI 密钥：nav

`GET https://api.bilibili.com/x/web-interface/nav`

- 已登录：`code=0`，`data.isLogin=true`，含 `mid`、`uname`、`face`。
- 未登录：`code=-101`，`data.isLogin=false`，但 `data.wbi_img` **仍然给出 WBI 密钥**。只有这种明确的未登录信封（并且没有 `v_voucher`）可以用来取密钥；它绝不能当作登录证明。
- 密钥：`data.wbi_img.img_url` 和 `sub_url` 文件名（去掉扩展名）各是 32 位十六进制字符串，不符合就当作 `schema_changed`。

<a id="wbi"></a>
## 5. WBI 签名

1. `mixin_key`：把 `img_key + sub_key` 按下表的下标逐个取字符，取前 32 个。

   ```text
   46, 47, 18, 2, 53, 8, 23, 32, 15, 50, 10, 31, 58, 3, 45, 35, 27, 43, 5, 49, 33, 9, 42, 19, 29,
   28, 14, 39, 12, 38, 41, 13, 37, 48, 7, 16, 24, 55, 40, 61, 26, 17, 0, 1, 60, 51, 30, 4, 22, 25,
   54, 21, 56, 59, 6, 63, 57, 62, 11, 36, 20, 34, 44, 52
   ```

2. 参数值去掉 `!'()*` 这几个字符，加入 `wts`（当前 Unix 秒），按键名 ASCII 升序排列。
3. 键和值都做百分号编码，只保留 `A-Z a-z 0-9 - _ . ~`（空格编码为 `%20`），用 `&` 连接得到查询串。
4. `w_rid = md5(查询串 + mixin_key)`，小写十六进制；最终 URL 为 `base?查询串&w_rid=...`。
5. 密钥缓存 30 分钟。签名请求返回 -352 时，清空密钥、重新取一次 nav、只重试一次；再失败就按风控报告。

固定向量（实现必须通过）：

| 输入 | 期望 |
| --- | --- |
| `img_key=7cd084941338484aae1ad9425b84077c`，`sub_key=4932caff0ff746eab6f01bf08b70ac45` | `mixin_key=ea1db124af3c7062474693fa704f4ff8` |
| 上述密钥，参数 `foo=1, bar=2, baz=3`，`wts=1702204169`，base 为 `https://api.bilibili.com/x/space/wbi/arc/search` | `...?bar=2&baz=3&foo=1&wts=1702204169&w_rid=e0ed91b768b58beb346ad3116de6ab68` |
| mixin 直接取 `ea1db124af3c7062474693fa704f4ff8ab4a500c8e7ca0784bf98095b48cd341`（任意长度的测试值），参数 `foo=one, bar=two, zab=three`，`wts=1702204800` | 查询串 `bar=two&foo=one&wts=1702204800&zab=three`，`w_rid=776653b3b52c0c8d2106f418f05c2426` |
| 同上 mixin，参数 `a=on!e()*'`，`wts=1702204800` | 查询串 `a=one&wts=1702204800`，`w_rid=c95be09151d35615ef16ac92b5cb8abf` |

<a id="links"></a>
## 6. 链接识别

| 输入 | 结果 |
| --- | --- |
| `https://www.bilibili.com/video/BV1xx411c7mD`（也接受 `bilibili.com`、`m.bilibili.com`，可带 `?p=2`） | 视频，BV 号为 `BV` 加 10 位字母数字（共 12 位） |
| `https://www.bilibili.com/video/av170001` | 视频，用 `aid` 查询 |
| 直接粘贴 `BV1xx411c7mD` 或 `av170001` | 视频 |
| `https://b23.tv/xxxx`（以及 `bili2233.cn`） | 短链：不跟随地读取跳转（最多 3 跳），只接受跳到上面域名的 https 地址，再按跳转后的地址识别 |
| `https://space.bilibili.com/94697`（可带 `/video` 等后缀和查询串） | 用户，`mid=94697` |
| 纯数字 `94697`，或 `UID:94697`、`uid94697` | 用户 |
| `https://live.bilibili.com/<房间号>`（可带查询串；短号也接受） | 直播间：用 `room_init` 换成长号与主播 `uid`（第 20 节），打开主播的用户页（FR-LINK-002） |
| 其他域名、http、带用户名密码或端口的 URL、`t.bilibili.com`、`live.bilibili.com` 的其他路径、专栏、opus | 明确提示“暂不支持这种链接”，不发请求 |

识别在本地完成，只有短链需要一次网络请求。视频详情返回的 `bvid`/`aid` 必须和请求的一致，不一致按 `schema_changed` 处理。

## 7. 视频详情

`GET https://api.bilibili.com/x/web-interface/wbi/view`，WBI 签名，参数 `bvid=...` 或 `aid=...`。

`data` 里用到的字段：`bvid`、`aid`、`title`、`pic`（封面）、`desc`、`pubdate`（Unix 秒）、`duration`（秒）、`owner.{mid,name,face}`、`stat.{view,danmaku,reply,favorite,coin,share,like}`、`pages[].{cid,page,part,duration}`（多 P）、`tname`（分区名，可能为空）、`rights`、`state` 以及充电专属标记 `is_upower_exclusive`、`is_upower_play`、`is_upower_preview`（兼容布尔与 `0`/`1`）。当 `is_upower_exclusive && !is_upower_play` 时在调用 `playurl` 前提前拦截为 `forbidden`（不发无效 `playurl` 请求）；`is_upower_exclusive && is_upower_play` 表示当前账号已开通该 UP 主充电完整播放权，照常走最高画质与音质缓存。不可得的情况见第 3 节的错误码。

## 8. 用户资料

`GET https://api.bilibili.com/x/web-interface/card?mid=<mid>&photo=true`（不需要签名）。

用到 `data.card.{mid,name,face,sign,level_info.current_level,fans,attention}`、`data.follower`、`data.archive_count`、`data.space.l_img`（主页横幅大图，可能缺失）。返回的 `card.mid` 必须等于请求的 mid。

<a id="arc-search"></a>
## 9. 投稿列表

`GET https://api.bilibili.com/x/space/wbi/arc/search`，WBI 签名。参数按浏览器的顺序写（签名时会排序，顺序不影响签名）：

```text
mid=<mid> ps=30 tid=0 pn=<页码> keyword= order=pubdate platform=web web_location=1550101
order_avoided=true dm_img_list=[] dm_img_str=bm8gd2ViZ2 dm_cover_img_str=bm8gd2ViZ2
dm_img_inter={"ds":[],"wh":[0,0,0],"of":[0,0,0]} [w_webid=<访问标识>]
```

- `dm_img_str` 与 `dm_cover_img_str`：把文本 `no webgl` 做 base64 后去掉最后两个字符（`bm8gd2ViZ2`），表示没有 WebGL 的浏览器。
- `w_webid`：读取 `https://space.bilibili.com/<mid>` 页面（响应上限 2 MiB），取 `<script id="__RENDER_DATA__">` 里百分号编码的 JSON 的 `access_id`。只接受长度不超过 600、由字母数字和 `.-_=` 组成的值。每位作者 30 分钟内最多读一次页面，最多缓存 16 位作者。页面里没有这个值时不带这个参数继续请求。
- 固定向量：上述 IMG/SUB 密钥，`mid=94697, pn=2, ps=30, w_webid=eyJ.web-id_1`，`wts=1702204169` 时 `w_rid=d1973c4ae71235d7bc494f04744540c9`。
- 参数支持：`tid`（`0` 表示全部分区，非零按分区筛选，纳入 WBI 签名）、`order`（`pubdate` 最新发布 / `click` 最多播放 / `stow` 最多收藏）、`keyword`（关键词搜索，仅在用户显式提交时发请求）。
- 结果：`data.list.vlist[].{bvid,aid,title,pic,created,length("mm:ss"),play,comment,description}`、`data.list.tlist`（各分区 `{tid,name,count}` 映射；为空时站点可能返回 `null` 或 `[]`，TRACE 安全夹取整数并按 `count` 降序再按 `tid` 升序稳定排序）、`data.page.{pn,ps,count}`。`count` 只是站点给的观察值，不保证完整。置顶视频可能出现在第一页，与时间顺序不一致；不同页之间可能重复，前端按 `bvid` 去重。
- 风险：这是最容易触发 -352 的接口。翻页由用户滚动触发，不预取；遇到 -352 就停在当前页，显示原因和“重试”按钮。

<a id="qr-login"></a>
## 10. 扫码登录

1. `GET https://passport.bilibili.com/x/passport-login/web/qrcode/generate` → `data.url`（二维码内容）、`data.qrcode_key`。
2. 每 2 秒 `GET .../qrcode/poll?qrcode_key=<key>`，看 `data.code`：

   | data.code | 状态 |
   | --- | --- |
   | 86101 | 未扫码 |
   | 86090 | 已扫码，等待在手机上确认 |
   | 86038 | 二维码已过期 |
   | 0 | 登录成功 |

3. 成功时从响应的 `Set-Cookie` 取 `SESSDATA`、`bili_jct`、`DedeUserID`、`DedeUserID__ckMd5`、`sid`；没有 `Set-Cookie` 时，再从 `data.url` 的查询参数里取同名字段。另存 `data.refresh_token`（以后刷新 Cookie 用）。缺 `SESSDATA` 就当作失败，不保存半份凭证。
4. 一个二维码 180 秒后作废；用户可以随时取消。取消或过期后晚到的轮询结果一律丢弃，不能写入凭证，也不能改动新二维码的状态。
5. 登录成功后用 nav 确认 `isLogin=true` 且 `mid` 等于 `DedeUserID`，才算登录完成。

Cookie 刷新（用 `refresh_token` 加 RSA 计算的 correspondPath）见第 17 节。

<a id="images"></a>
## 11. 图片

封面、头像在 `*.hdslb.com`，可能给出 `http://` 地址，统一改成 `https://`。图片 CDN 会拒绝外站 Referer：`<img>` 一律带 `referrerpolicy="no-referrer"`。缩略图可以在 URL 后追加 `@<宽>w_<高>h_1c.webp` 之类的尺寸后缀，减少流量；显示大图时去掉后缀。

<a id="pacing"></a>
## 12. 请求节奏

所有 API 请求之间至少间隔 500 毫秒（全局）；单次请求超时 15 秒；JSON 响应上限 4 MiB。翻页只由用户操作触发。遇到风控或拦截不重试，API 请求的网络错误也只由用户手动重试。媒体文件传输中的网络错误（提前断开、超时、连接重置、5xx）按[媒体与存储选型 §5](2026-11-05-media.md#download)在任务内有限次自动续传。

<a id="dynamics"></a>
## 13. 动态列表（`feed/space` 已真实检查，`detail` 经夹具验证）

动态是哔哩哔哩统一的发布单位：投稿视频、图文、纯文字、转发、专栏都会作为一条动态出现在用户的“动态”页；图文只以动态的形式存在。视频动态引用一个视频（`bvid`），这个视频同时出现在投稿列表里。

`GET https://api.bilibili.com/x/polymer/web-dynamic/v1/feed/space`，`Referer: https://space.bilibili.com/<mid>/`。参数：

```text
offset=<上一页返回的 offset，首页为空> host_mid=<mid> platform=web
features=itemOpusStyle,listOnlyfans,opusBigCover,onlyfansVote,onlyfansAssetsV2
dm_img_list=[] dm_img_str=bm8gd2ViZ2 dm_cover_img_str=bm8gd2ViZ2 dm_img_inter={"ds":[],"wh":[0,0,0],"of":[0,0,0]}
```

- **ACT-003 实测结论**：登录态下携带上述参数（不带 WBI 签名）直接通过第 1 页（13 条）和第 2 页（12 条，使用第 1 页返回的 `offset`）；返回 `code=-352` 时客户端支持自动退回 WBI 签名重试一次。
- 访客 Cookie 请求会返回 -352（社区议题 2026-10 报告，未修复），这也是 DEC-018 只做登录模式的依据之一。
- 结果：`data.items[]`、`data.offset`（字符串，下一页用）、`data.has_more`。每条：`id_str`（大整数，按字符串处理）、`type`（`DYNAMIC_TYPE_AV` 视频、`DYNAMIC_TYPE_DRAW` 图文、`DYNAMIC_TYPE_WORD` 文字、`DYNAMIC_TYPE_FORWARD` 转发、`DYNAMIC_TYPE_ARTICLE` 专栏，以及直播、番剧等）、`modules.module_author.{mid,name,face,pub_ts}`、`modules.module_dynamic.desc.text`、`modules.module_dynamic.major`；转发的原动态在 `orig`，结构相同。
- `major.type`：`MAJOR_TYPE_ARCHIVE`（`archive.{bvid,aid,title,cover,duration_text}`）、`MAJOR_TYPE_OPUS`（`opus.{summary.text,pics[].{url,width,height}}`）、`MAJOR_TYPE_DRAW`（`draw.items[].{src,width,height}`）、`MAJOR_TYPE_BLOCKED`（充电专属锁定内容，配合 `basic.is_only_fans` 与 `major.blocked.{hint_message,sub_title}` 保留外层时间/标题并清晰展示锁定原因，不标为 `Degraded` 也不入队缓存）、`MAJOR_TYPE_ARTICLE`、`MAJOR_TYPE_UGC_SEASON`、`MAJOR_TYPE_PGC`、`MAJOR_TYPE_LIVE`、`MAJOR_TYPE_LIVE_RCMD`、`MAJOR_TYPE_COMMON`、`MAJOR_TYPE_COURSES`、`MAJOR_TYPE_MUSIC` 等。没见过的类型显示类型名，不当作错误，也不丢弃。
- 置顶动态可能出现在第一页顶部，与时间顺序不一致；页之间可能重复，按 `id_str` 去重。
- 风险：和投稿列表一样容易触发 -352；翻页只由用户滚动触发，遇到风控停在当前页。
- 单条动态详情（缓存时列表缓存已过期的回退）：`GET /x/polymer/web-dynamic/v1/detail?id=<id_str>`，结构同上的单条 `data.item`，已在合成夹具中验证（ACT-003 因列表缓存直接命中未发起真实请求）。

<a id="playurl"></a>
## 14. 播放地址（已真实检查）

`GET https://api.bilibili.com/x/player/wbi/playurl`，WBI 签名，带登录 Cookie。参数：`bvid`（或 `avid`）、`cid`（来自视频详情的 `pages[].cid`）、`fnval=4048`（全部 DASH 能力）、`fnver=0`、`fourk=1`、`qn=<目标档>`。

- 已知行为与 ACT-003 / ACT-004 实测：不登录最高 480P；登录普通账号 `1080P`（`qn=80`），大会员返回 `112`（1080P+）、`116`（1080P60）、`120`（4K）、`125`（HDR）等。ACT-003 实测请求 `qn=80` 时 `data.dash.video` 包含了 `id=112`（`codecid=7` AVC）等流；ACT-004 实测按默认最高档请求 `qn=127` 时返回 `accept_quality=[116,80,64,32,16]`，TRACE 选出排序表最高的 `id=116`（`1080P 60帧` AVC）视频流与 `id=30280`（`192K`）音频流。所以 `qn` 只是提示，**实际能拿到哪些档以返回的 `dash.video[].id` 为准**。
- 结果：`data.accept_quality[]`、`data.support_formats[]`（每档的 `quality`、`new_description`、`codecs[]`，以及部分档上的 `need_login`、`need_vip`）、`data.timelength`（毫秒）、`data.dash.video[]` 与 `data.dash.audio[]`，每项有 `id`（清晰度或音质代码）、`baseUrl`/`base_url`、`backupUrl`/`backup_url`、`bandwidth`、`codecid`（7 = AVC，12 = HEVC，13 = AV1）、`codecs`、`width`、`height`、`frameRate`、`segment_base`；`dash.audio` 可能为 `null`（无声视频）。
- 最高音质：`data.dash.dolby.audio[]`（杜比全景声，E-AC-3，代码 30250）和 `data.dash.flac.audio`（Hi-Res 无损，FLAC，代码 30251），账号与稿件都支持时才有，结构同普通音频；可能为 `null`。普通音质代码：30216 = 64K，30232 = 132K，30280 = 192K。社区资料未查证 `dolby`/`flac` 的具体返回条件，按实际返回为准。
- 清晰度代码（社区资料）：`127` 8K、`126` 杜比视界、`125` HDR、`120` 4K、`116` 1080P60、`112` 1080P+、`80` 1080P、`74` 720P60、`64` 720P、`32` 480P、`16` 360P。代码大小不完全等于画质高低（`126` 与 `125`、`127` 的编码和设备要求不同），TRACE 用固定的排序表比较“更高”，不直接比数字。
- “能拿到的最高档”（DEC-024）= 这次返回的 `dash.video[]` 里排序表上最高的一档；`accept_quality`、`support_formats` 里更高、但没有返回流的档（例如要大会员）只记录，不算“能拿到”。
- 地址约 120 分钟失效（带 `deadline` 参数），**是签名 URL，不写日志、不落盘**。
- 付费、会员内容只返回试看时：通过 `data.durl` / 试看标记或 `timelength` 明显短于稿件分 P 时长（如时长 > 60s 而返回流 < 50% 且 <= 390s）判断，记为部分缓存并说明原因。

<a id="media-cdn"></a>
## 15. 媒体文件下载（已真实检查）

- 视频与音频流：`platform=pc` 的地址校验 `Referer`，请求带 `Referer: https://www.bilibili.com/` 和公共 UA；支持 `Range`。主地址和备用地址可能指向不同类型的节点：`upos-*.bilivideo.com`（官方节点）、`*.mcdn.bilivideo.cn`（PCDN，常带非标准端口）以及其他区域镜像域名；额度统计与下载器按非 API 域名识别媒体流。
- **提前断开与分段拉取**（2026-11-04 / 2026-11-05 实测）：2026-11-04 一次不带 `Range` 的整段请求在 512 KiB 处被服务器关闭，连接正常结束但长度不足。社区下载工具普遍用分段 `Range` 请求、段内重试和节点轮换应对；TRACE 的做法见[媒体与存储选型 §5](2026-11-05-media.md#download)。2026-11-05 ACT-004 实测走 TRACE 的 8 MiB 闭区间 `Range` 分段拉取（`upos-sz-mirrorhwo1.bilivideo.com` 3 段 HTTP 206 共 `17,394,145` 字节、`upos-sz-mirrorcos.bilivideo.com` 1 段 HTTP 206 `205,332` 字节、`i0.hdslb.com` 2 段 HTTP 206 共 `4,017,952` 字节），全程 0 次断连、0 次重试。
- 图片：`*.hdslb.com`，去掉 `@...` 尺寸后缀得到原图，同样改成 `https://`（ACT-003 实测单张原图 `3,208,287` 字节下载与 SHA-256 校验通过）。
- 请求节奏：媒体文件不走 API 的 500 毫秒间隔，由下载并发上限控制（默认 2）；传输错误按媒体选型 §5 分段续传、轮换节点；遇到 403/404/410 换下一个地址，都失败时每个分 P 最多重取一次 `playurl`；遇到 412 立即停止并暂停任务。
- 诊断：日志和任务详情只记节点域名、偏移、尝试次数和错误类别，不记地址的查询串（签名）。

来源（查询 2026-11-04、2026-11-05）：社区整理的[视频流 URL 文档](https://sessionhu.github.io/bilibili-API-collect/docs/video/videostream_url.html)（清晰度代码、`fnval` 位、杜比与无损音频字段）；动态列表的参数与字段来自本机参考索引中一个多站点订阅项目的 B 站路由（只读事实，未复制代码）。

证据等级（第 16～20 节）：**核** = 社区文档与本机参考索引中的实现一致；**社** = 只有社区文档；**参** = 只有参考实现；**未** = 没有核实或说法不一。已在夹具测试与 [ACT-005](https://github.com/chialecode/trace/blob/f8af16863dbe885cfd63de048730c07a2b91e7be/USER-ACTIONS.md#act-005) 真实站点有界检查中验证。

<a id="followings"></a>
## 16. 关注列表与分组（已真实检查）

- `GET https://api.bilibili.com/x/relation/followings?vmid=<登录账号 mid>&pn=<页>&ps=50&order=desc&order_type=`，带登录 Cookie，`Referer: https://space.bilibili.com/<mid>/`。`ps` 最大 50（社）；不需要 WBI（核；ACT-005 实测不签名直接返回第 1 页 50 条、`total=244`）。
- 看别人的关注列表只能看前 5 页（code 22007），对方开了隐私返回 22115（核）；看自己的列表可以全部翻完（社）。TRACE 只读登录账号自己的列表（FR-SUBS-003）。
- 结果：`data.list[].{mid, uname, face, sign, mtime, tag(分组 ID 数组), special, official_verify, vip}`、`data.total`（社；ACT-005 实测一致）。`mid` 按字符串处理。
- 分组：`GET /x/relation/tags`（登录）→ `data[].{tagid, name, count, tip}`，`0` 是默认分组、`-10` 是特别关注（社；ACT-005 实测通过）；组内成员 `GET /x/relation/tag?tagid=&pn=&ps=`（登录），`ps` 上限未知（未），TRACE 用 50，返回少于 `ps` 视为最后一页。
- 错误：-101 按第 4 节；-6 在参考实现里表示 Cookie 过期（参），按 `auth_required` 处理；22007、22115 按 `forbidden`。

<a id="cookie-refresh"></a>
## 17. 登录续期（`cookie/info` 已真实检查，刷新流程经夹具验证）

1. **是否需要刷新**：`GET https://passport.bilibili.com/x/passport-login/web/cookie/info`，带 Cookie（可加 `csrf=<bili_jct>`）→ `data.refresh`（布尔）、`data.timestamp`（毫秒）；未登录 -101。不需要 WBI（社；ACT-005 实测返回 `code=0`、`refresh=true` 与 13 位毫秒时间戳）。
2. **correspondPath**：明文 `refresh_<timestamp>`，用下面的公钥做 RSA-OAEP 加密，结果写成小写十六进制。社区示例用 SHA-256 做 OAEP 哈希（社）；另有一处写 SHA-1（未）。OAEP 每次结果不同，没有固定向量，B 已在 `TestCorrespondPathRoundTripAndKeySHA256` 用测试密钥对往返解密测试并用常量的 SHA-256 核对公钥一致；真实站点是否接受由 H-001 的扫码会话确认（第 3 步取不到 `refresh_csrf` 即说明不接受）。

   ```text
   -----BEGIN PUBLIC KEY-----
   MIGfMA0GCSqGSIb3DQEBAQUAA4GNADCBiQKBgQDLgd2OAkcGVtoE3ThUREbio0Eg
   Uc/prcajMKXvkCKFCWhJYJcLkcM2DKKcSeFpD/j6Boy538YXnR6VhcuUJOhH2x71
   nzPjfdTcqMz7djHum0qSZA0AyCBDABUqCrfNgCiJ00Ra7GmRj+YCK1NJEuewlb40
   JNrRuoEUXpabUzGB8QIDAQAB
   -----END PUBLIC KEY-----
   ```

3. **refresh_csrf**：`GET https://www.bilibili.com/correspond/1/<correspondPath>`，带 Cookie，从 HTML 里 `<div id="1-name">` 的文本取值（社，两个来源一致）；响应上限 2 MiB，取不到按 `schema_changed`。
4. **刷新**：`POST https://passport.bilibili.com/x/passport-login/web/cookie/refresh`，表单 `csrf=<旧 bili_jct>`、`refresh_csrf`、`source=main_web`、`refresh_token=<旧值>`。成功时新的 `SESSDATA`、`bili_jct` 等经 `Set-Cookie` 下发，新令牌在 `data.refresh_token`。错误：-101 未登录、-111 csrf 校验失败、86095 `refresh_csrf` 错误或令牌与 Cookie 不匹配（社）。
5. **确认**：`POST https://passport.bilibili.com/x/passport-login/web/confirm/refresh`，带**新** Cookie，表单 `csrf=<新 bili_jct>`、`refresh_token=<旧值>`，让旧令牌失效（社）。

- 顺序：1 → 2 → 3 → 4 → 用 nav 确认新 Cookie 有效（`isLogin=true` 且 `mid` 不变）→ 原子保存新 Cookie 与新令牌 → 5。第 5 步失败时新 Cookie 是否仍可用（未）：TRACE 保留新凭证，记下“待确认”，下次续期检查时先补做第 5 步。
- `refresh_token` 在浏览器里存在 localStorage 的 `ac_time_value`（社，多处一致），不在 Cookie 里；扫码登录从 `qrcode/poll` 的 `data.refresh_token` 取得（第 10 节）。刷新会让旧 Cookie 失效：粘贴的 Cookie 与浏览器共用会话，刷新后浏览器那边会掉线，所以 TRACE 只为扫码登录续期（DEC-031）。

<a id="danmaku"></a>
## 18. 弹幕（已真实检查）

- **分段接口**（TRACE 用这个）：`GET https://api.bilibili.com/x/v2/dm/wbi/web/seg.so?type=1&oid=<cid>&pid=<aid>&segment_index=<从 1 开始>&web_location=1315873`，WBI 签名（另有不签名的 `/x/v2/dm/web/seg.so`，社）；带登录 Cookie，`Referer` 为视频页。每段 6 分钟，段数 `ceil(时长 / 360 秒)`。返回体是 protobuf，不是 JSON；返回 JSON 信封时按第 3 节分类（例如 -352）。ACT-005 实测签名版对真实视频拉取第 1 段二进制 protobuf 并解出 107 条弹幕通过，夹具覆盖返回 JSON 错误信封的情况。
- **protobuf**：`DmSegMobileReply.elems = 1`（repeated `DanmakuElem`，社）。`DanmakuElem`（社，两个来源一致）：

  | 字段 | 编号 | 类型 | 说明 |
  | --- | --- | --- | --- |
  | `id` | 1 | int64 | |
  | `progress` | 2 | int32 | 出现时间，毫秒 |
  | `mode` | 3 | int32 | 1～3 滚动、4 底部、5 顶部、6 逆向、7 高级、8 代码、9 BAS |
  | `fontsize` | 4 | int32 | 18、25、36 |
  | `color` | 5 | uint32 | RGB 十进制 |
  | `midHash` | 6 | string | 发送者的哈希，不是 mid |
  | `content` | 7 | string | |
  | `ctime` | 8 | int64 | 发送时间（Unix 秒） |
  | `weight` | 9 | int32 | |
  | `action` | 10 | string | |
  | `pool` | 11 | int32 | 0 普通、1 字幕、2 特殊 |
  | `idStr` | 12 | string | |
  | `attr` | 13 | int32 | 位标记 |

  未知字段跳过，不当作错误。
- **旧 XML 接口**：`x/v1/dm/list.so?oid=<cid>` 或 `comment.bilibili.com/<cid>.xml`，响应是原始 deflate 压缩，只给实时弹幕池，条数受 `<maxlimit>` 限制（社）。TRACE 不用它取数据，只沿用它的文件格式。
- **保存格式**：通用 XML，`<i><chatserver>chat.bilibili.com</chatserver><chatid><cid></chatid>…<d p="出现时间(秒，小数),mode,fontsize,color,发送时间(Unix 秒),pool,midHash,dmid,weight">内容</d>…</i>`（社，多个来源一致）；`progress` 除以 1000，`dmid` 优先用 `idStr`；按出现时间排序；内容做 XML 转义，去掉 XML 不允许的控制字符。`mode` 7～9（高级、代码、BAS）原样保存，播放器不渲染。

<a id="subtitles"></a>
## 19. CC 字幕（列表接口已真实检查，字幕文件经夹具验证）

- `GET https://api.bilibili.com/x/player/wbi/v2?bvid=<bvid>&cid=<cid>`，WBI 签名，带登录 Cookie，`Referer` 为视频页。参考实现不签名也能用（参）；TRACE 用签名版（ACT-005 实测探测 2 个视频均返回 `code=0` 且 `bvid`/`cid` 匹配，所探视频无 CC 字幕轨）。
- `data.subtitle.subtitles[]`：`id`、`lan`、`lan_doc`、`subtitle_url`（以 `//` 开头，补 `https:`；带 `auth_key`，有时效，**不落盘、不进日志**）、`type`、`ai_type`、`ai_status`。`lan` 以 `ai-` 开头的是 AI 字幕（社）。
- `data.view_points[]`（零额外请求顺手解析）：每项含 `type`、`from`（起点秒）、`to`（终点秒）、`content`（章节标题）；TRACE 过滤无效条目（标题为空、`from < 0` 或 `to <= from`）、去重并按 `from` 升序排列后随分 P 写入 `item.json` 的可选字段 `parts[].chapters`。
- 未登录时 `subtitles` 为空（核）；社区报告过旧版 `x/player/v2` 返回别的视频的字幕、AI 字幕的 `subtitle_url` 为空（社）：TRACE 校验返回的 `bvid`/`cid` 与请求一致（不一致按 `schema_changed`），跳过空地址。
- 字幕文件（`*.hdslb.com`）：`{font_size, font_color, background_alpha, background_color, Stroke, type, lang, version, body: [{from, to, sid, location, content, music}]}`，`from`/`to` 单位为秒（核）。TRACE 转成 WebVTT 与 SRT（时间格式分别为 `HH:MM:SS.mmm` 与 `HH:MM:SS,mmm`，内容做相应转义），原始 JSON 不另存。

<a id="live"></a>
## 20. 直播（已真实检查）

直播接口在 `api.live.bilibili.com`，请求头 `Referer: https://live.bilibili.com/`，同样走 500 毫秒全局间隔与登录检查（INV-07）。

- **按 uid 批量查开播**：`GET /room/v1/Room/get_status_info_by_uids?uids[]=<uid>&uids[]=<uid>…`（也可以 POST JSON `{"uids":[…]}`）→ `data` 以 uid 字符串为键：`title, uname, face, live_status(0 未开播 / 1 直播中 / 2 轮播), live_time(Unix 秒), online, room_id, short_id, cover_from_user, keyframe`（核；ACT-005 实测同时验证了 `live_status=2` 轮播与 `live_status=1` 直播中）。一次能查多少个未知（未）：TRACE 每次最多 50 个；账号不在 `data` 里表示没有直播间。
- **房间号**：短号或长号 → `GET /room/v1/Room/room_init?id=<号>` → `room_id(长号), short_id, uid, live_status, encrypted, is_sp`（核；ACT-005 实测通过）；按 uid 取房间 `GET /room/v1/Room/getRoomInfoOld?mid=<uid>`（社）。房间不存在 19002003（社，单一来源）按 `unavailable`。
- **取流**：`GET /xlive/web-room/v2/index/getRoomPlayInfo?room_id=<长号>&protocol=0,1&format=0,1,2&codec=0,1&qn=10000&platform=web&ptype=8&dolby=5&panorama=1`（核：三个录制项目一致）。是否要 WBI 各家不同（参）：先不签名，遇 -352 时签名重试一次（同第 13 节动态列表的做法；ACT-005 实测不带 WBI 签名直接返回 `code=0` 与 `http_hls`/`fmp4` `avc` `qn=10000` 原画流）。
  - 结构：`data.playurl_info.playurl.stream[].protocol_name`（`http_stream` / `http_hls`）→ `format[].format_name`（`flv` / `ts` / `fmp4`）→ `codec[].{codec_name(avc/hevc), current_qn, accept_qn[], base_url, url_info[].{host, extra, stream_ttl}}`；`g_qn_desc[]{qn, desc}`。完整地址 = `host + base_url + extra`（核）。
  - `current_qn` 可能低于请求的档；`all_special_types` 含 203 表示加密流（参），TRACE 不录加密流，说明原因。
  - 地址带 `expires`，有效期未知（未）：每次重连都重新取流，不复用旧地址。地址是签名 URL，**不落盘、不进日志**。
- **下载**：`Referer: https://live.bilibili.com/`、`Origin: https://live.bilibili.com`、公共 UA，拉流不带 Cookie（核）。去掉 `*.mcdn.*` 节点优先官方节点；自己跟随 301/302（只跟随到 `*.bilivideo.com`、`*.bilivideo.cn`、`*.hdslb.com` 等站点域名的 https/http 地址）；连接超时 5 秒，10 秒收不到数据就断开重连（参）。HTTP 412 按风控停止。
- **FLV 已知问题**（参考实现的处理方式，只描述做法）：相邻时间戳跳变超过 500 毫秒时重算偏移保持连续；每个文件从 0 开始；重连后丢弃重复的数据块；收到新的音视频头（编码参数变化）就换新文件；音频头迟到、填充数据要处理。
- **HLS fMP4**：先取 `EXT-X-MAP` 的初始化分片，再按播放列表顺序取 `.m4s` 分片；初始化分片变化时换新文件（ACT-005 实测 `d1--cn-gotcha207.bilivideo.com` 35 次播放列表与分片请求共 `8,899,963` 字节，收尾转渐进式 MP4 `9,073,932` 字节、时长 `21.01s`、`moov` 在前校验通过）。ACT-007（2026-11-08）记录的媒体列表：`#EXT-X-VERSION`、`#EXT-X-START`、`#EXT-X-MEDIA-SEQUENCE`、`#EXT-X-TARGETDURATION`、一个在首个分片前的 `#EXT-X-MAP`，6～8 个相对地址的 `.m4s` 分片，每个分片带 `#EXTINF` 和站点自定义的 `#EXT-BILI-AUX`（按未知标签忽略）；`Content-Type: application/vnd.apple.mpegurl`。同一地址也可能返回多码率索引（`#EXT-X-STREAM-INF`），录制时跟随码率最高的一路（推断，见 F-032）。

来源（查询 2026-11-07）：社区整理的 [bilibili-API-collect](https://github.com/SocialSisterYi/bilibili-API-collect)（`docs/login/cookie_refresh.md`、`docs/user/relation.md`、`docs/danmaku/danmaku_proto.md`、`docs/danmaku/danmaku_xml.md`、`docs/video/player.md`、`docs/live/info.md`、`docs/live/live_stream.md`）；本机参考索引中的多站点订阅项目与三个直播录制项目（只读事实，未复制代码）。参考实现与本页第 13 节的一处差异：它的动态列表请求里 `dm_img_list` 是随机生成的一个点、不带 `dm_img_inter`；TRACE 现在的写法已经通过 ACT-003 与 ACT-004，不改，只记录。

<a id="account-lists"></a>
## 21. 登录账号的收藏夹、稍后再看与历史记录（ACT-020 已真实检查）

用于“我的”（FR-ACCT-004，DEC-047）。查询 2026-11-14。来源：社区整理的接口文档仓库 bilibili-API-collect 已于 2026-01 归档并清空（GitHub REST 读到默认分支只剩 `README.md`），本节改按一个活跃的第三方哔哩哔哩客户端（GPL-3.0，2026-11-13 仍有提交）的请求与数据模型核对，**只取协议事实，不复制代码**；另参照本机参考索引中多站点订阅项目的收藏夹与稍后再看路由。标记沿用本页：（社）社区资料、（参）参考实现、（未）未知；真实检查后改为（核）。全部是登录账号自己的只读 GET，带登录 Cookie，走第 12 节的 500 毫秒全局间隔，错误码按第 3 节。

**收藏夹列表**

- `GET https://api.bilibili.com/x/v3/fav/folder/created/list-all?up_mid=<登录账号 mid>`，不分页，不要 WBI（参）。可选 `type=2`（只算视频）不用。
- 结果 `data.count`、`data.list[].{id, fid, mid, attr, title, fav_state, media_count, cover?}`（参）。`id` 是收藏夹的 `media_id`（下一步用它），`fid` 是原始编号；`id` 一律按字符串或 int64 处理。`attr` 的第 0 位为 1 表示私密、第 1 位为 0 表示默认收藏夹（社，单一来源，真实检查核对）；默认收藏夹排第一。
- `data` 为 `null` 表示没有收藏夹（参），按空处理；-101 按未登录。

**收藏夹内容**

- `GET https://api.bilibili.com/x/v3/fav/resource/list?media_id=<id>&pn=<页，从 1>&ps=20&keyword=&order=mtime&type=0&tid=0&platform=web`，不要 WBI（参）。`order=mtime` 按收藏时间从新到旧；`ps` 上限 20（社，另有资料写 40，未核）。
- 结果 `data.info.{id, title, cover, media_count, upper.{mid, name, face}}`、`data.medias[]`、`data.has_more`（参）。`medias[]` 字段：`id`（稿件 avid 或其他类型的编号）、`type`（`2` 视频稿件、`12` 音频、`24` 番剧/影视，参）、`title`、`cover`、`intro`、`page`（分 P 数）、`duration`（秒）、`upper.{mid, name, face}`、`attr`、`bvid`（个别为 `bv_id`，两者都认）、`fav_time`（Unix 秒）、`pubtime`、`ugc.first_cid`、`ogv.{type_name, season_id}`（参）。
- **失效条目**：`attr` 为 `1`（其他原因删除）或 `9`（UP 主自己删除）时稿件已失效，标题常被站点替换为“已失效视频”（参）；不可打开、不可缓存。
- 翻页：`has_more=false` 为到底；`medias` 为 `null` 且 `has_more=false` 是空收藏夹。`has_more=true` 而 `medias` 为空按 `schema_changed`，不当成到底。
- 别人的私密收藏夹会返回错误（社）；TRACE 只读自己的。

**稍后再看**

- `GET https://api.bilibili.com/x/v2/history/toview/web?pn=<页>&ps=20&viewed=<0|2>&key=&asc=false&need_split=true&web_location=333.881`，**需要 WBI 签名**（第 5 节，参）。`viewed=0` 全部、`viewed=2` 未看完（参；`1` 未看、`3` 已看完的含义未核，TRACE 不用）。
- 结果 `data.count`（总数）、`data.list[].{aid, bvid, cid, title, pic, duration, pubdate, owner.{mid, name, face}, progress, videos, is_pgc, is_pugv, bangumi, redirect_url, add_at?}`（参）。`progress` 是秒，`-1` 表示看完（社）；`is_pgc`/`is_pugv` 为真的是番剧或课程，TRACE 不打开。以 `count` 与已取条数判断到底。
- 旧接口 `GET /x/v2/history/toview`（不分页，最多约 100 条，社）只作后备，不在本阶段使用。

**历史记录**

- `GET https://api.bilibili.com/x/web-interface/history/cursor?type=<all|archive|live|article>&ps=20&max=<上一页 cursor.max>&view_at=<上一页 cursor.view_at>&business=<上一页 cursor.business>`，第一页 `max=0&view_at=0`、不带 `business`；不要 WBI（参）。`ps` 上限 30（社）。TRACE 用 `type=all`。
- 结果 `data.cursor.{max, view_at, business, ps}`、`data.tab[].{type, name}`、`data.list[]`（参）。`list[]`：`title`、`cover`（有时为空，另有 `covers[]`）、`history.{oid, epid, bvid, page, cid, part, business, dt}`、`videos`、`author_name`、`author_mid`、`view_at`（Unix 秒）、`progress`（秒，`-1` 看完）、`duration`、`kid`、`badge`、`show_title`、`live_status`、`uri`（参）。
- `history.business`：`archive` 视频稿件（`bvid` 可打开）；`live` 直播（`oid` 是房间号、`author_mid` 是主播，`live_status=1` 在播）；`pgc` 番剧影视；`article`、`article-list` 专栏；`cheese` 课程（参）。TRACE 只打开 `archive`（视频详情）与 `live`（主播用户页），其余只显示类型并可在浏览器打开（地址用 `uri`，必须是 `https://` 的 `bilibili.com` 或 `b23.tv` 域名，否则不给浏览器入口）。
- 到底：`list` 为空或 `cursor.max=0`（社）。翻页游标不变且仍有条目时按 `schema_changed` 停下，不死循环。
- 用户在站点上暂停了历史记录时列表照常返回已有内容（社）；TRACE 不读也不改暂停开关。

**关注**：沿用第 16 节（分组、关注列表都已真实检查）。

**写操作**（不在 TRACE 范围，只记边界）：取消收藏 `fav/resource/batch-deal`、删除历史 `history/delete`、移出稍后再看 `toview/v2/dels` 等都是带 `csrf` 的 POST，TRACE 不调用（DEC-047）。

**真实检查结果**（[ACT-020](https://github.com/chialecode/trace/blob/f8af16863dbe885cfd63de048730c07a2b91e7be/USER-ACTIONS.md#act-020)，2026-11-14，1 次，经生产 `AccountService`，脱敏回执 `artifacts/site-check/act020.json`；以下各条改为（核））：

- B-V0 关注（第 16 节接口经“我的”复用）：3 个分组，第 1 页 20 人、`total` 247、字段齐全。
- B-V1 `list-all`：4 个收藏夹，恰好 1 个默认收藏夹（`attr` 第 1 位为 0）且排第一；本账号没有私密收藏夹，私密位只由合成测试覆盖。
- B-V2 `resource/list`：`ps=20` 被接受；第 1、2 页各 20 条、页间 0 重复、`has_more` 正确；第 1 页有 1 条失效条目，按 `attr` 判为不可打开。
- B-V3 `toview/web` 带 WBI：一次通过（没有 -352）；`viewed=0` 的 `count` 92、`viewed=2` 的 `count` 91，列表各 20 条。
- B-V4 `history/cursor`：第 1、2 页各 20 条，游标三元组推进、页间 0 重复；本次两页全是 `archive`，`live`/`pgc`/`article` 的映射只由合成测试覆盖。
