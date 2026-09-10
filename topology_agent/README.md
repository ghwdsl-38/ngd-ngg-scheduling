# LLDP拓扑采集与Node打标组件

`topology_agent`运行在每个Worker上，只采集“Worker直连上层Leaf”的事实，
并写入Kubernetes Node元数据。Leaf以上的Room、Border、Spine等拓扑仍由
Algorithm Server读取独立配置文件，不由Agent推断。

本实现保留参考采集器的 Socket、邻居去重及日志方式，Bond 改为主接口采集。
Node 元数据协议保持兼容：

- Kubernetes访问继续使用官方`client-go`；
- 同时支持In-Cluster和显式Kubeconfig；
- 继续写入`topology.demo.ngg.io/*`；
- 继续生成Leaf集合Hash；
- PRC无需修改即可读取这些标签和注解。

## 1. 处理流程

```text
main.go
  │ 读取NODE_NAME、认证和采集参数
  ▼
agent.go: topologyAgent.run
  │ 默认执行一轮后退出
  │ 显式--interval=3m时，每3分钟开始一轮
  ▼
agent.go: topologyAgent.collect
  │
  ├─ pkg/bond/discovery.go
  │    自动识别物理有线口和Bond
  │    Bond选择主接口
  │    Slave状态仅用于日志及显式接口模式
  │    排除lo、veth、poh、bridge等虚拟接口
  │
  └─ lldp.go: receiveLLDP
       打开AF_PACKET/SOCK_RAW Socket
       监听EtherType 0x88cc
       自动模式始终不Bind，按ifindex做用户态过滤
       单个显式接口才执行unix.Bind
       排除PACKET_OUTGOING和疑似本机反射
       解析LLDP TLV
  ▼
pkg/topologyfacts/facts.go
  │ 规范化Leaf、链路和Bond信息
  │ 生成Leaf集合Hash和Node元数据
  ▼
kube.go
  │ client-go GET → MergePatch → GET回读验证
  ▼
Kubernetes Node
```

### 1.1 Bond 主接口采集

`--interfaces=bond0` 直接绑定 bond0，自动模式也选择有效 Bond 主接口。
一轮可保留两个 Leaf 的观察，两条记录都标记 `interface=bond0`。
无法确认报文来自哪个 Slave 时不推断物理端口，`active=false` 表示未确认物理活动链路，
不代表该 Leaf 不可调度；现有 PRC/Algorithm 不按这个字段过滤节点。

## 2. 目录文件

| 文件 | 功能 |
|---|---|
| `main.go` | Cobra入口、参数、Kubernetes认证初始化 |
| `agent.go` | 周期采集、元数据生成和Node同步编排 |
| `lldp.go` | LLDP TLV解析、邻居身份与排序 |
| `lldp_socket_linux.go` | Linux AF_PACKET Socket、绑定与接收 |
| `bond.go` | 主包到Bond发现包的薄适配层 |
| `pkg/bond/discovery.go` | 物理网卡、链路和Bond自动发现 |
| `pkg/topologyfacts/facts.go` | Leaf集合规范化、Hash和Node元数据协议 |
| `kube_config.go` | In-Cluster/Kubeconfig配置加载 |
| `kube.go` | 官方client-go的Node Get/Patch/Get回读验证 |
| `bond_test.go` | 主备、LACP、物理口、虚拟口和/proc回退测试 |
| `lldp_test.go` | LLDP报文解析、自反射和故障关闭测试 |

## 3. 网卡选择规则

- `--interfaces=bond0`：直接选择并绑定 Bond 主接口，不展开 Slave。
- `--interfaces=auto`：选择链路正常的 Bond 主接口及独立物理有线口，排除 Bond Slave 和其他虚拟接口。
- `--interfaces=eth0`：按指定网卡收包，支持显式 Slave。
- 多接口用逗号分隔；一个显式接口执行 Bind，自动/多接口模式按选中 ifindex 过滤。
- Bond 模式、Slave 列表、Active Slave、carrier/operstate/MII 仍从 sysfs 和 `/proc/net/bonding` 读取并输出日志。

生产部署建议显式指定业务 Bond，避免管理网等其他接口进入 Leaf 集合。

## 4. LLDP采集和解析

Agent不调用`lldpd`或`lldpcli`，直接执行：

```text
unix.Socket(AF_PACKET, SOCK_RAW, htons(0x88cc))
```

需要root或`CAP_NET_RAW`。当前解析：

- Chassis ID及Subtype；
- Port ID及Subtype；
- TTL；
- Port Description；
- System Name；
- System Description；
- System Capabilities；
- Management Address。

用于Leaf身份的规则保持不变：优先使用`System Name`，缺少时使用
`Chassis ID`。邻居按“本地接口+Chassis+Port”保留，同一接口上的不同
邻居不会在采集阶段被覆盖；最终Leaf集合按Chassis ID去重。

采集过程对齐已在真实集群验证的`lldp-new-3`：

