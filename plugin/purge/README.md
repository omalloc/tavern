# Purge Plugin

`purge` 插件为 Tavern 提供缓存清理（刷新）功能。支持通过特定的 HTTP 请求清理已缓存的单文件或目录。

## 特性

- **单文件清理**：精确清理指定 URL 的缓存。
- **目录清理**：清理指定前缀（目录）下的所有缓存文件。
- **访问控制**：基于 IP 的白名单访问控制。
- **自定义 Header**：可自定义用于区分清理类型的 HTTP Header 名称。

## 配置

在 `config.yaml` 的 `plugins` 部分进行配置：

```yaml
plugins:
  - name: purge
    options:
      allow_hosts:
        - "127.0.0.1"
        - "::1"
      header_name: "Purge-Type" # 默认为 Purge-Type
      log_path: "logs/purge.log"
```

### 配置项说明

| 配置项 | 类型 | 描述 | 默认值 |
| :--- | :--- | :--- | :--- |
| `allow_hosts` | `[]string` | 允许执行 PURGE 操作的客户端 IP 列表 | 必填 |
| `header_name` | `string` | 指定清理类型的 Header 名称 | `Purge-Type` |
| `log_path` | `string` | 清理日志存放路径 | - |

## API 说明

### 1. 清理缓存

使用 `PURGE` 方法请求需要清理的资源 URL。

- **方法**: `PURGE`
- **URL**: 资源的完整 URL
- **Headers**:
    - `Purge-Type`: (可选) 
        - `file`: (默认) 清理单文件。
        - `dir`: 清理该 URL 路径下的所有缓存（目录刷新）。

#### 响应状态码

- `200 OK`: 清理成功。
- `403 Forbidden`: 客户端 IP 不在 `allow_hosts` 白名单中。
- `404 Not Found`: 指定的资源在缓存中不存在。
- `500 Internal Server Error`: 服务器内部错误。

#### 使用示例

**清理单文件：**

```bash
curl -X PURGE http://example.com/static/js/main.js
```

**清理目录：**

```bash
curl -X PURGE -H "Purge-Type: dir" http://example.com/static/js/
```

### 2. 任务查询 (开发中)

查询当前的清理任务状态。

- **方法**: `GET`
- **URL**: `/plugin/purge/tasks`

---

## 注意事项

1. 确保执行清理请求的机器 IP 已添加到 `allow_hosts` 中。
2. 目录清理（`Purge-Type: dir`）会递归清理该路径下的所有子文件，请谨慎操作。
3. 如果配置了 `header_name`，请在请求时使用自定义的 Header 名称。
