# objbench

`objbench` 是一个对象存储性能压测工具，基于 [`github.com/zhangyf/objstore`](https://github.com/zhangyf/objstore) 接口库实现。
通过统一的 `objstore.Store` 接口对接 **腾讯云 COS** 与 **AWS S3（及 S3 兼容存储）**，一套代码两种后端。

## 特性

- **统一接口**：基于 `objstore.Store`，COS / S3 用同一套压测逻辑。
- **多档文件大小**：一次运行对多个对象大小分别压测（如 `4k,64k,1m,16m`）。
- **固定测试时长**：每个大小分组运行指定时长（`-duration`）。
- **读写混合比例**：通过 `-read-ratio` 指定读操作占比，其余为写。
- **完整指标**：测试结束输出 QPS、吞吐量、Min/Mean/P50/P90/P95/P99/Max 时延，上传/下载/汇总分开统计。
- **可控并发与限速**：除并发 worker 数外，支持 `-rate` 令牌桶限制目标 QPS，做平滑阶梯加压。
- **分布式集群压测**：多机协同打压，通过存储桶「公告栏」零依赖协调，全局聚合 QPS / 吞吐 / 合并分位。
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
| `-rate`         | `0`             | 目标 QPS 上限（令牌桶）；`0` = 不限速（饱和压测） |
| `-burst`        | `1`             | 令牌桶突发大小；越小节奏越平滑             |

## 可控并发与限速（令牌桶）

`-concurrency` 控制并行 worker 数（并发度），`-rate` 控制**目标 QPS 上限**，两者解耦：

- `-rate 0`（默认）：不限速，worker 全力打满后端，测**饱和极限**。
- `-rate N`：用令牌桶把总操作速率限制在约 N QPS，做**可控压力 / 阶梯加压**（如 5000→8000→11000 观察时延拐点）。

令牌桶是**纯进程内、基于时间**的限速器，不读写存储桶，因此即使后端被限流，限速节奏的精度也不受影响——实测 QPS 打不上去时，差值本身就是有价值的结论。

要点：
- **`-burst` 设小**（默认 1）以获得平滑、均匀的请求流，适合采集干净的时延数据。
- **worker 数要足够**覆盖 `rate × 单请求时延`（Little 定律：并发 ≈ QPS × 时延），否则令牌发出却无空闲 worker 执行，会人为放大时延。
- 令牌桶按绝对时间表发放令牌（开环），可缓解压测经典的 **Coordinated Omission**（协调遗漏）问题，使 P99 更真实。

```bash
# 用 64 个 worker，但把总速率限制在 5000 QPS，平滑加压
objbench -provider cos -bucket my-bucket-1250000000 -region ap-beijing \
  -sizes 1m -duration 60s -concurrency 64 -rate 5000 -burst 1
```

## 分布式集群压测

单机受网卡 / CPU 限制压不满后端时，用多台机器协同打压。

### 架构：存储桶当「公告栏」

采用 **coordinator（协调者）+ agent（压测节点）** 架构，**通过一个 objstore 桶作为公告栏**进行协调，无需额外服务、端口或机器间直连——只要各节点都能访问同一个协调桶即可，天然适配跨网络 / 跨机房集群。

```
┌─────────────┐        协调桶（公告栏）           ┌──────────┐
│ Coordinator │ ① 写计划 control/plan.json  →     │ Agent A  │
│             │ ← ③ 写结果 results/A.json         │ Agent B  │
│ ④ 收齐聚合   │ ← ③ 写结果 results/B.json         │  ...     │
└─────────────┘                                   └──────────┘
```

流程：
1. **coordinator 发计划**：把 size / duration / 每机限速 / 统一起跑时刻写入 `control/plan.json`。
2. **agent 签到 + 领计划**：每台机器启动后写 `agents/<id>.json` 报到，并轮询读计划。
3. **对表齐步走**：计划里写死一个**绝对起跑时刻**（`start-delay` 后），各 agent 到点同时开打（靠 NTP 时钟同步，无需互相通信）。
4. **上报 + 聚合**：每台机器跑完把结果写回 `results/<id>.json`；coordinator 收齐后合并算出**全局总 QPS / 吞吐 / 合并分位**。

> 公告栏读写**只发生在压测前后**（领计划 / 上报），压测过程中各机器只打数据、不碰公告栏，因此避开了流控高峰；读写还带指数退避重试。**强烈建议协调桶用一个独立小桶**（与被测桶物理隔离），彻底消除干扰。

### A 模式：加机器即加压力

计划**不预设机器数**，只规定「每台机器打多少」（`-rate` 为**每 agent 配额**）：

> **集群总压力 ≈ 每 agent 配额 × 实际到场的 agent 数**

要加压，只需在新机器上再启动一个 `agent`，**无需改动任何计划**。例如每 agent 限 3000 QPS：3 台 → 9000，加到 4 台 → 12000。

全局分位的合并：各 agent 上报**蓄水池采样**的时延样本（每类操作上限 1 万个），coordinator 合并所有样本后重新计算真实的全局 P50/P90/P95/P99，避免「分位的平均」这种统计错误。

### 用法

**在每台压测机器上**启动 agent（先启动，等计划）：

```bash
objbench agent \
  -provider cos -bucket TARGET-bucket-1250000000 -region ap-beijing \
  -coord-provider cos -coord-bucket COORD-bucket-1250000000 -coord-region ap-beijing \
  -coord-prefix objbench-coord -agent-id $(hostname)
```

**在协调机器上**发布计划并收集聚合（每 agent 限 3000 QPS）：

```bash
objbench coordinate \
  -coord-provider cos -coord-bucket COORD-bucket-1250000000 -coord-region ap-beijing \
  -coord-prefix objbench-coord \
  -run-id run-$(date +%s) -sizes 1m,8m -duration 60s -concurrency 64 \
  -rate 3000 -burst 1 \
  -start-delay 30s -expect-results 3 -collect-timeout 10m
```

- `-rate`（coordinate）：**每 agent** QPS 配额；集群总量 ≈ rate × agent 数。
- `-start-delay`：发计划到统一起跑的提前量，agent 必须在此之前加入。
- `-expect-results N`：收到 N 份结果即聚合输出（否则等到 `-collect-timeout`）。
- agent 与被测桶用 `-bucket/-provider/...`，协调桶用 `-coord-*`，两套凭证可分别走环境变量（协调桶用 `OBJBENCH_COORD_SECRET_ID/KEY/BUCKET/REGION`）。

### 分布式参数

| 参数 | 适用 | 说明 |
|------|------|------|
| `-coord-bucket` | agent/coordinate | 协调桶（独立桶，必填） |
| `-coord-provider/-coord-region/-coord-secret-id/-coord-secret-key/-coord-endpoint/-coord-profile` | agent/coordinate | 协调桶连接参数 |
| `-coord-prefix` | agent/coordinate | 协调对象 key 前缀（默认 `objbench-coord`） |
| `-agent-id` | agent | 节点唯一 id（默认 `hostname-pid`） |
| `-poll` | agent | 轮询计划间隔（默认 2s） |
| `-wait-timeout` | agent | 等待计划最长时间（默认 10m） |
| `-run-id` | coordinate | 本轮 run id（默认时间戳） |
| `-rate` | coordinate | **每 agent** QPS 配额 |
| `-start-delay` | coordinate | 起跑提前量（默认 30s） |
| `-expect-results` | coordinate | 收到该数量结果即聚合（0 = 等满 timeout） |
| `-collect-timeout` | coordinate | 等待结果最长时间（默认 10m） |
| `-expect-agents` | coordinate | （可选）等待该数量 agent 签到后再锁定起跑时刻 |

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
