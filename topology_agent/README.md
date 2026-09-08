# LLDP拓扑采集与Node打标组件

`topology_agent`运行在每个Worker上，只采集“Worker直连上层Leaf”的事实，
并写入Kubernetes Node元数据。Leaf以上的Room、Border、Spine等拓扑仍由
Algorithm Server读取独立配置文件，不由Agent推断。

本实现的**网卡发现、Bond筛选、Socket监听、邻居去重和超时退出流程**
严格对齐物理服务器验证版`lldp-new-2`。同时保留本项目原有的数据协议：

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
  │ 周期执行，先GET当前Node
  ▼
agent.go: topologyAgent.collect
  │
  ├─ pkg/bond/discovery.go
  │    自动识别物理有线口和Bond
  │    主备只选Active Slave
  │    其他模式选择Link/MII有效Slave
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
  │ client-go MergePatch
  ▼
Kubernetes Node
```

### 1.1 与lldp-new-2的采集流程对齐情况

| 采集节点 | 当前Topology Agent | `lldp-new-2` | 状态 |
|---|---|---|---|
| 枚举接口 | `net.Interfaces()` | `net.Interfaces()` | 一致 |
| Link有效性 | Admin Up且`carrier=1`或`operstate=up` | 相同 | 一致 |
| 物理口识别 | `device`、Ethernet type、排除无线 | 相同 | 一致 |
| Bond数据源 | sysfs，缺失项由`/proc/net/bonding`补充 | 相同 | 一致 |
| 主备模式 | 只选Active Slave | 相同 | 一致 |
| 非主备模式 | 选Link/MII有效的Slave | 相同 | 一致 |
| 自动Socket | 不Bind，按选中ifindex过滤 | 相同 | 一致 |
| 单显式接口 | 对指定接口执行`unix.Bind` | 相同 | 一致 |
| 本机报文 | 丢弃`PACKET_OUTGOING` | 相同 | 一致 |
| 邻居身份 | 接口+Chassis subtype/ID+Port subtype/ID | 相同 | 一致 |
| 重复报文 | 更新已有邻居，不重置idle计时 | 相同 | 一致 |
| 普通物理口结束 | 首个新邻居后等待idle timeout | 相同 | 一致 |
| 全Bond Slave结束 | 每个选中Slave均有邻居时提前结束 | 相同 | 一致 |
| 本机反射 | 采集时标记，Leaf筛选阶段剔除 | 相同 | 一致 |
| LLDP解析 | MAC、Chassis、Port、TTL、描述、能力、管理地址 | 相同 | 一致 |

项目只保留以下非采集差异：

- 使用Cobra以及`--interfaces`，并兼容多个显式接口；单显式接口行为与参考版一致；
- Kubernetes访问使用官方`client-go`，而不是参考版中的手写HTTP客户端；
- 写入`topology.demo.ngg.io/*`，以便PRC继续读取；
- 作为DaemonSet持续周期执行，而不是单独的命令行输出工具；
- 当前业务协议最多接受两个不同Leaf。

日志统一使用`[LLDP-AGENT]`前缀，包含启动参数、接口选择、Socket打开、
报文接收、解析结果、Node Patch以及错误位置。

## 2. 目录文件

| 文件 | 功能 |
|---|---|
| `main.go` | Cobra入口、参数、Kubernetes认证初始化 |
| `agent.go` | 周期采集、Node读取、元数据比较和Patch编排 |
| `lldp.go` | AF_PACKET Socket、接口过滤和LLDP TLV解析 |
| `bond.go` | 主包到Bond发现包的薄适配层 |
| `pkg/bond/discovery.go` | 物理网卡、链路和Bond自动发现 |
| `pkg/topologyfacts/facts.go` | Leaf集合规范化、Hash和Node元数据协议 |
| `kube_config.go` | In-Cluster/Kubeconfig配置加载 |
| `kube.go` | 官方client-go的Node Get/Patch |
| `bond_test.go` | 主备、LACP、物理口、虚拟口和/proc回退测试 |
| `lldp_test.go` | LLDP报文解析、自反射和故障关闭测试 |

## 3. 网卡选择规则

### 3.1 自动模式

`--interfaces`为空时，Agent读取`--sys-class-net`指定的宿主机sysfs：

1. 枚举网卡；
2. 只选择存在`device`、类型为Ethernet、非无线的物理口；
3. 要求`carrier=1`或`operstate=up`；
4. 从`<接口>/bonding/`读取Bond信息；
5. sysfs信息不完整时回退读取`/proc/net/bonding`；
6. `active-backup`只选Active Slave；
7. 其他Bond模式选择链路及MII状态有效的Slave；
8. 不再回退到“监听全部接口”。

最后一条是故障关闭策略。只有虚拟网卡时Agent明确报错，不会把`poh_*`、
veth或本机反射报文误写为Leaf。

### 3.2 显式模式

可以通过逗号分隔指定接口：

```bash
--interfaces=ens5f1np1,ens8f0np0
```

指定`bond0`时会展开为符合Bond策略的Slave。显式指定普通接口属于运维
覆盖，即使该接口被识别为虚拟接口也允许监听，但疑似本机反射邻居仍会
被过滤。

生产环境如果服务器存在管理网、存储网等多组物理口，建议明确填写承载
业务网络的物理口或Bond，避免把非业务上联纳入Leaf集合。

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
邻居不会在采集阶段被覆盖；当前Node业务协议最多接受两个不同Leaf。

收集窗口由三个条件结束：

- 所有选中接口都是Bond Slave，且每个Slave都收到邻居；
- 收到首个新邻居后，连续`idle-seconds`没有新的唯一邻居；
- 到达`listen-seconds`总超时。

重复邻居只更新最新内容，不重置idle计时。`count>0`时，达到指定的唯一
邻居数量也会结束。

## 5. Node元数据协议

标签：

```text
topology.demo.ngg.io/leaf-set-id=<Leaf集合Hash>
topology.demo.ngg.io/leaf-count=1|2
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
| `--interfaces` | 空 | 自动选择；也可填写接口或Bond，多个用逗号分隔 |
| `--listen-seconds` | 120 | 单轮最长LLDP监听时间，与参考版一致 |
| `--idle-seconds` | 3 | 首个邻居后无新唯一邻居的提前结束时间；0表示禁用 |
| `--count` | 0 | 最大唯一邻居数；0表示使用idle/最大超时 |
| `--resync-seconds` | 30 | 一轮完成后到下一轮开始的等待时间 |
| `--sys-class-net` | `/sys/class/net` | 网卡sysfs根目录 |
| `--kubeconfig` | 空 | 空时优先In-Cluster，也支持显式文件 |
| `--kube-context` | 空 | 显式覆盖Kubeconfig Context |

`NODE_NAME`为必填环境变量，必须与Kubernetes Node的`metadata.name`
完全一致。

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
go test . -run '^TestProcBondingFallbackSelectsActiveBackupSlave$' -v
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
- 自动模式会发现所有有效物理业务/非业务上联，复杂服务器建议显式配置；
- 超过两个不同Leaf会被现有拓扑协议拒绝；
- 完整一轮未收到任何外部邻居时保留Node旧拓扑，不主动删除；
- `idle-seconds=3`适合快速现场验证；双Leaf发送周期不同步时可设为0，使用
  完整`listen-seconds`窗口。
