# objbench

`objbench` 是一个对象存储性能压测工具，基于 [thanos-io/objstore](https://github.com/thanos-io/objstore) 接口库实现。
一套代码即可对接 S3、腾讯云 COS、GCS、Azure Blob、阿里云 OSS、华为 OBS、百度 BOS、Swift、本地文件系统等多种后端。

## 特性

- **多后端统一接口**：通过 `objstore` 的 `Bucket` 接口操作，配置驱动，无需为每个云改代码。
- **多档文件大小**：一次运行可对多个对象大小分别压测（如 `4k,64k,1m,16m`）。
- **固定测试时长**：每个大小分组运行指定时长（`-duration`）。
- **读写混合比例**：通过 `-read-ratio` 指定读操作占比，其余为写。
- **完整指标**：测试结束后输出 QPS、吞吐量、Min/Mean/P50/P90/P95/P99/Max 时延，上传/下载/汇总分别统计。
- **自动预热与清理**：读测试前自动预上传对象作为目标；结束后可自动清理。

## 安装

```bash
go install github.com/zhangyf/objbench/cmd/objbench@latest
```

或从源码构建：

```bash
git clone https://github.com/zhangyf/objbench.git
cd objbench
go build -o objbench ./cmd/objbench
```

## 使用

### 1. 准备 bucket 配置（objstore YAML 格式）

S3 示例 `s3.yaml`：

```yaml
type: S3
config:
  bucket: my-bucket
  endpoint: s3.ap-northeast-1.amazonaws.com
  region: ap-northeast-1
  access_key: <AK>
  secret_key: <SK>
```

腾讯云 COS 示例 `cos.yaml`：

```yaml
type: COS
config:
  bucket: my-bucket-1250000000
  region: ap-beijing
  app_id: "1250000000"
  secret_id: <SecretId>
  secret_key: <SecretKey>
```

本地文件系统（用于本机验证）`fs.yaml`：

```yaml
type: FILESYSTEM
config:
  directory: /tmp/objbench-data
```

> 配置字段与各 provider 的官方定义一致，详见 objstore 文档。

### 2. 运行压测

```bash
objbench \
  -config s3.yaml \
  -sizes 4k,64k,1m,8m \
  -duration 30s \
  -concurrency 32 \
  -read-ratio 0.7
```

## 参数

| 参数            | 默认值          | 说明                                       |
|-----------------|-----------------|--------------------------------------------|
| `-config`       | （必填）        | objstore bucket YAML 配置文件路径          |
| `-sizes`        | `4k,64k,1m,8m`  | 逗号分隔的对象大小列表，支持 `k/m/g` 单位   |
| `-duration`     | `30s`           | 每个大小分组的测试时长                     |
| `-concurrency`  | `16`            | 并发 worker 数                             |
| `-read-ratio`   | `0.5`           | 读操作占比 `[0,1]`，其余为写               |
| `-prefix`       | `""`            | 所有压测对象的 key 前缀                    |
| `-cleanup`      | `true`          | 测试结束后删除创建的对象                   |
| `-warmup`       | `0`             | 每个大小预上传的对象数（0 = 并发数）       |
| `-verbose`      | `false`         | 打开 objstore 客户端日志                   |

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

## License

Apache License 2.0
