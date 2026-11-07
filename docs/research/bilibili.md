# 哔哩哔哩协议资料

核对：2026-11-07（第 16～20 节关注列表、登录续期、弹幕、字幕、直播已完成 [ACT-005](../../USER-ACTIONS.md#act-005) 真实站点有界检查与夹具验证）。内容是社区观察到的网页协议，加上 TRACE 自己计算的固定向量。端点不是站点对 TRACE 的稳定承诺；站点改版后要重新核对。规则见[站点规则](../dev-rules/sources-and-safety.md#site-rules)。

## 1. 验证状态

本页协议在真实站点上做过 [ACT-002](../../USER-ACTIONS.md#act-002)、[ACT-003](../../USER-ACTIONS.md#act-003)（2026-11-04）、[ACT-004](../../USER-ACTIONS.md#act-004)（2026-11-05）与 [ACT-005](../../USER-ACTIONS.md#act-005)（2026-11-07）有界检查：

- **ACT-002 第一轮**：访客 `nav`、`card` 通过，投稿列表 `arc/search` 第 1 页返回 `-412` / HTTP 412，按停止条件停止；已登录因测试 Cookie 失效（`nav` 返回 `-101`）停止。
- **ACT-002 第二轮**（更新 Cookie 后）：访客的 `nav`、`card`、带 `dm_img_*` 与 `w_webid` 的 `arc/search` 第 1 页、`wbi/view`（按 `bvid` 与 `aid`）、`qrcode/generate` 通过；已登录的 `nav`、`card`、`arc/search` 第 1、2 页、`wbi/view` 通过。访客态没有请求第 2 页。
- **ACT-003（动态列表与缓存，仅已登录态）**：`nav`（`isLogin=true`）、`feed/space` 第 1 页（13 条，含 `DYNAMIC_TYPE_AV`、`DYNAMIC_TYPE_DRAW`、`DYNAMIC_TYPE_FORWARD`、`DYNAMIC_TYPE_LIVE_RCMD`）与第 2 页（12 条，按 `offset` 翻页）通过；从第 1 页取一条图文动态下载原图（1 张，`3,208,287` 字节）通过；从第 1 页取一条视频动态请求 `wbi/view` 与 `player/wbi/playurl`（`qn=80` 请求返回 `qn=112` AVC 视频流与 `30280` 192K 音频流），下载双流（视频 `96,039,012` 字节、音频 `3,831,296` 字节）并用 `mp4ff` 合并为渐进式 MP4（`99,895,351` 字节，解析时长 `136.51s` 与期望 `136.51s` 误差 `0.00s`）通过。单条动态详情 `/x/polymer/web-dynamic/v1/detail` 因前两页列表已含完整条目未在真实站点触发（`0/2`），由合成夹具验证。
- **2026-11-04 下载观察**：用登录 Cookie 下载一条约 6 MB 的视频流时，CDN 发出 524,288 字节（512 KiB）后关闭了连接（见第 15 节）。
- **ACT-004 真实站点检查（2026-11-05）**：走正式代码对真实站点执行 1 次全部通过：登录校验 `nav`、`feed/space` 第 1 页（13 条）、图文动态缓存（原图 1 张，`3,208,287` 字节，SHA-256 校验通过）、视频动态缓存（1P，`qn=127` 请求返回最高可用 `qn=116` `1080P 60帧` AVC `1080×1920@60.000` 视频流与 `30280` `192K` 音频流，返回与可用清晰度列表均为 `[116, 80, 64, 32, 16]`；视频流经 `upos-sz-mirrorhwo1.bilivideo.com` 3 个 8 MiB Range 分段拉取共 `17,394,145` 字节，音频流经 `upos-sz-mirrorcos.bilivideo.com` 1 段 `205,332` 字节，断连 0、重试 0；`mp4ff` 合并 MP4 `17,601,607` 字节，时长 `12.93s` 与站点 `13s` 误差 `0.07s`，`moov` 在前、交错存放、两轨带 `elst`）以及画质升级探测（`CheckUpgrade` 更新 `upgradeCheckedAt` 与最高可用档 `116` / `30280`，24 小时内二次非强制探测跳过）全部通过（[ACT-004](../../USER-ACTIONS.md#act-004)）。另记一次计划外的请求：2026-11-05 第一次包冒烟时，打开的登录页按设计立即请求了二维码，向 `qrcode/generate` 发出约 1 次、`qrcode/poll` 约 5 次请求（无凭证、未扫码），没有遇到错误；之后冒烟改为把应用的网络请求指向本机关闭的端口。
- **ACT-005 真实站点检查（2026-11-07）**：第 16～20 节对真实站点执行 1 次全部通过：用 `card` 创建订阅并对目标用户执行 2 次检查更新（第 1 次读取 `feed/space` 第 1 页 12 条建立起点、未读数 0；第 2 次读取第 1 页匹配起点即停、未读数 0）；探测 2 个视频的 `wbi/view` 与 `player/wbi/v2`（均正常返回 `code=0`，所探 2 个视频本身无 CC 字幕轨），对 18 秒视频拉取第 1 段 protobuf 弹幕 `/x/v2/dm/wbi/web/seg.so` 解出 107 条弹幕并生成 `9,976` 字节标准 XML；读取登录账号的关注分组 `/x/relation/tags`（2 个分组）与关注列表第 1 页 `/x/relation/followings`（第 1 页 50 条、总计 244）；查询 `/x/passport-login/web/cookie/info`（返回 `refresh=true` 与毫秒时间戳，按规则不执行刷新）；查询目标用户开播状态 `/room/v1/Room/get_status_info_by_uids`（`live_status=2` 识别为 `round` 轮播）；对指定直播间调用 `/room/v1/Room/room_init`（返回 `live_status=1` 直播中）、`get_status_info_by_uids` 与 `/xlive/web-room/v2/index/getRoomPlayInfo`（不带 WBI 签名直接返回 `http_hls`/`fmp4` `avc` `qn=10000` 原画流），经 `d1--cn-gotcha207.bilivideo.com` 拉取 HLS 播放列表与 fMP4 分片（35 次请求、`8,899,963` 字节，0 断连），停止录制后收尾转成渐进式 MP4（`9,073,932` 字节，时长 `21.01s`，`moov` 在前）并校验通过（[ACT-005](../../USER-ACTIONS.md#act-005)）。

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
| -404、62002（稿件不可见）、62004（审核中）、62012（仅 UP 主自己可见） | 内容不可得 | `unavailable`，带具体原因 |
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

`data` 里用到的字段：`bvid`、`aid`、`title`、`pic`（封面）、`desc`、`pubdate`（Unix 秒）、`duration`（秒）、`owner.{mid,name,face}`、`stat.{view,danmaku,reply,favorite,coin,share,like}`、`pages[].{cid,page,part,duration}`（多 P）、`tname`（分区名，可能为空）、`rights`、`state`。不可得的情况见第 3 节的错误码。

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
- 结果：`data.list.vlist[].{bvid,aid,title,pic,created,length("mm:ss"),play,comment,description}`，`data.page.{pn,ps,count}`。`count` 只是站点给的观察值，不保证完整。置顶视频可能出现在第一页，与时间顺序不一致；不同页之间可能重复，前端按 `bvid` 去重。
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
features=itemOpusStyle,listOnlyfans,opusBigCover,onlyfansVote
dm_img_list=[] dm_img_str=bm8gd2ViZ2 dm_cover_img_str=bm8gd2ViZ2 dm_img_inter={"ds":[],"wh":[0,0,0],"of":[0,0,0]}
```

- **ACT-003 实测结论**：登录态下携带上述参数（不带 WBI 签名）直接通过第 1 页（13 条）和第 2 页（12 条，使用第 1 页返回的 `offset`）；返回 `code=-352` 时客户端支持自动退回 WBI 签名重试一次。
- 访客 Cookie 请求会返回 -352（社区议题 2026-10 报告，未修复），这也是 DEC-018 只做登录模式的依据之一。
- 结果：`data.items[]`、`data.offset`（字符串，下一页用）、`data.has_more`。每条：`id_str`（大整数，按字符串处理）、`type`（`DYNAMIC_TYPE_AV` 视频、`DYNAMIC_TYPE_DRAW` 图文、`DYNAMIC_TYPE_WORD` 文字、`DYNAMIC_TYPE_FORWARD` 转发、`DYNAMIC_TYPE_ARTICLE` 专栏，以及直播、番剧等）、`modules.module_author.{mid,name,face,pub_ts}`、`modules.module_dynamic.desc.text`、`modules.module_dynamic.major`；转发的原动态在 `orig`，结构相同。
- `major.type`：`MAJOR_TYPE_ARCHIVE`（`archive.{bvid,aid,title,cover,duration_text}`）、`MAJOR_TYPE_OPUS`（`opus.{summary.text,pics[].{url,width,height}}`）、`MAJOR_TYPE_DRAW`（`draw.items[].{src,width,height}`）、`MAJOR_TYPE_ARTICLE`、`MAJOR_TYPE_UGC_SEASON`、`MAJOR_TYPE_PGC`、`MAJOR_TYPE_LIVE`、`MAJOR_TYPE_LIVE_RCMD`、`MAJOR_TYPE_COMMON`、`MAJOR_TYPE_COURSES`、`MAJOR_TYPE_MUSIC` 等。没见过的类型显示类型名，不当作错误，也不丢弃。
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

证据等级（第 16～20 节）：**核** = 社区文档与本机参考索引中的实现一致；**社** = 只有社区文档；**参** = 只有参考实现；**未** = 没有核实或说法不一。已在夹具测试与 [ACT-005](../../USER-ACTIONS.md#act-005) 真实站点有界检查中验证。

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
