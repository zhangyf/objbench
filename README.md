# objbench

`objbench` 是一个对象存储性能压测工具，基于 [`github.com/zhangyf/objstore`](https://github.com/zhangyf/objstore) 接口库实现。
通过统一的 `objstore.Store` 接口对接 **腾讯云 COS** 与 **AWS S3（及 S3 兼容存储）**，一套代码两种后端。

## 特性

- **统一接口**：基于 `objstore.Store`，COS / S3 用同一套压测逻辑。
- **多档文件大小**：一次运行对多个对象大小分别压测（如 `4k,64k,1m,16m`）。
- **固定测试时长**：每个大小分组运行指定时长（`-duration`）。
- **读写混合比例**：通过 `-read-ratio` 指定读操作占比，其余为写。
- **完整指标**：测试结束输出 QPS、吞吐量、Min/Mean/P50/P90/P95/P99/Max 时延，上传/下载/汇总分开统计。
- **自动预热与清理**：读测试前自动预上传对象作为目标；结束后可自动清理。

## 安装

```bash
# 私有依赖，需先配置 GOPRIVATE 与 git 凭据
go env -w GOPRIVATE=github.com/zhangyf
go install github.com/zhangyf/objbench/cmd/objbench@latest
```

或从源码构建：

```bash
git clone https://github.com/zhangyf/objbench.git
cd objbench
go build -o objbench ./cmd/objbench
```

## 使用

### 腾讯云 COS

```bash
objbench \
  -provider cos \
  -bucket my-bucket-1250000000 \
  -region ap-beijing \
  -secret-id  $COS_SECRET_ID \
  -secret-key $COS_SECRET_KEY \
  -sizes 4k,64k,1m,8m \
  -duration 30s \
  -concurrency 32 \
  -read-ratio 0.7
```

### AWS S3

```bash
objbench \
  -provider s3 \
  -bucket test-bkt-tk \
  -region ap-northeast-1 \
  -secret-id  $AWS_ACCESS_KEY_ID \
  -secret-key $AWS_SECRET_ACCESS_KEY \
  -sizes 1m,8m,64m \
  -duration 1m \
  -concurrency 64 \
  -read-ratio 0.5
```

> S3 凭证为空时自动走 AWS default credential chain（env / 共享凭据文件 / IMDS / STS）。
> 可用 `-profile` 指定 AWS profile，`-endpoint` 指定 S3 兼容存储的自定义 endpoint。

### 从环境变量读取凭证（不在命令行暴露 ak/sk）

命令行参数为空时，自动按以下顺序回退到环境变量（命令行优先级最高）：

| 配置项     | 通用变量              | COS 回退         | S3 回退                   |
|------------|-----------------------|------------------|---------------------------|
| secret-id  | `OBJBENCH_SECRET_ID`  | `COS_SECRET_ID`  | `AWS_ACCESS_KEY_ID`       |
| secret-key | `OBJBENCH_SECRET_KEY` | `COS_SECRET_KEY` | `AWS_SECRET_ACCESS_KEY`   |
| bucket     | `OBJBENCH_BUCKET`     | —                | —                         |
| region     | `OBJBENCH_REGION`     | —                | —                         |

```bash
# COS：凭证全部从环境变量读取，命令行不出现 ak/sk
export COS_SECRET_ID=...
export COS_SECRET_KEY=...
objbench -provider cos -bucket my-bucket-1250000000 -region ap-beijing \
  -sizes 4k,1m,8m -duration 30s -concurrency 32 -read-ratio 0.7

# S3：留空 secret-id/secret-key 即走 AWS default credential chain
export AWS_ACCESS_KEY_ID=...
export AWS_SECRET_ACCESS_KEY=...
objbench -provider s3 -bucket test-bkt-tk -region ap-northeast-1 \
  -sizes 1m,8m -duration 1m -concurrency 64
```

> 推荐用环境变量，避免凭证出现在 shell history / 进程列表（`ps`）中。

## 参数

| 参数            | 默认值          | 说明                                       |
|-----------------|-----------------|--------------------------------------------|
| `-provider`     | `cos`           | 存储后端：`cos` 或 `s3`                    |
| `-bucket`       | （必填）        | 桶名                                       |
| `-region`       | `""`            | 区域，如 `ap-beijing` / `ap-northeast-1`   |
| `-secret-id`    | `""`            | COS SecretId / S3 Access Key ID（空则读环境变量） |
| `-secret-key`   | `""`            | COS SecretKey / S3 Secret Access Key（空则读环境变量） |
| `-endpoint`     | `""`            | 自定义 endpoint（S3 兼容模式）             |
| `-profile`      | `""`            | AWS profile（仅 S3，密钥为空时生效）       |
| `-sizes`        | `4k,64k,1m,8m`  | 逗号分隔的对象大小列表，支持 `k/m/g` 单位   |
| `-duration`     | `30s`           | 每个大小分组的测试时长                     |
| `-concurrency`  | `16`            | 并发 worker 数                             |
| `-read-ratio`   | `0.5`           | 读操作占比 `[0,1]`，其余为写               |
| `-prefix`       | `""`            | 所有压测对象的 key 前缀                    |
| `-cleanup`      | `true`          | 测试结束后删除创建的对象                   |
| `-warmup`       | `0`             | 每个大小预上传的对象数（0 = 并发数）       |

## 输出示例

```
========== size=1MiB ==========
wall: 30.01s
op        count   err  qps      throughput    min       mean     p50      p90      p95      p99      max
upload    10835   0    361.1    361.0 MiB/s   1.96ms    20.4ms   18.6ms   30.8ms   38.8ms   59.2ms   152ms
download  10773   0    359.0    359.0 MiB/s   0.72ms    8.51ms   7.13ms   12.5ms   14.1ms   19.0ms   84ms
overall   21608   0    720.1    720.0 MiB/s   0.72ms    14.5ms   ...
```

## 指标说明

- **QPS**：单位时间内成功完成的操作数。
- **throughput**：单位时间传输的字节数（上传按对象大小计，下载按实际读取字节计）。
- **P90/P95/P99**：采用 nearest-rank 方法从全部时延样本中计算的分位时延。
- **upload / download / overall**：分别为写、读、合并的统计。

## 实现说明

- 上传走 `Store.PutObjectStream(ctx, key, r, size)`，下载走 `Store.GetObject` 并完整读取计入吞吐。
- 每个 worker 独立随机决定读/写；读操作从已上传的 key 中随机选取目标。
- 时延全量采样后排序计算分位，避免估算误差。

## License

Apache License 2.0