- `count=0`时始终监听完整的`--timeout`窗口；
- `count>0`时收集到指定数量的唯一邻居即可结束；
- 邻居身份由“本地接口+Chassis+Port”组成，重复报文更新已有记录；
- 超时后只要至少存在一个有效外部邻居，就继续生成元数据并打标；
- 不再因为非主备Bond没有凑够两个不同Leaf而放弃本轮打标。

Chassis ID是判断物理Leaf是否
不同的依据；System Name是写入Node并与Algorithm配置匹配的Leaf ID。两个
接口收到同一个Chassis只会形成一个Leaf集合成员，但两条物理链路明细均可
保留。只有整个窗口没有有效外部邻居时，本轮才失败并保留Node上的旧拓扑。

## 5. Node元数据协议

标签：

```text
topology.demo.ngg.io/leaf-set-id=<Leaf集合Hash>
topology.demo.ngg.io/leaf-count=<去重后的Leaf数量，至少为1>
topology.demo.ngg.io/leaf-switch=<只有一个且值合法时写入>
```

注解：

```text
topology.demo.ngg.io/leaf-switch-ids=["Leaf-A","Leaf-B"]
topology.demo.ngg.io/leaf-links=[...]
topology.demo.ngg.io/source=LLDP
topology.demo.ngg.io/observed-at=<RFC3339时间>
topology.demo.ngg.io/local-interface=<接口>
topology.demo.ngg.io/remote-port=<交换机端口>
```

PRC读取位置是`prc/pkg/controller/snapshot.go`。因此不能直接改成
`lldp.network.local/*`，否则现有PRC无法构建Node静态拓扑快照。

## 6. 运行参数

| 参数 | 默认值 | 含义 |
|---|---:|---|
| `--interfaces` | `auto` | 自动选择有效独立物理有线口和Bond主接口；也可指定Bond或具体接口 |
| `--timeout` | `120s` | 单轮LLDP监听窗口；Go Duration格式，可修改为`90s`等 |
| `--count` | 0 | 最大唯一邻居数；0表示始终监听完整timeout窗口 |
| `--interval` | `0` | 0表示执行一轮后退出；设置为`3m`等正值才周期执行 |
| `--sys-class-net` | `/sys/class/net` | 网卡sysfs根目录 |
| `--kubeconfig` | 空 | 空时优先In-Cluster，也支持显式文件 |
| `--kube-context` | 空 | 显式覆盖Kubeconfig Context |
| `--node-name` | `NODE_NAME`或hostname | 要更新的Kubernetes Node名称 |

Node名称必须与Kubernetes Node的`metadata.name`完全一致。DaemonSet通过
Downward API设置`NODE_NAME`，一般不需要显式传`--node-name`。

直接运行二进制且不传`--interval`时，完成一次采集和打标后退出。项目的
DaemonSet清单显式传入`--interval=180s`，因此集群部署仍然每3分钟执行一轮。

## 7. 部署条件

DaemonSet需要：

- `hostNetwork: true`；
- `NET_RAW`；
- 以只读方式挂载完整宿主机`/sys`；
- `get/patch nodes` RBAC；
- ServiceAccount或显式Kubeconfig；
- 通过Downward API把`spec.nodeName`写入`NODE_NAME`。

必须挂载完整`/sys`，不能只挂载`/sys/class/net`，否则`device`指向
`../../devices`的符号链接可能在容器内断开，导致物理网卡识别失败。

统一部署入口见项目`deploy/README.md`。

## 8. 本地测试

本地没有真实LLDP交换机也能验证解析和接口策略：

```bash
cd topology_agent
go test ./... -v
```

关键用例：

```bash
go test . -run '^TestAutomaticDiscoverySelectsPhysicalAndRejectsVirtual$' -v
go test . -run '^TestAutomaticDiscoveryRejectsVirtualOnlyEnvironment$' -v
go test . -run '^TestProcBondingFallbackSelectsBondMaster$' -v
go test . -run '^TestParseLLDPFrameReadsSwitchDetails$' -v
```

这些用例使用临时sysfs和构造的LLDP帧，不要求主机安装`lldpd`，也不创建
Raw Socket。虚拟网卡环境的预期结果是自动发现失败并停止，不生成Node
标签；这是正确的故障关闭行为。

真实物理环境验收仍需确认：

```bash
sudo tcpdump -i <物理口> -nn -e -vv ether proto 0x88cc
```

随后部署Agent并检查日志及Node上的`topology.demo.ngg.io/*`元数据。

## 9. 当前边界

- Agent只负责Node到直连Leaf，不处理Leaf以上拓扑；
- 程序未传接口参数时默认`auto`，当前部署清单显式限定`bond0`；
- `count=0`会跑满120秒，避免LLDP发送周期较长时过早结束；
- 不强制要求发现两个Leaf，至少一个有效外部邻居即可更新Node；
- Leaf集合按Chassis识别物理设备，PRC使用的Leaf ID优先取System Name；
- 写入使用`client-go`，并在Merge Patch后重新GET Node逐项验证。
