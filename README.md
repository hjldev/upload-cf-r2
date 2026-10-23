# r2up

用 Go 编写的 Cloudflare R2 文件上传命令行工具，基于 R2 的 S3 兼容 API。

- 上传单个文件或递归上传目录，支持前缀、`--exclude` 排除、并发上传
- 大文件自动分片上传（并发分片），中断（Ctrl+C）或失败时自动清理未完成的分片
- 终端中实时显示总进度和速度；自动识别 Content-Type，可设置 Cache-Control
- `--skip-existing` 跳过远端已存在且大小相同的文件，适合重复同步
- `r2up init` 交互式填写凭证、测试连接并写入配置文件
- 附带 `ls`、`rm`、`presign`（生成下载/上传预签名链接）
- `r2up multipart` 列出并清理中断后残留的分片上传

## 安装

在本仓库根目录执行，安装到 `$GOPATH/bin`：

```bash
go install ./cmd/r2up
```

或构建到 `bin/` 并注入版本号：

```bash
go build -ldflags "-X main.version=v0.1.0" -o bin/r2up ./cmd/r2up
```

模块名目前是 `upload-cf`，若要发布后用 `go install <仓库地址>/cmd/r2up@latest` 安装，需要先把 `go.mod` 里的模块名改成仓库地址。

## 配置

先在 Cloudflare 控制台 → R2 → **管理 R2 API 令牌** 创建一个具有“对象读和写”权限的令牌，
页面会显示 Access Key ID、Secret Access Key（只显示一次）和 S3 终结点地址。然后运行：

```bash
r2up init
```

按提示填写即可：

```
账户 ID（也可粘贴 S3 终结点地址）: https://<账户ID>.r2.cloudflarestorage.com
Access Key ID: ...
Secret Access Key（输入时不显示）:
默认存储桶: my-bucket
公开访问域名（可选，输入 - 清空）: cdn.example.com

正在测试连接… 成功，可以访问存储桶 my-bucket
配置已保存到 /Users/you/.config/r2up/config.json
```

- 账户 ID 一栏直接粘贴控制台给出的终结点地址即可，会自动提取账户 ID 和辖区（如 `eu`）。
- 保存前会用填写的凭证列一次存储桶来测试连接，失败时给出原因并询问是否仍然保存；`--no-verify` 跳过测试。
- 配置文件权限自动设为 `600`。再次运行 `r2up init` 时，`[ ]` 中显示当前值（密钥只显示末 4 位），回车即保留，可用来修改单项配置。
- `--config` 指定其他路径，`-b`、`--public-url` 等全局参数的值会作为默认值预先填好。
- CI 等非交互环境不需要 `init`，直接设置下文的环境变量即可。

生成的配置文件（默认 `~/.config/r2up/config.json`，Windows 为 `C:\Users\<用户名>\.config\r2up\config.json`）
格式如下，也可以参考 [config.example.json](config.example.json) 手动编写：

```json
{
  "account_id": "你的账户 ID",
  "access_key_id": "xxxxxxxx",
  "secret_access_key": "xxxxxxxx",
  "bucket": "my-bucket",
  "public_url": "https://cdn.example.com"
}
```

也可以只用环境变量（适合 CI）：

| 环境变量 | 说明 |
| --- | --- |
| `R2_ACCOUNT_ID` | Cloudflare 账户 ID |
| `R2_ACCESS_KEY_ID` / `R2_SECRET_ACCESS_KEY` | R2 API 令牌 |
| `R2_BUCKET` | 默认存储桶 |
| `R2_PUBLIC_URL` | 可选，自定义域名或 r2.dev 地址，上传后输出访问链接 |
| `R2_JURISDICTION` | 可选，数据辖区，如 `eu` |
| `R2_ENDPOINT` | 可选，直接指定 S3 API 地址（覆盖账户 ID） |

优先级：命令行参数（`-b`、`--account-id`、`--endpoint`、`--public-url`）> 环境变量 > 配置文件。
密钥只能通过配置文件或环境变量提供，避免出现在 shell 历史里。

## 使用

`upload` 是默认命令：不写子命令时，`r2up <文件或目录>` 等同于 `r2up upload <文件或目录>`，
upload 的所有参数都可以直接使用。

