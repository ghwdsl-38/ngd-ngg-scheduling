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
| Bond采集基础流程 | 每个选中Slave分别接收邻居 | 相同 | 一致 |
| 自动Bond完成条件 | 在接口覆盖基础上要求不同Chassis：主备1个，其他模式2个 | 仅检查接口覆盖 | 按生产双Leaf要求增强 |
| 显式Bond主接口 | 展开为所有Link/MII有效Slave，不监听Master | 直接监听指定接口 | 按显式范围全量盘点要求增强 |
| 本机反射 | 采集时标记，Leaf筛选阶段剔除 | 相同 | 一致 |
| LLDP解析 | MAC、Chassis、Port、TTL、描述、能力、管理地址 | 相同 | 一致 |

项目只保留以下非采集差异：

- 使用Cobra以及`--interfaces`，并兼容多个显式接口；单显式接口行为与参考版一致；
- Kubernetes访问使用官方`client-go`，而不是参考版中的手写HTTP客户端；
- 写入`topology.demo.ngg.io/*`，以便PRC继续读取；
- 作为DaemonSet持续周期执行，而不是单独的命令行输出工具；
- 自动模式最多接受两个不同Leaf；显式接口模式保留采集窗口内的全部Leaf。

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
8. 如果存在`bond0`，只使用`bond0`，不混入管理网、存储网或其他Bond；
9. 没有`bond0`时，保持`lldp-new-2`的物理网卡自动发现；
10. 不再回退到“监听全部接口”。

最后一条是故障关闭策略。只有虚拟网卡时Agent明确报错，不会把`poh_*`、
veth或本机反射报文误写为Leaf。

### 3.2 显式模式

可以通过逗号分隔指定接口：

```bash
--interfaces=ens5f1np1,ens8f0np0
```

显式接口参数只限制“在哪些接口范围采集”，不限制Leaf数量：

- 显式普通网卡或Bond Slave：只监听指定接口；
- 显式`bond0`等Bond Master：展开为所有Link/MII有效Slave，不监听Master；
- `active-backup`显式展开时Active和Standby状态仍写入链路明细，但为了盘点
  指定范围内的全部Leaf，两条有效Slave都会参与监听；
- 多接口模式要求每个选中接口至少收到一个有效外部邻居后，才启动3秒idle
  计时；覆盖未完成时最长等待65秒；
- 最终Leaf按照LLDP Chassis ID去重，不执行自动模式的1/2个Leaf数量目标。

显式指定普通接口属于运维覆盖，即使该接口被识别为虚拟接口也允许监听，
但疑似本机反射邻居仍会被过滤。

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
邻居不会在采集阶段被覆盖；最终Leaf集合按Chassis ID去重。

采集仍沿用`lldp-new-2`的单Socket、ifindex过滤、邻居身份和重复帧更新流程。
在此基础上，Bond完成条件增加了不同Leaf校验：

- `active-backup`：Active Slave收到一个有效外部Leaf后完成；
- 其他Bond模式：至少两个选中接口收到邻居，并且Chassis ID去重后恰有两个
  不同Leaf，才提前完成；
- Bond目标未满足时禁用通用idle提前退出，最长等待`listen-seconds`；
- 普通物理口收到首个新邻居后，连续`idle-seconds`没有新邻居即可结束；
- 到达`listen-seconds`总超时。

上述1/2个Leaf目标只适用于`--interfaces`为空的自动模式。显式模式不按Bond
模式提前结束，而是：所有指定/展开接口完成覆盖后，连续`idle-seconds`没有
发现新的不同Chassis才结束；没有完成接口覆盖时等待到总超时。总超时后只要
至少发现一个有效外部Leaf，就保存已发现的全部Leaf及链路。

重复邻居只更新最新内容，不重置idle计时。Chassis ID是判断物理Leaf是否
不同的依据；System Name是写入Node并与Algorithm配置匹配的Leaf ID。两个
接口收到同一个Chassis只会形成一个Leaf集合成员，但两条物理链路明细均可
保留。`count>0`是人工覆盖项，达到指定唯一邻居数量时仍会结束。

非主备Bond等待65秒后仍不足两个不同Chassis时，本轮返回“不完整”错误，
不会用单Leaf结果覆盖Node上一次成功的双Leaf拓扑。这样不会把“两张网卡都
连到同一交换机”误报成双Leaf，也不会因一次丢包立即缩减已有拓扑。

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
| `--interfaces` | 空 | 自动选择；也可填写接口或Bond，多个用逗号分隔 |
| `--listen-seconds` | 65 | 单轮最长监听时间，覆盖两个常见30秒LLDP发送周期 |
| `--idle-seconds` | 3 | 首个邻居后无新唯一邻居的提前结束时间；0表示禁用 |
| `--count` | 0 | 最大唯一邻居数；0表示使用idle/最大超时 |
| `--resync-seconds` | 180 | 相邻两轮开始时间的目标间隔，即每3分钟启动一轮 |
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
- 自动模式优先且独占`bond0`；没有`bond0`时才发现其他有效物理口；
- 自动模式超过两个不同Leaf会拒绝，非主备Bond不足两个不同Leaf时保留旧拓扑；
- 显式模式不限制Leaf数量，按Chassis去重并保存采集窗口内的全部Leaf；
- 自动Bond目标未完成、显式接口覆盖未完成时，都不会被3秒idle提前截断，
  而是最多等待65秒。
