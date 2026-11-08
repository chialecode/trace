# 界面字体

| 文件 | 来源 | 许可 | SHA-256 |
| --- | --- | --- | --- |
| `InterVariable.ttf` | Inter 4.1，官方发布 <https://github.com/rsms/inter/releases/tag/v4.1> 的 `Inter-4.1.zip`（压缩包 SHA-256 `9883fdd4a49d4fb66bd8177ba6625ef9a64aa45899767dde3d36aa425756b11e`），取包内根目录的同名文件，未修改 | SIL Open Font License 1.1，原文见 `OFL.txt`（包内 `LICENSE.txt`） | `4989b125924991b90d05b2d16e0e388c48f7d5bb8b30539bbf9c755278d0ccaf` |

- 查证日期 2026-11-08：仓库未归档，许可为 OFL-1.1，v4.1 发布于 2024-11-16，为当时最新发布。
- 程序启动时用 `ui.RegisterFont` 注册为 `Inter`（`internal/ui/font.go`），中文等 Inter 没有的字由系统字体补上（DirectWrite 回退）。
- 打包时 `OFL.txt` 随包写进 `THIRD-PARTY-NOTICES.txt`。升级字体时替换两个文件并更新本表。