```bash
# 上传单个文件，key 为文件名
r2up photo.jpg

# 指定完整 key
r2up backup.tar.gz -k backups/2026-10-21.tar.gz

# 上传整个目录到 site/ 下（key 不包含 dist 本身），排除无用文件
r2up ./dist -p site -x .DS_Store -x '*.map'

# 先看看会上传什么
r2up ./dist -p site -n

# 静态资源：长缓存 + 跳过已上传的文件 + 8 个文件并发
r2up ./assets -p static --cache-control "public, max-age=31536000" --skip-existing -j 8

# 临时换一个存储桶
r2up report.pdf -b another-bucket

# 列出对象（默认按 / 分层，-r 递归）
r2up ls site/
r2up ls -r site/

# 删除
r2up rm site/old.html

# 生成 24 小时有效的下载链接 / 1 小时有效的上传链接
r2up presign reports/2026.pdf -e 24h
r2up presign incoming/data.csv --put
curl -T data.csv "<上一步输出的链接>"
```

> 如果本地文件名恰好和子命令同名（`init`、`ls`、`rm`、`presign`、`upload`、`multipart`、`help`、`completion` 以及别名 `up`、`cp`、`delete`、`mp`），
> 会被当成子命令执行，此时请写成 `r2up upload ls` 或 `r2up ./ls`。
> 首个参数既不是本地文件、又和某个子命令很像时（如 `r2up uplod x`），会提示可能想用的子命令，而不会去上传。

### upload 参数（`r2up <文件>` 同样适用）

| 参数 | 默认 | 说明 |
| --- | --- | --- |
| `-p, --prefix` | | key 前缀（相当于远端目录） |
| `-k, --key` | | 单个文件的完整 key，不能与 `--prefix` 同时使用 |
| `-x, --exclude` | | 排除匹配文件名或相对路径的 glob，可重复；匹配到目录时整个目录跳过 |
| `-j, --concurrency` | 4 | 同时上传的文件数 |
| `--part-concurrency` | 10 | 单个文件同时上传的分片数 |
| `--part-size` | 8 | 分片大小（MiB，≥5；超大文件会自动调大以满足 10000 片上限） |
| `--multipart-threshold` | 16 | 超过该大小（MiB）使用分片上传 |
| `--content-type` | 自动 | 覆盖 Content-Type |
| `--cache-control` | | 设置 Cache-Control |
| `--skip-existing` | false | 跳过远端已存在且大小相同的对象 |
| `-n, --dry-run` | false | 只打印计划，不上传 |
| `-q, --quiet` | false | 只输出错误 |

有文件上传失败时退出码为 1，其余文件会继续上传。

## 清理未完成的分片上传

分片上传中途中断时，已传上去的分片会留在存储桶里：它们**不会出现在 `r2up ls` 的对象列表中，但同样占用空间并计费**。
r2up 在上传出错或按 Ctrl+C 时会自动清理；但如果进程被强制结束、电脑断电或断网导致清理请求发不出去，分片就会残留。

```bash
# 列出未完成的分片上传及占用空间（可加前缀过滤）
r2up multipart
r2up multipart videos/

# 清理：先列出，确认后取消上传并删除已传的分片
r2up multipart clean

# 只清理一天前开始的上传，不询问（适合放进定时任务）
r2up multipart clean --older-than 24h -y
```

```
2026-10-23 14:22:08    3h12m 前    10 片   80.0 MiB  videos/big.bin
共 1 个未完成的分片上传，占用 80.0 MiB
```

- `r2up ls` 发现同一前缀下有未完成的分片上传时，会在末尾提示数量。
- **正在进行中的上传也会出现在列表里**，清理它会导致那次上传失败。别处可能还在上传时，用 `--older-than` 只清理较早的。
- 非交互环境（如脚本、CI）下 `clean` 必须加 `-y` 才会执行。

## 实现说明

- 使用 `aws-sdk-go-v2` 的 S3 客户端，Region 为 `auto`，path-style 寻址。
- 新版 SDK 默认会给请求附加 CRC32 校验头，R2 对此支持不完整，因此按 Cloudflare 文档设置为
  `RequestChecksumCalculationWhenRequired` / `ResponseChecksumValidationWhenRequired`。
- R2 要求分片上传中除最后一片外每片大小相同，工具始终使用固定分片大小。
- 不读取 `~/.aws` 下的配置，避免与 AWS 凭证混用。

## 开发

```bash
go test ./...
```

端到端测试使用 [gofakes3](https://github.com/johannesboyne/gofakes3) 在内存中模拟 S3 服务，不需要真实的 R2 账号。
