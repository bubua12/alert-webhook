# 告警过滤功能测试手册

## 配置示例

根据你的需求，在 config.yaml 中配置以下过滤规则：

```yaml
# 告警过滤规则配置
filter:
  # 基于告警名称的过滤规则
  alert_name:
    # 包含规则：只有匹配这些规则的告警才会被发送（支持通配符*）
    include:
      - "HighCPU*"           # 匹配以HighCPU开头的告警
      - "*Memory*"           # 匹配包含Memory的告警
      - "DiskSpaceLow"       # 精确匹配
      - "NetworkLatencyHigh" # 精确匹配
    # 排除规则：匹配这些规则的告警不会被发送（优先级高于include）
    exclude:
      - "*Test*"             # 排除包含Test的告警
      - "DebugAlert"         # 排除调试告警
      
  # 基于告警级别的过滤规则  
  severity:
    # 包含规则：只发送这些级别的告警
    include:
      - "critical"           # 严重告警
      - "warning"            # 警告告警
      - "emergency"          # 紧急告警
    # 排除规则：不发送这些级别的告警
    exclude:
      - "info"               # 排除信息级别告警
      - "none"               # 排除none级别
```

## 过滤规则说明

### 1. 工作原理
- **双重过滤**: 告警必须同时通过 alert_name 和 severity 两个维度的过滤
- **优先级**: exclude 规则优先级高于 include 规则
- **默认行为**: 如果不配置某个维度的规则，该维度不进行过滤

### 2. 通配符支持
- `*` : 匹配任意字符
- `HighCPU*` : 匹配以 "HighCPU" 开头的所有告警
- `*Memory*` : 匹配包含 "Memory" 的所有告警
- `*Test` : 匹配以 "Test" 结尾的所有告警

### 3. 配置场景示例

#### 场景1: 只接收严重告警
```yaml
filter:
  severity:
    include:
      - "critical"
      - "emergency"
```

#### 场景2: 排除测试和调试告警
```yaml
filter:
  alert_name:
    exclude:
      - "*Test*"
      - "*Debug*"
      - "InfoInhibitor"
```

#### 场景3: 只关注特定服务的告警
```yaml
filter:
  alert_name:
    include:
      - "MySQL*"
      - "Redis*" 
      - "*Database*"
  severity:
    exclude:
      - "info"
```

#### 场景4: 完全自定义过滤
```yaml
filter:
  alert_name:
    include:
      - "HighCPU*"
      - "HighMemoryUsage"
      - "DiskSpaceLow"
    exclude:
      - "*Test*"
  severity:
    include:
      - "critical"
      - "warning"
    exclude:
      - "info"
```

## 测试方法

### 1. 配置验证
启动服务时，日志会显示：
```
配置初始化成功
```

### 2. 运行时日志
当接收到告警时，会显示过滤结果：
```
告警 [HighCPUUsage] 级别 [critical] 通过过滤规则
告警 [TestAlert] 级别 [warning] 被过滤规则拦截
过滤后剩余 2 个告警将被发送
```

### 3. 分批发送日志
对于企业微信长消息，会显示：
```
[wechat] 告警分为 2 批发送
[wechat] 发送第 1/2 批消息，包含 3 个告警
[wechat] 第 1 批消息发送成功
```

## 常见用例

1. **生产环境**: 只接收 critical 和 emergency 级别告警
2. **测试环境**: 排除所有包含 "Test" 的告警
3. **特定服务监控**: 只监控数据库、缓存等关键服务告警
4. **减少噪音**: 排除 info 级别的信息性告警

## 注意事项

1. **不配置表示不限制**: 如果不配置 filter 部分，所有告警都会被发送
2. **规则匹配顺序**: exclude 优先于 include
3. **大小写敏感**: 规则匹配区分大小写
4. **性能影响**: 过滤规则在内存中执行，对性能影响很小